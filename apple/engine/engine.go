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

// Package engine is the Lattice mesh engine for Apple platforms, embedded in
// a Network Extension (NEPacketTunnelProvider) via gomobile bind.
//
// It runs the same enrollment and WireGuard data plane as the standalone
// agent (NATS registration, netmap convergence, wireguard-go), with packets
// bridged to the NE flow through packetTUN (unexported wireguard-go tun.Device adapter) instead of a kernel TUN. Route
// and address setup are owned by the Swift side via
// NEPacketTunnelNetworkSettings, reported through EngineDelegate.OnTunnelUp.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	latticeagent "github.com/alatticeio/lattice/internal/agent"
	agentconfig "github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/internal/agent/infra"
	agentlog "github.com/alatticeio/lattice/internal/agent/log"

	// Required at build time by gomobile bind (bind glue lives here).
	_ "golang.org/x/mobile/bind"

	wgtypes "golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// DefaultMTU is the tunnel MTU used when the config omits one. 1280 keeps
// WireGuard's overhead inside the smallest link MTU (Apple NE default).
const DefaultMTU = 1280

// Engine events reported to the Swift side via EngineDelegate.OnEvent.
const (
	EventConnecting   = "connecting"
	EventConnected    = "connected"
	EventDisconnected = "disconnected"
	eventErrorPrefix  = "error: "
)

// EngineDelegate is implemented on the Swift side; gomobile generates the
// corresponding protocol for NEPacketTunnelProvider to conform to.
type EngineDelegate interface {
	// DeliverPacket hands one decrypted inbound packet to the NE flow.
	DeliverPacket(packet []byte) error
	// OnEvent reports engine state transitions ("connecting", "connected",
	// "disconnected", or "error: <message>").
	OnEvent(event string)
	// OnTunnelUp reports this node's assigned overlay IP once registration
	// completes. The Swift side then applies NEPacketTunnelNetworkSettings.
	OnTunnelUp(overlayIP string)
	// OnPeerStates reports per-peer connection quality as a JSON object
	// mapping peer name to lifecycle state ("ice-ready" = direct,
	// "lrp-ready" = relayed, "probing" = still negotiating). Emitted
	// whenever the snapshot changes while the tunnel is up.
	OnPeerStates(statesJSON string)
}

type engineConfig struct {
	ServerURL string `json:"serverURL"`
	Token     string `json:"token"`
	Name      string `json:"name"`
	MTU       int    `json:"mtu"`
}

// Engine is the long-running mesh engine. Create one per tunnel session via
// NewEngine, call Start once, feed system packets with SendPacket, and Stop
// when the tunnel tears down.
type Engine struct {
	cfg      engineConfig
	delegate EngineDelegate
	tun      *packetTUN

	mu       sync.Mutex
	running  bool
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// NewEngine validates the config and returns an engine bound to delegate.
// configJSON: {"serverURL":"http://host:8080","token":"lt-...","name":"my-mac","mtu":1280}
func NewEngine(configJSON string, delegate EngineDelegate) (*Engine, error) {
	var cfg engineConfig
	if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.ServerURL == "" {
		return nil, errors.New("config: serverURL is required")
	}
	if cfg.Token == "" {
		return nil, errors.New("config: token is required")
	}
	if cfg.Name == "" {
		cfg.Name = "lattice-ne"
	}
	if cfg.MTU <= 0 {
		cfg.MTU = DefaultMTU
	}
	return &Engine{
		cfg:      cfg,
		delegate: delegate,
	}, nil
}

// Start launches the engine in the background and returns immediately.
// Registration and data-plane bring-up happen asynchronously; progress is
// reported through the delegate.
func (e *Engine) Start() error {
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
		defer e.emit(EventDisconnected)
		e.run(ctx)
	}()
	return nil
}

// SendPacket injects one packet from the NE flow into the tunnel.
// Packets are dropped (counted) if the queue is full.
func (e *Engine) SendPacket(packet []byte) error {
	t := e.getTUN()
	if t == nil {
		return errors.New("engine not started")
	}
	return t.WriteInbound(packet)
}

