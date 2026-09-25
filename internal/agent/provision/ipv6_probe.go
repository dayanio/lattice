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

package provision

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"time"
)

// Defaults for the IPv6 egress probe.
const (
	// DefaultIPv6ProbeInterval is how often an exit node re-checks its IPv6 egress.
	DefaultIPv6ProbeInterval = 60 * time.Second
	// DefaultIPv6FailThreshold is how many consecutive failed probes withdraw a
	// previously reported egress. One success is enough to report it: withdrawing
	// flips every consumer to the IPv6 blackhole, so it must not flap.
	DefaultIPv6FailThreshold = 2
	ipv6DialTimeout          = 3 * time.Second
)

// DefaultIPv6ProbeTargets are well-known IPv6 endpoints (Cloudflare and Google
// public DNS over TCP 443). Reaching any one proves real IPv6 connectivity,
// including a cloud security group that blocks outbound IPv6.
var DefaultIPv6ProbeTargets = []string{"[2606:4700:4700::1111]:443", "[2001:4860:4860::8888]:443"}

// IPv6ProberConfig wires an IPv6Prober. Every dependency is injectable so the
// state machine is testable without a network; zero values take the defaults.
type IPv6ProberConfig struct {
	// IsExit reports whether this node currently acts as an exit node. Only
	// exits probe; a node that is not (or stops being) an exit reports no egress.
	IsExit func() bool
	// Setup installs the IPv6 forwarding and NAT66 rules. It runs when a probe
	// succeeds while egress is not yet reported, and egress is only reported once
	// it returns nil: telling consumers to send IPv6 to an exit that cannot
	// forward it would black-hole them. A failure counts as a failed probe and is
	// retried on the next one.
	Setup func() error
	// OnChange is called, outside the prober's lock, when the reported egress
	// flips. Informational (logging).
	OnChange func(egress bool)
	// HasGlobalIPv6 reports whether the host has a global unicast IPv6 address
	// (not link-local, not ULA). Default: inspect the host's interfaces.
	HasGlobalIPv6 func() bool
	// Dial opens a TCP connection. Default: net.Dialer with a 3 s timeout.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// Targets are the addresses to try; any single success passes.
	Targets       []string
	Interval      time.Duration
	FailThreshold int
}

// IPv6Prober decides whether an exit node has a usable IPv6 egress.
type IPv6Prober struct {
	cfg IPv6ProberConfig

	mu       sync.Mutex
	egress   bool
	failures int
}

// NewIPv6Prober returns a prober with defaults filled in.
func NewIPv6Prober(cfg IPv6ProberConfig) *IPv6Prober {
	if cfg.IsExit == nil {
		cfg.IsExit = func() bool { return true }
	}
	if cfg.HasGlobalIPv6 == nil {
		cfg.HasGlobalIPv6 = hostHasGlobalIPv6
	}
	if cfg.Dial == nil {
		d := &net.Dialer{Timeout: ipv6DialTimeout}
		cfg.Dial = d.DialContext
	}
	if len(cfg.Targets) == 0 {
		cfg.Targets = DefaultIPv6ProbeTargets
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultIPv6ProbeInterval
	}
	if cfg.FailThreshold <= 0 {
		cfg.FailThreshold = DefaultIPv6FailThreshold
	}
	return &IPv6Prober{cfg: cfg}
}

// Egress reports the current verdict.
func (p *IPv6Prober) Egress() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.egress
}

// Run probes immediately and then every Interval until ctx is cancelled.
func (p *IPv6Prober) Run(ctx context.Context) {
	p.step(ctx)
	t := time.NewTicker(p.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.step(ctx)
		}
	}
}

// step runs one probe and applies the hysteresis: one success reports egress,
// FailThreshold consecutive failures withdraw it.
func (p *IPv6Prober) step(ctx context.Context) {
	if !p.cfg.IsExit() {
		p.set(false, true)
		return
	}
	if p.check(ctx) {
		if p.cfg.Setup != nil && !p.Egress() {
			if err := p.cfg.Setup(); err != nil {
				p.set(false, false)
				return
			}
		}
		p.set(true, true)
		return
	}
	p.set(false, false)
}

// set records a probe result. ok=true is a success (or a forced verdict);
// ok=false is a failed probe that only withdraws egress once it has failed
// FailThreshold times in a row.
func (p *IPv6Prober) set(egress, ok bool) {
	p.mu.Lock()
	prev := p.egress
	switch {
	case ok && egress:
		p.failures = 0
		p.egress = true
	case ok && !egress: // not an exit: no verdict to hysterese over
		p.failures = 0
		p.egress = false
	default: // failed probe
		p.failures++
		if p.failures >= p.cfg.FailThreshold {
			p.egress = false
		}
	}
	now := p.egress
	fn := p.cfg.OnChange
	p.mu.Unlock()

	if now != prev && fn != nil {
		fn(now)
	}
}

// check is one probe: the host has a global IPv6 address AND a TCP connection to
// one of the targets succeeds. The connection also proves there is a route.
func (p *IPv6Prober) check(ctx context.Context) bool {
	if !p.cfg.HasGlobalIPv6() {
		return false
	}
	for _, target := range p.cfg.Targets {
		dctx, cancel := context.WithTimeout(ctx, ipv6DialTimeout)
		c, err := p.cfg.Dial(dctx, "tcp6", target)
		cancel()
		if err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}

// hostHasGlobalIPv6 reports whether any interface carries a global unicast IPv6
// address. Link-local (fe80::/10) and unique-local (fc00::/7) addresses do not
// count: neither is routable on the internet.
func hostHasGlobalIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(ipn.IP)
		if !ok || !addr.Is6() || addr.Is4In6() {
			continue
		}
		if addr.IsGlobalUnicast() && !addr.IsPrivate() {
			return true
		}
	}
	return false
}
