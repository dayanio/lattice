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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/signal"

	wgconn "golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

var _ infra.RelayChannel = (*TCPClient)(nil)

const (
	// initialReconnectBackoff / maxReconnectBackoff bound the relay client's
	// reconnect retry loop. The relay is the fallback transport, so the
	// client must keep retrying for the life of the process — the previous
	// behavior (single connect, permanent loss on first error) silently
	// disabled the fallback path until restart.
	initialReconnectBackoff = 1 * time.Second
	maxReconnectBackoff     = 30 * time.Second
)

// TCPClient implements infra.Relay using a persistent TCP connection with
// HTTP upgrade handshake, buffered writer, and keepalive loop.
//
// The connection is supervised: a background loop redials with capped
// exponential backoff after any disconnect, re-registers, and resumes
// serving WireGuard traffic. Frames queued while disconnected are dropped
// (they are stale encrypted datagrams; WireGuard retransmits at its own
// layer).
type TCPClient struct {
	*relayClient
	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer

	sendCh chan []byte

	// writeMu serialises writes to the buffered writer. writerLoop and the
	// per-peer auth exchange (run from the ReceiveFunc goroutine right after a
	// connect) both write to it; without this their frames could interleave.
	writeMu sync.Mutex

	// connected is a fresh channel per connection lifetime: non-nil while
	// a connection is up, closed and nil'ed when it drops. Snapshotted by
	// goroutines that need to wait out a disconnect.
	connected chan struct{}

	// authPending is set after each successful Register; ReceiveFunc — the
	// sole reader of the stream once it starts — consumes it and performs
	// the per-peer auth exchange (doing it in the supervisor would race
	// the ReceiveFunc reader after a reconnect).
	authPending atomic.Bool

	closed atomic.Bool
}

// NewTCPClient creates a new TCP Relay client and starts its connect
// supervisor. The first dial happens in the background: construction does
// not fail when the relay is unreachable (the agent starts degraded and
// the supervisor retries with backoff). The URL may carry a
// "?token=secret" query parameter; it is stripped before dialing and
// presented in the Register frame. privateKey is this peer's WireGuard
// private key, used to answer the relay's per-peer auth challenge.
func NewTCPClient(ctx context.Context, localID infra.PeerID, url string, privateKey wgtypes.Key, onMessage func(ctx context.Context, remoteId infra.PeerID, packet *signal.SignalPacket) error) (*TCPClient, error) {
	serverURL, authToken := splitURLToken(url)
	ctx, cancel := context.WithCancel(ctx)
	c := &TCPClient{
		relayClient: &relayClient{
			ctx:        ctx,
			cancel:     cancel,
			log:        log.GetLogger("relay-tcp"),
			localId:    localID,
			serverURL:  serverURL,
			authToken:  authToken,
			privateKey: [KeySize]byte(privateKey),
			probeCh:    make(chan *Task, probeChanSize),
			onMessage:  onMessage,
		},
		sendCh: make(chan []byte, sendChanDepth),
	}

	go c.probeWorker()
	go c.run()
	go c.writerLoop()
	go c.keepaliveLoop()

	return c, nil
}

// run is the connect supervisor: dial, register, wait for the connection
// to die, redial with capped exponential backoff. Runs until Close.
func (c *TCPClient) run() {
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

		// Block until this connection drops, then loop and redial.
		if ch := c.connectedCh(); ch != nil {
			select {
			case <-ch:
			case <-c.ctx.Done():
				return
			}
		}
	}
}

