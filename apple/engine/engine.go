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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

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
	EventConnecting = "connecting"
	// EventAwaitingApproval means registration succeeded but the workspace holds
	// the device for administrator approval (ADR-0003). The engine keeps waiting
	// and continues on its own once the device is approved.
	EventAwaitingApproval = "awaiting-approval"
	EventConnected        = "connected"
	EventDisconnected     = "disconnected"
	eventErrorPrefix      = "error: "
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
	// "relay-ready" = relayed, "probing" = still negotiating). Emitted
	// whenever the snapshot changes while the tunnel is up.
	OnPeerStates(statesJSON string)
	// OnRoutesChanged reports the current set of extra CIDRs (beyond the
	// base overlay /24) this node should route into the tunnel, as a JSON
	// array of strings, e.g. ["192.168.1.0/24"] or ["0.0.0.0/0"] for an
	// Exit Node. Emitted once when the tunnel comes up and again whenever
	// the set changes (a route was selected/deselected, or a selected
	// provider changed/cleared what it advertises).
	OnRoutesChanged(routesJSON string)
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
	privKey  wgtypes.Key

	mu       sync.Mutex
	node     *latticeagent.Node // set once the node exists; read by Peers
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
	agentconfig.Conf.AppId = infra.NormalizeAppID(e.cfg.Name)
	agentconfig.Conf.ServerUrl = e.cfg.ServerURL
	agentconfig.Conf.WgPort = 0 // random UDP port inside the NE process

	// Register via NATS: enrollment token + public key → identity (JWT) and
	// overlay address. Re-registers are idempotent per name, so reconnects
	// keep the same overlay IP — but only if the key itself is stable too:
	// the server rejects a re-registration under the same name with a
	// different key ("public key mismatch ... requires re-enrollment").
	// Every engine start used to call GeneratePrivateKey() fresh, so any NE
	// process restart (crash, OS jetsam, a transient error tearing the
	// extension down) rotated identity and locked itself out. Persisting
	// the key to disk keeps it stable across restarts.
	privKey, err := loadOrCreatePrivateKey()
	if err != nil {
		e.emitError(fmt.Errorf("generate key: %w", err))
		return
	}
	e.mu.Lock()
	e.privKey = privKey
	e.mu.Unlock()
	peer, err := latticeagent.RegisterSandboxViaNATSNotify(ctx, e.cfg.ServerURL, e.cfg.Token, e.cfg.Name, privKey,
		func() { e.emit(EventAwaitingApproval) })
	if err != nil {
		e.emitError(fmt.Errorf("enroll: %w", err))
		return
	}
	if peer.Address == nil || *peer.Address == "" {
		e.emitError(errors.New("enroll: server assigned no overlay address"))
		return
	}
	localIP := *peer.Address
	if peer.RelayURL != "" {
		agentconfig.Conf.EnableRelay = true
		// Respect an explicit override (env) — the advertised URL may not be
		// reachable from this network while an operator-provided one is.
		if agentconfig.Conf.RelayURL == "" {
			agentconfig.Conf.RelayURL = peer.RelayURL
		}
	}

	t := newPacketTUN("lattice", e.cfg.MTU)
	e.setTUN(t)

	// Diagnostic: dup2 fds 1+2 into a sandbox-writable file — slog captures
	// os.Stdout at init and wireguard-go holds the original stderr fd, so
	// reassigning the os.Stderr variable alone captures nothing.
	// The sandboxed appex cannot write the shared TMPDIR or the real home —
	// its writable home is CFFIXED_USER_HOME (the container Data directory on
	// the macOS sandbox).
	home := os.Getenv("CFFIXED_USER_HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home != "" {
		cacheDir := filepath.Join(home, "Library", "Caches")
		_ = os.MkdirAll(cacheDir, 0755)
		if f, ferr := os.OpenFile(
			filepath.Join(cacheDir, "lattice-ne.log"),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644,
		); ferr == nil {
			_ = unix.Dup2(int(f.Fd()), 1)
			_ = unix.Dup2(int(f.Fd()), 2)
		}
	}
	agentlog.SetLevel("debug")

	node, err := latticeagent.NewNode(ctx, &latticeagent.NodeConfig{
		Logger:             agentlog.GetLogger("lattice-ne"),
		Port:               0,
		ShowLog:            true,
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

	e.mu.Lock()
	e.node = node
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.node = nil
		e.mu.Unlock()
	}()

	// LatticeDNS: the built-in *.lattice responder resolves from the live
	// peer table; without this the interceptor stays dormant.
	t.SetPeerSource(node.GetPeerManager().GetAll)

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
	go e.pollRoutes(ctx, node)

	// Deliver decrypted packets to the Swift side. PopOutbound blocks on
	// the channel, so this goroutine sleeps at the OS level when idle —
	// a polling variant here wakes the CPU ~1000x/s and NE kills the
	// process for exceeding the CPU-wake limit within minutes.
	go func() {
		for {
			pkt, ok := t.PopOutbound()
			if !ok {
				return
			}
			if err := e.delegate.DeliverPacket(pkt); err != nil {
				return
			}
		}
	}()

	if blob, err := json.Marshal(computeExtraRoutes(node.GetPeerManager().GetAll())); err == nil {
		e.emitRoutesChanged(string(blob))
	}

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
// the UI show 直连 (ice-ready) vs 经中继 (relay-ready) per peer.
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

// pollRoutes watches the peer manager's AllowedIPs and pushes the extra-
// routes snapshot to Swift whenever it changes (a route selection changed,
// or RefreshConfig picked up a provider updating/clearing what it offers).
func (e *Engine) pollRoutes(ctx context.Context, node *latticeagent.Node) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var last string
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			blob, err := json.Marshal(computeExtraRoutes(node.GetPeerManager().GetAll()))
			if err != nil {
				continue
			}
			if string(blob) == last {
				continue
			}
			last = string(blob)
			e.emitRoutesChanged(last)
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

func (e *Engine) emitRoutesChanged(routesJSON string) {
	if e.delegate != nil {
		e.delegate.OnRoutesChanged(routesJSON)
	}
}

// wgIdentityDir resolves the extension's writable, non-purgeable storage
// directory for the persisted WireGuard identity — CFFIXED_USER_HOME is the
// sandboxed container's Data directory on Apple platforms (falls back to
// the process home dir when unset, e.g. under `go test`). Empty return
// means persistence is unavailable; callers fall back to an ephemeral key.
func wgIdentityDir() string {
	home := os.Getenv("CFFIXED_USER_HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Lattice")
}

// PublicKey returns this engine's current WireGuard public key as a base64
// string, or "" if the engine hasn't loaded/generated its identity yet
// (before run() reaches the key-loading step, or Start was never called).
func (e *Engine) PublicKey() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var zero wgtypes.Key
	if e.privKey == zero {
		return ""
	}
	return e.privKey.PublicKey().String()
}

