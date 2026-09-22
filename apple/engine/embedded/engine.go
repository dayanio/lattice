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

package embedded

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/alatticeio/lattice-shim/shim"
	wgtypes "golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	latticeagent "github.com/alatticeio/lattice/internal/agent"
	agentconfig "github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/internal/agent/gvisor"
	"github.com/alatticeio/lattice/internal/agent/infra"
	agentlog "github.com/alatticeio/lattice/internal/agent/log"
)

// EmbeddedEngine is a Lattice mesh node that runs entirely in user space:
// no kernel TUN, no Network Extension, no system VPN authorization. Create
// one with New, call Start once, and Dial/Listen once it is up.
type EmbeddedEngine struct {
	cfg Config

	mu      sync.Mutex
	server  *shim.Server
	node    *latticeagent.Node
	overlay string

	// Lifecycle state for the context-free StartAsync/Stop pair. Start(ctx)
	// does not touch these.
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
}

// New validates configJSON and returns an EmbeddedEngine bound to it.
// configJSON: {"serverURL":"http://host:8080","token":"lt-...","name":"my-app"}
func New(configJSON string) (*EmbeddedEngine, error) {
	cfg, err := ParseConfig(configJSON)
	if err != nil {
		return nil, err
	}
	return &EmbeddedEngine{cfg: cfg}, nil
}

// Start registers with the control plane, brings up the WireGuard data
// plane over a gVisor netstack (via lattice-shim's Server), and blocks
// until ctx is cancelled or an unrecoverable error occurs.
func (e *EmbeddedEngine) Start(ctx context.Context) error {
	agentconfig.Conf.AppId = infra.NormalizeAppID(e.cfg.Name)
	agentconfig.Conf.ServerUrl = e.cfg.ServerURL
	agentconfig.Conf.WgPort = 0

	privKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	peer, err := latticeagent.RegisterSandboxViaNATSNotify(ctx, e.cfg.ServerURL, e.cfg.Token, e.cfg.Name, privKey, nil)
	if err != nil {
		return fmt.Errorf("enroll: %w", err)
	}
	if peer.Address == nil || *peer.Address == "" {
		return errors.New("enroll: server assigned no overlay address")
	}
	overlayIP := *peer.Address
	if peer.RelayURL != "" {
		agentconfig.Conf.EnableRelay = true
		if agentconfig.Conf.RelayURL == "" {
			agentconfig.Conf.RelayURL = peer.RelayURL
		}
	}

	srv, err := shim.NewServer(overlayIP, nil)
	if err != nil {
		return fmt.Errorf("netstack: %w", err)
	}
	tunDevice := gvisor.NewTUNAdapter(srv.Channel(), gvisor.InjectIntoChannel(srv.Channel()))

	node, err := latticeagent.NewNode(ctx, &latticeagent.NodeConfig{
		Logger:             agentlog.GetLogger("lattice-embedded"),
		Port:               0,
		ShowLog:            true,
		Flags:              agentconfig.Conf,
		CustomTUN:          tunDevice,
		CustomName:         e.cfg.Name,
		CurrentPeer:        peer,
		ProvisionerFactory: gvisor.NewSandboxProvisionerFactory(overlayIP, e.cfg.Name),
	})
	if err != nil {
		_ = srv.Close()
		return fmt.Errorf("create node: %w", err)
	}

	node.GetNetworkMap = func() (*infra.Message, error) {
		return node.GetNetMap(peer.Token)
	}

	if err := node.Start(ctx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("start node: %w", err)
	}
	go node.StartHeartbeat(ctx)

	e.mu.Lock()
	e.server = srv
	e.node = node
	e.overlay = overlayIP
	e.mu.Unlock()

	<-ctx.Done()

	_ = node.Stop()
	_ = srv.Close()

	e.mu.Lock()
	e.server = nil
	e.node = nil
	e.overlay = ""
	e.mu.Unlock()

	return nil
}

// StartAsync launches the engine in the background and returns immediately;
// registration and data-plane bring-up happen on a goroutine. It is the
// gomobile-facing form of Start — context.Context cannot cross the gobind
// boundary, so the engine manages its own context and Stop cancels it.
// Poll OverlayAddress to see when registration has completed.
func (e *EmbeddedEngine) StartAsync() error {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return errors.New("engine already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	e.running = true
	e.mu.Unlock()

	go func() {
		defer close(e.done)
		defer func() {
			e.mu.Lock()
			e.running = false
			e.cancel = nil
			e.mu.Unlock()
		}()
		// Start tears everything down and clears state once ctx is done.
		_ = e.Start(ctx)
	}()
	return nil
}

// Stop cancels an engine launched with StartAsync and blocks (bounded at
// 10 seconds) until teardown finishes. When the engine was started through
// Start(ctx) instead, the caller's own cancellation already tears it down
// and Stop is a harmless no-op.
func (e *EmbeddedEngine) Stop() error {
	e.mu.Lock()
	cancel := e.cancel
	done := e.done
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	return nil
}

// OverlayAddress returns this node's assigned overlay IP, or "" before
// Start has completed registration.
func (e *EmbeddedEngine) OverlayAddress() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.overlay
}

// netstack returns the running shim server, or an error if Start has not
// yet completed registration.
func (e *EmbeddedEngine) netstack() (*shim.Server, error) {
	e.mu.Lock()
	srv := e.server
	e.mu.Unlock()
	if srv == nil {
		return nil, errors.New("embedded engine not started")
	}
	return srv, nil
}

// Dial dials a remote overlay address. Returns an error if Start has not
// yet completed registration.
func (e *EmbeddedEngine) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	srv, err := e.netstack()
	if err != nil {
		return nil, err
	}
	return srv.Dial(ctx, network, addr)
}

// Listen creates a TCP listener on the overlay netstack. Returns an error
// if Start has not yet completed registration.
func (e *EmbeddedEngine) Listen(network, addr string) (net.Listener, error) {
	srv, err := e.netstack()
	if err != nil {
		return nil, err
	}
	return srv.Listen(network, addr)
}