// Connect establishes the TCP connection, performs the HTTP Upgrade
// handshake, and sends the Relay Register frame. One attempt; the run loop
// owns reconnection.
func (c *TCPClient) Connect() error {
	conn, err := net.Dial("tcp", c.serverURL)
	if err != nil {
		return err
	}
	// Exit-node bulk traffic rides this single connection; the default
	// socket buffers cap throughput at high RTT and starve sendCh.
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(4 << 20)
		_ = tcp.SetReadBuffer(4 << 20)
		BindToPhysicalIfc(tcp)
	}

	req, err := http.NewRequest("GET", "/ferry/v1/upgrade", nil)
	if err != nil {
		conn.Close() //nolint:errcheck
		return err
	}
	req.Header.Set("Upgrade", "relay")
	req.Header.Set("Connection", "Upgrade")

	if err = req.Write(conn); err != nil {
		conn.Close() //nolint:errcheck
		return err
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req) //nolint:bodyclose // resp.Body wraps conn; we take ownership of the raw connection
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close() //nolint:errcheck
		return fmt.Errorf("upgrade failed: %v", err)
	}
	// resp.Body wraps the underlying conn reader. We discard the upgrade response
	// and take ownership of the raw connection, so we must NOT close resp.Body.

	c.mu.Lock()
	c.conn = conn
	c.reader = reader
	c.writer = bufio.NewWriterSize(conn, writerBufSize)
	c.connected = make(chan struct{})
	c.mu.Unlock()

	if err = c.register(c); err != nil {
		c.disconnectIfCurrent(conn)
		return err
	}
	c.authPending.Store(true)
	return nil
}

// connectedCh snapshots the current connection's lifetime channel.
func (c *TCPClient) connectedCh() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// disconnectIfCurrent tears down connection state if it still belongs to
// stale. The identity check keeps a late read error from a dead connection
// from destroying the state of a connection established afterwards.
func (c *TCPClient) disconnectIfCurrent(stale net.Conn) {
	if stale == nil {
		return
	}
	c.mu.Lock()
	if c.conn != stale {
		c.mu.Unlock()
		return
	}
	c.conn = nil
	c.reader = nil
	c.writer = nil
	ch := c.connected
	c.connected = nil
	c.mu.Unlock()

	if ch != nil {
		close(ch)
	}
	_ = stale.Close()
}

// Close cancels the client context and closes the underlying TCP connection.
// The supervisor exits; ReceiveFunc returns a permanent error so WireGuard's
// receive routine tears down with the client.
func (c *TCPClient) Close() error {
	c.closed.Store(true)
	c.cancel()
	c.disconnectIfCurrent(c.currentConn())
	return nil
}

func (c *TCPClient) currentConn() net.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn
}

// RemoteAddr returns the remote address of the TCP connection.
func (c *TCPClient) RemoteAddr() net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return c.conn.RemoteAddr()
	}
	return nil
}

// Send enqueues a pre-marshaled Relay frame for the writer goroutine. While
// disconnected, frames are dropped: they are stale encrypted datagrams and
// WireGuard retransmits them itself.
// Connected reports whether the relay TCP connection is currently up.
func (c *TCPClient) Connected() bool {
	return c.connectedCh() != nil
}

func (c *TCPClient) Send(ctx context.Context, targetId uint64, relayType uint8, data []byte) error {
	if c.connectedCh() == nil {
		return errors.New("relay: disconnected")
	}
	frame := c.makeFrame(targetId, relayType, data)
	select {
	case c.sendCh <- frame:
		return nil
	default:
		c.log.Warn("send channel full, dropping frame", "dst", targetId)
		return fmt.Errorf("relay: send channel full")
	}
}

// writerLoop is the sole goroutine that writes to the TCP connection. It
// survives reconnects: on a write failure it discards queued frames and
// waits for the supervisor to re-establish the connection.
func (c *TCPClient) writerLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case frame := <-c.sendCh:
			// Batch drain: grab any additional frames ready to coalesce
			// into a single buffered write + flush.
			pending := [][]byte{frame}
			for i, n := 0, len(c.sendCh); i < n; i++ {
				pending = append(pending, <-c.sendCh)
			}
			if !c.writeFrames(pending) {
				c.dropPending()
			}
		}
	}
}