// Stop tears the engine down and blocks until the run loop exits.
func (e *Engine) Stop() error {
	e.stopOnce.Do(func() {
		e.mu.Lock()
		cancel := e.cancel
		e.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
	if e.done != nil {
		select {
		case <-e.done:
		case <-time.After(10 * time.Second):
		}
	}
	e.mu.Lock()
	e.running = false
	e.mu.Unlock()
	return nil
}

func (e *Engine) getTUN() *packetTUN {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tun
}

func (e *Engine) setTUN(t *packetTUN) {
	e.mu.Lock()
	e.tun = t
	e.mu.Unlock()
}

// run is the blocking engine loop: enroll, bring up the node, pump packets,
// and stay converged until the context is cancelled.
func (e *Engine) run(ctx context.Context) {
	e.emit(EventConnecting)

	// The agent internals read these globals for NATS identity and endpoints.
	agentconfig.Conf.AppId = e.cfg.Name
	agentconfig.Conf.ServerUrl = e.cfg.ServerURL
	agentconfig.Conf.WgPort = 0 // random UDP port inside the NE process

	// Register via NATS: enrollment token + public key → identity (JWT) and
	// overlay address. Re-registers are idempotent per name, so reconnects
	// keep the same overlay IP.
	privKey, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		e.emitError(fmt.Errorf("generate key: %w", err))
		return
	}
	peer, err := latticeagent.RegisterSandboxViaNATS(ctx, e.cfg.ServerURL, e.cfg.Token, e.cfg.Name, privKey)
	if err != nil {
		e.emitError(fmt.Errorf("enroll: %w", err))
		return
	}
	if peer.Address == nil || *peer.Address == "" {
		e.emitError(errors.New("enroll: server assigned no overlay address"))
		return
	}
	localIP := *peer.Address
	if peer.LrpUrl != "" {
		agentconfig.Conf.EnableLrp = true
		agentconfig.Conf.RelayURL = peer.LrpUrl
	}

	t := newPacketTUN("lattice", e.cfg.MTU)
	e.setTUN(t)

	node, err := latticeagent.NewNode(ctx, &latticeagent.NodeConfig{
		Logger:             agentlog.GetLogger("lattice-ne"),
		Port:               0,
		ShowLog:            false,
		Flags:              agentconfig.Conf,
		CustomTUN:          t,
		CustomName:         "lattice",
		CurrentPeer:        peer,
		ProvisionerFactory: newNEProvisionerFactory(localIP, "lattice"),
	})
	if err != nil {
		e.emitError(fmt.Errorf("create node: %w", err))
		return
	}

	node.GetNetworkMap = func() (*infra.Message, error) {
		return node.GetNetMap(peer.Token)
	}

	if err := node.Start(ctx); err != nil {
		e.emitError(fmt.Errorf("start node: %w", err))
		return
	}

	go node.StartHeartbeat(ctx)
	go e.periodicRefresh(ctx, node)
	go e.pollPeerStates(ctx, node)

	// Deliver decrypted packets to the Swift side.
	go func() {
		for {
			pkt, ok := t.PopOutbound()
			if !ok {
				if ctx.Err() != nil {
					return
				}
				// No packet pending — avoid a hot loop.
				select {
				case <-time.After(2 * time.Millisecond):
				case <-ctx.Done():
					return
				}
				continue
			}
			if err := e.delegate.DeliverPacket(pkt); err != nil {
				return
			}
		}
	}()

	e.emit(EventConnected)
	e.emitTunnelUp(localIP)

	<-ctx.Done()

	_ = node.Stop()
	_ = t.Close()
	e.setTUN(nil)
}

func (e *Engine) periodicRefresh(ctx context.Context, node *latticeagent.Node) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = node.RefreshConfig(ctx)
		}
	}
}

// pollPeerStates watches the probe factory's per-peer connection lifecycle
// and pushes the snapshot to Swift whenever it changes — this is what lets
// the UI show 直连 (ice-ready) vs 经中继 (lrp-ready) per peer.
func (e *Engine) pollPeerStates(ctx context.Context, node *latticeagent.Node) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var last string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			states := node.ConnectionStates()
			if len(states) == 0 {
				continue
			}
			blob, err := json.Marshal(states)
			if err != nil {
				continue
			}
			if string(blob) == last {
				continue
			}
			last = string(blob)
			if e.delegate != nil {
				e.delegate.OnPeerStates(last)
			}
		}
	}
}

func (e *Engine) emit(event string) {
	if e.delegate != nil {
		e.delegate.OnEvent(event)
	}
}

func (e *Engine) emitError(err error) {
	if e.delegate != nil {
		e.delegate.OnEvent(eventErrorPrefix + err.Error())
	}
}

func (e *Engine) emitTunnelUp(ip string) {
	if e.delegate != nil {
		e.delegate.OnTunnelUp(ip)
	}
}
