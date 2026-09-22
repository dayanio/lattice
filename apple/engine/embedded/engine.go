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
	"sync"

	"github.com/alatticeio/lattice-shim/shim"
	wgtypes "golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	latticeagent "github.com/alatticeio/lattice/internal/agent"
	agentconfig "github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/internal/agent/gvisor"
	agentlog "github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/agent/infra"
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
		srv.Close()
		return fmt.Errorf("create node: %w", err)
	}

	node.GetNetworkMap = func() (*infra.Message, error) {
		return node.GetNetMap(peer.Token)
	}

	if err := node.Start(ctx); err != nil {
		srv.Close()
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

// Stop is a no-op placeholder for callers that prefer an explicit method
// over cancelling the context passed to Start; Start already tears
// everything down when ctx is cancelled or done. Embedders that called
// Start with context.Background() should cancel their own context instead
// of relying on Stop to do anything — this method exists so the type has a
// symmetrical Start/Stop pair matching apple/engine's Engine.
func (e *EmbeddedEngine) Stop() error {
	return nil
}

// OverlayAddress returns this node's assigned overlay IP, or "" before
// Start has completed registration.
func (e *EmbeddedEngine) OverlayAddress() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.overlay
}