// writeFrames appends all frames to the buffered writer and flushes once.
// Returns false (and tears down the current connection) on any write error.
func (c *TCPClient) writeFrames(frames [][]byte) bool {
	c.mu.Lock()
	conn := c.conn
	w := c.writer
	c.mu.Unlock()
	if conn == nil || w == nil {
		return false
	}
	if !c.flushFrames(w, frames) {
		c.disconnectIfCurrent(conn)
		return false
	}
	return true
}

// flushFrames appends the frames to w and flushes, under writeMu.
func (c *TCPClient) flushFrames(w *bufio.Writer, frames [][]byte) bool {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	for _, f := range frames {
		if _, err := w.Write(f); err != nil {
			return false
		}
	}
	return w.Flush() == nil
}

func (c *TCPClient) dropPending() {
	for {
		select {
		case <-c.sendCh:
		default:
			return
		}
	}
}

// keepaliveLoop sends KeepAlive frames every 20 seconds; the server resets
// its read deadline on each one.
func (c *TCPClient) keepaliveLoop() {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.Send(c.ctx, 0, KeepAlive, nil) //nolint:errcheck
		}
	}
}

// performAuthExchange answers the relay's per-peer auth challenge on the
// just-established connection. Returns true when the caller should proceed
// reading frames (exchange completed, or the relay turned out to be a
// legacy one that never challenges); false when the connection was torn
// down and the outer loop should wait for a reconnect.
func (c *TCPClient) performAuthExchange(conn net.Conn, reader *bufio.Reader) bool {
	_ = conn.SetReadDeadline(time.Now().Add(authChallengeWait))

	headBufp := GetHeaderBuffer()
	headBuf := *headBufp
	_, err := io.ReadFull(reader, headBuf)
	if err != nil {
		PutHeaderBuffer(headBufp)
		// No challenge in time: a legacy relay already accepted the bare
		// Register — proceed unverified.
		c.log.Debug("no auth challenge received, assuming legacy relay")
		return true
	}
	header, perr := Unmarshal(headBuf)
	PutHeaderBuffer(headBufp)
	if perr != nil || header.Cmd != AuthChallenge || header.PayloadLen != KeySize {
		// The frame we consumed cannot be re-interpreted — the stream
		// framing is lost, so the connection must be rebuilt.
		c.log.Warn("unexpected frame while awaiting auth challenge", "cmd", headerCmd(header), "err", perr)
		c.disconnectIfCurrent(conn)
		return false
	}

	var challenge [KeySize]byte
	if _, err = io.ReadFull(reader, challenge[:]); err != nil {
		c.disconnectIfCurrent(conn)
		return false
	}
	resp, err := c.computeAuthResponse(challenge)
	if err != nil {
		c.log.Error("compute auth response failed", err)
		c.disconnectIfCurrent(conn)
		return false
	}

	h := Header{PayloadLen: uint32(len(resp)), Cmd: AuthResponse, Seq: c.nextSeq()}
	frame := make([]byte, HeaderSize+len(resp))
	copy(frame, h.Marshal())
	copy(frame[HeaderSize:], resp[:])
	if !c.writeFramesTo(conn, [][]byte{frame}) {
		return false
	}
	c.log.Debug("peer-auth response sent")
	return true
}

// headerCmd safely extracts the command byte for logging.
func headerCmd(h *Header) uint8 {
	if h == nil {
		return 0
	}
	return h.Cmd
}

// writeFramesTo writes complete frames on the connection identified by
// conn (no-op failure if the current connection has moved on).
func (c *TCPClient) writeFramesTo(conn net.Conn, frames [][]byte) bool {
	c.mu.Lock()
	cur := c.conn
	w := c.writer
	c.mu.Unlock()
	if cur != conn || w == nil {
		return false
	}
	if !c.flushFrames(w, frames) {
		c.disconnectIfCurrent(conn)
		return false
	}
	return true
}