// Peers returns the remote nodes this device knows about as a JSON array:
// [{"appId","name","address","platform","state","online"}]. It comes from the
// tunnel's own network map, so the app can list devices without a management
// login. "[]" until the node exists.
func (e *Engine) Peers() string {
	e.mu.Lock()
	node := e.node
	e.mu.Unlock()
	if node == nil {
		return "[]"
	}
	return peerListJSON(node.GetPeerManager().GetAll(), node.ConnectionStates(), infra.NormalizeAppID(e.cfg.Name))
}

// ResetIdentity deletes the persisted WireGuard identity file, if any, so
// the next engine Start generates and persists a brand-new one. It is a
// package-level function, not an Engine method, because it must be
// callable before any Engine exists — the Swift side calls this ahead of
// constructing a fresh Engine for a user-initiated identity reset (see
// PacketTunnelProvider.startTunnel's resetIdentity flag handling).
func ResetIdentity() error {
	dir := wgIdentityDir()
	if dir == "" {
		return nil
	}
	path := filepath.Join(dir, "wg-identity.key")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// loadOrCreatePrivateKey returns this device's stable WireGuard identity,
// generating and persisting one on first run. The server keys peer identity
// on (name, public key) and rejects a same-name registration under a
// different key, so a fresh key every engine start (Network Extension
// restarts are frequent and often outside app control) permanently locks
// the device out until the stale server-side record is cleared.
func loadOrCreatePrivateKey() (wgtypes.Key, error) {
	dir := wgIdentityDir()
	path := ""
	if dir != "" {
		path = filepath.Join(dir, "wg-identity.key")
		if data, err := os.ReadFile(path); err == nil {
			if key, err := wgtypes.ParseKey(strings.TrimSpace(string(data))); err == nil {
				return key, nil
			}
		}
	}
	key, err := wgtypes.GeneratePrivateKey()
	if err != nil {
		return wgtypes.Key{}, err
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0700); err == nil {
			_ = os.WriteFile(path, []byte(key.String()), 0600)
		}
	}
	return key, nil
}
