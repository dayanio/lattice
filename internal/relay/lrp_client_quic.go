// Copyright 2026 The Lattice Authors, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/signal"

	"github.com/quic-go/quic-go"
	wgconn "golang.zx2c4.com/wireguard/conn"
)

var _ infra.Lrp = (*QUICClient)(nil)

// QUICClient implements infra.Lrp using QUIC datagrams for Forward/Probe
// and a QUIC control stream for registration. App-level keepalive is not
// needed because quic.Config.KeepAlivePeriod handles connection liveness.
//
// Like the TCP client, the connection is supervised: the run loop redials
// with capped exponential backoff after any disconnect and re-registers.
type QUICClient struct {
	*lrpClient

	mu        sync.Mutex
	conn      *quic.Conn
	ctrl      *quic.Stream
	connected chan struct{} // fresh per connection lifetime; closed when it drops

	closed atomic.Bool
}

// NewQUICClient creates a new QUIC LRP client and starts its connect
// supervisor. The first dial happens in the background: construction does
// not fail when the relay is unreachable. The URL may carry a
// "?token=secret" query parameter; it is stripped before dialing and
// presented in the Register frame.
func NewQUICClient(ctx context.Context, localID infra.PeerID, url string, onMessage func(ctx context.Context, remoteId infra.PeerID, packet *signal.SignalPacket) error) (*QUICClient, error) {
	serverURL, authToken := splitURLToken(url)
	ctx, cancel := context.WithCancel(ctx)
	c := &QUICClient{
		lrpClient: &lrpClient{
			ctx:       ctx,
			cancel:    cancel,
			log:       log.GetLogger("lrp-quic"),
			localId:   localID,
			serverURL: serverURL,
			authToken: authToken,
			probeCh:   make(chan *Task, probeChanSize),
			onMessage: onMessage,
		},
	}

	go c.probeWorker()
	go c.run()

	return c, nil
}

// run is the connect supervisor: dial, register, wait for the connection
// to die, redial with capped exponential backoff. Runs until Close.
func (c *QUICClient) run() {
	backoff := initialReconnectBackoff
	for {
		if c.closed.Load() {
			return
		}
		if err := c.Connect(); err != nil {
			if c.closed.Load() {
				return
			}
			c.log.Warn("relay connect failed, retrying", "addr", c.serverURL, "err", err, "backoff", backoff)
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < maxReconnectBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = initialReconnectBackoff

		if ch := c.connectedCh(); ch != nil {
			select {
			case <-ch:
			case <-c.ctx.Done():
				return
			}
		}
	}
}

// Connect dials the QUIC server, opens the control stream, and registers.
// One attempt; the run loop owns reconnection.
func (c *QUICClient) Connect() error {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec
		NextProtos:         []string{"lrp"},
	}
	quicCfg := &quic.Config{
		EnableDatagrams: true,
		MaxIdleTimeout:  90 * time.Second,
		KeepAlivePeriod: 25 * time.Second,
	}

	conn, err := quic.DialAddr(c.ctx, c.serverURL, tlsCfg, quicCfg)
	if err != nil {
		return err
	}

	ctrl, err := conn.OpenStreamSync(c.ctx)
	if err != nil {
		conn.CloseWithError(0, "open stream failed") //nolint:errcheck
		return err
	}

	c.mu.Lock()
	c.conn = conn
	c.ctrl = ctrl
	c.connected = make(chan struct{})
	c.mu.Unlock()

	if err = c.register(ctrl); err != nil {
		c.disconnectIfCurrent(conn)
		return err
	}
	return nil
}

func (c *QUICClient) connectedCh() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// disconnectIfCurrent tears down connection state if it still belongs to
// stale (identity check: a late error from a dead connection must not
// destroy the state of a newer one).
func (c *QUICClient) disconnectIfCurrent(stale *quic.Conn) {
	if stale == nil {
		return
	}
	c.mu.Lock()
	if c.conn != stale {
		c.mu.Unlock()
		return
	}
	c.conn = nil
	c.ctrl = nil
	ch := c.connected
	c.connected = nil
	c.mu.Unlock()

	if ch != nil {
		close(ch)
	}
	_ = stale.CloseWithError(0, "connection lost")
}

func (c *QUICClient) currentConn() *quic.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

// Close cancels the client context and closes the underlying QUIC connection.
func (c *QUICClient) Close() error {
	c.closed.Store(true)
	c.cancel()
	c.disconnectIfCurrent(c.currentConn())
	return nil
}

// RemoteAddr returns the remote address of the QUIC connection.
func (c *QUICClient) RemoteAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.RemoteAddr()
	}
	return nil
}

// Send transmits a LRP frame (header + data) as a QUIC datagram. While
// disconnected, sends fail fast: WireGuard retransmits at its own layer.
func (c *QUICClient) Send(ctx context.Context, targetId uint64, lrpType uint8, data []byte) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return errors.New("lrp: disconnected")
	}
	frame := c.makeFrame(targetId, lrpType, data)
	return conn.SendDatagram(frame)
}

// ReceiveFunc returns a WireGuard ReceiveFunc that reads incoming QUIC
// datagrams. It survives reconnects: on a connection error it waits for
// the supervisor to re-establish the connection. A permanent error is
// returned only once the client is closed.
func (c *QUICClient) ReceiveFunc() wgconn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, eps []wgconn.Endpoint) (n int, err error) {
		for {
			if c.closed.Load() {
				return 0, errors.New("lrp: closed")
			}

			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()

			if conn == nil {
				select {
				case <-c.ctx.Done():
					return 0, c.ctx.Err()
				case <-time.After(500 * time.Millisecond):
				}
				continue
			}

			data, recvErr := conn.ReceiveDatagram(c.ctx)
			if recvErr != nil {
				if c.closed.Load() || c.ctx.Err() != nil {
					return 0, recvErr
				}
				c.disconnectIfCurrent(conn)
				// Wait for the supervisor to reconnect, then resume reading.
				if ch := c.connectedCh(); ch != nil {
					<-ch
				}
				continue
			}

			if len(data) < HeaderSize {
				c.log.Warn("datagram too short", "len", len(data))
				continue
			}

			header, parseErr := Unmarshal(data[:HeaderSize])
			if parseErr != nil {
				c.log.Error("failed to parse LRP header", parseErr)
				continue
			}

			switch header.Cmd {
			case Forward:
				payload := data[HeaderSize:]
				if len(payload) > len(packets[0]) {
					c.log.Warn("forward payload exceeds buffer", "need", len(payload), "have", len(packets[0]))
					continue
				}
				copy(packets[0], payload)
				sizes[0] = len(payload)
				eps[0] = &infra.LRPEndpoint{
					Addr:          infra.LrpFakeAddrPort(uint64(header.ToID)),
					RemoteId:      uint64(header.ToID),
					TransportType: infra.LRP,
				}
				return 1, nil

			case Probe:
				payload := data[HeaderSize:]
				buf := make([]byte, len(payload))
				copy(buf, payload)
				select {
				case c.probeCh <- &Task{SessionID: uint64(header.ToID), Data: buf}:
				default:
					c.log.Warn("probe task dropped: channel at capacity")
				}
				continue

			default:
				c.log.Debug("unknown LRP command, ignoring", "cmd", header.Cmd)
				continue
			}
		}
	}
}