// ReceiveFunc returns a WireGuard ReceiveFunc that reads incoming Relay
// frames. It survives reconnects: on a connection error it waits for the
// supervisor to re-establish the connection instead of returning an error
// (an error would permanently tear down WireGuard's receive routine). A
// permanent error is returned only once the client is closed.
func (c *TCPClient) ReceiveFunc() wgconn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, eps []wgconn.Endpoint) (n int, err error) {
		for {
			if c.closed.Load() {
				return 0, io.ErrClosedPipe
			}

			c.mu.Lock()
			conn := c.conn
			reader := c.reader
			c.mu.Unlock()

			if conn == nil || reader == nil {
				// Not connected: wait out the gap (the supervisor redials).
				select {
				case <-c.ctx.Done():
					return 0, c.ctx.Err()
				case <-time.After(500 * time.Millisecond):
				}
				continue
			}

			// Per-peer auth (ADR-0004): answer the relay's challenge before
			// serving any frame. Consumed here because this goroutine is
			// the sole reader of the stream once it has started.
			if c.authPending.CompareAndSwap(true, false) {
				if !c.performAuthExchange(conn, reader) {
					continue
				}
			}

			// 30s read deadline — lets this goroutine observe closure/timeout
			// instead of blocking forever on a dead socket.
			_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))

			headBufp := GetHeaderBuffer()
			headBuf := *headBufp

			if _, err = io.ReadFull(reader, headBuf); err != nil {
				PutHeaderBuffer(headBufp)
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					return 0, nil // timeout, not a real error
				}
				c.disconnectIfCurrent(conn)
				continue
			}

			header, parseErr := Unmarshal(headBuf)
			PutHeaderBuffer(headBufp)
			if parseErr != nil {
				c.log.Error("failed to parse Relay header", parseErr)
				return 0, nil
			}

			switch header.Cmd {
			case Probe:
				if header.PayloadLen > MaxProbePayload {
					// The payload is still on the stream: leaving it unread makes
					// the next header parse start in the middle of it.
					c.log.Warn("probe payload too large, discarded", "bytes", header.PayloadLen)
					if _, err = io.CopyN(io.Discard, reader, int64(header.PayloadLen)); err != nil {
						c.disconnectIfCurrent(conn)
					}
					return 0, nil
				}
				buf := make([]byte, header.PayloadLen)
				if _, err = io.ReadFull(reader, buf); err != nil {
					c.disconnectIfCurrent(conn)
					continue
				}
				select {
				case c.probeCh <- &Task{SessionID: uint64(header.ToID), Data: buf}:
				default:
					c.log.Warn("probe task dropped: channel at capacity")
				}
				return 0, nil

			case Forward:
				if int(header.PayloadLen) > len(packets[0]) {
					c.log.Warn("forward payload exceeds buffer, discarded", "need", header.PayloadLen, "have", len(packets[0]))
					if _, err = io.CopyN(io.Discard, reader, int64(header.PayloadLen)); err != nil {
						c.disconnectIfCurrent(conn)
					}
					return 0, nil
				}
				if _, err = io.ReadFull(reader, packets[0][:header.PayloadLen]); err != nil {
					c.disconnectIfCurrent(conn)
					continue
				}
				sizes[0] = int(header.PayloadLen)
				eps[0] = &infra.RelayEndpoint{
					Addr:          infra.RelayFakeAddrPort(uint64(header.ToID)),
					RemoteId:      uint64(header.ToID),
					TransportType: infra.Relay,
				}
				return 1, nil

			default:
				if header.PayloadLen > 0 {
					_, _ = io.CopyN(io.Discard, reader, int64(header.PayloadLen))
				}
				c.log.Warn("unknown Relay command discarded", "cmd", header.Cmd)
				return 0, nil
			}
		}
	}
}

// Write satisfies the writer interface for register().
func (c *TCPClient) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return 0, errors.New("relay: not connected")
	}
	return c.conn.Write(p)
}
