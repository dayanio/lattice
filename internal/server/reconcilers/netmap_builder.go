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

package reconcilers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/overlay6"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/go-logr/logr"
)

// overlayCIDRBase is the /24 block standalone workspaces allocate peer
// addresses from (CONTEXT.md's overlay IP convention).
const overlayCIDRBase = "10.96.0."

// AllocateAddress returns the lowest free overlay address in the workspace
// block, skipping every address in taken.
func AllocateAddress(taken []string) (string, error) {
	used := make(map[string]struct{}, len(taken))
	for _, a := range taken {
		used[a] = struct{}{}
	}
	for i := 2; i < 255; i++ {
		candidate := overlayCIDRBase + strconv.Itoa(i)
		if _, ok := used[candidate]; !ok {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("address space %s0/24 exhausted", overlayCIDRBase)
}

// NetmapBuilder assembles per-peer netmap messages from the standalone
// database — the DB-path replacement for the K8s detector/generator chain
// (internal/agent/controller/detector.go). Message semantics mirror the
// K8s output exactly: sorted network peers, TTL-filtered policies,
// ComputedPeers for connection targets, and ComputedRules computed by the
// shared PolicyEvaluator (via PeerRuleCalculator).
type NetmapBuilder struct {
	peers           store.PeerRepository
	policies        store.PolicyRepository
	identities      store.PeerIdentityRepository
	routeSelections store.PeerRouteSelectionRepository
	logger          logr.Logger
	// relayURL, when set, is stamped into every peer's RelayURL so agents
	// fall back to the control-plane relay when ICE cannot traverse.
	relayURL string
	// selfRelayURL is relayURL plus the relay's auth token, given only to the
	// netmap's Current peer. Sandbox-style clients (the iOS engine) enable the
	// relay only when their own record carries a relay URL, and they take that
	// record from here rather than from the registration response. The token
	// must not appear on any other peer's entry.
	selfRelayURL string
	// providerLive, when set, reports whether a route provider (by agent app
	// id) is currently online. A selected provider that is not live has its
	// advertised routes withheld from consumers: nobody would forward for
	// them, and a dead default route blackholes the consumer's whole network.
	providerLive func(appID string) bool
	// ipv6Enabled turns the netmap dual-stack: every peer's AllowedIPs gains its
	// derived overlay IPv6 /128. Off (the default) keeps the netmap byte-identical
	// to a build without IPv6 support, which is what old clients expect.
	ipv6Enabled bool
	// ipv6Egress reports whether a route provider (by agent app id) has told the
	// server it has a working IPv6 egress. Only then is "::/0" added next to the
	// provider's "0.0.0.0/0" for the consumers that selected it.
	ipv6Egress func(appID string) bool
}

// NewNetmapBuilder returns a builder over the standalone stores.
func NewNetmapBuilder(peers store.PeerRepository, policies store.PolicyRepository, identities store.PeerIdentityRepository, routeSelections store.PeerRouteSelectionRepository) *NetmapBuilder {
	return &NetmapBuilder{
		peers:           peers,
		policies:        policies,
		identities:      identities,
		routeSelections: routeSelections,
		logger:          logr.Discard(),
	}
}

// SetRelayURL stamps relayURL into every netmap peer's RelayURL.
func (b *NetmapBuilder) SetRelayURL(url string) { b.relayURL = url }

// SetSelfRelayURL sets the relay address (with its auth token) placed on the
// netmap's Current peer only, never on the entries describing other peers.
func (b *NetmapBuilder) SetSelfRelayURL(url string) { b.selfRelayURL = url }

// SetProviderLiveness installs the online check that gates route expansion.
// Unset (nil), every selected provider's routes are expanded as before.
func (b *NetmapBuilder) SetProviderLiveness(live func(appID string) bool) { b.providerLive = live }

// SetIPv6 turns the dual-stack netmap on or off. egress reports a provider's
// reported IPv6 egress capability; nil means no provider ever has one, so
// consumers are never handed "::/0".
func (b *NetmapBuilder) SetIPv6(enabled bool, egress func(appID string) bool) {
	b.ipv6Enabled = enabled
	b.ipv6Egress = egress
}

// toInfraPeer is dbToInfraPeer plus the dual-stack host route when enabled.
func (b *NetmapBuilder) toInfraPeer(p *models.Peer) *infra.Peer {
	peer := dbToInfraPeer(p)
	if b.ipv6Enabled && peer.AllowedIPs != "" {
		peer.AllowedIPs = overlay6.HostAllowedIPs(p.Address, true)
	}
	return peer
}

// providerRoutes returns the routes a selected provider adds to a consumer's
// AllowedIPs: the provider's advertised routes, plus "::/0" when it advertised
// the IPv4 default route and has reported a working IPv6 egress.
func (b *NetmapBuilder) providerRoutes(row *models.Peer) []string {
	extra := parseAdvertisedRoutes(row.AdvertisedRoutes)
	if len(extra) == 0 || !b.ipv6Enabled || b.ipv6Egress == nil || !b.ipv6Egress(row.AppID) {
		return extra
	}
	for _, r := range extra {
		if r == "0.0.0.0/0" {
			return append(extra, "::/0")
		}
	}
	return extra
}

// BuildForAppID resolves the peer by its agent instance id, verifies the
// registration token, and builds the peer's netmap message.
func (b *NetmapBuilder) BuildForAppID(ctx context.Context, appID, token string) (*infra.Message, error) {
	peer, err := b.peers.GetByAppID(ctx, appID)
	if err != nil {
		return nil, err
	}
	if peer.Token == "" || token == "" || peer.Token != token {
		return nil, errors.New("netmap: token mismatch for peer " + peer.Name)
	}
	if peer.Disabled {
		return nil, errors.New("netmap: peer " + peer.Name + " is disabled")
	}
	if peer.ApprovalStatus != "" && peer.ApprovalStatus != models.ApprovalApproved {
		// Not an error: the agent polls this endpoint and must keep its
		// heartbeat/poll loop alive while awaiting approval (ADR-0003).
		return b.pendingMessage(peer), nil
	}
	return b.BuildForPeer(ctx, peer)
}

// BuildForPeer builds the netmap message describing the whole workspace
// from target's point of view.
func (b *NetmapBuilder) BuildForPeer(ctx context.Context, peer *models.Peer) (*infra.Message, error) {
	rows, err := b.peers.ListByWorkspace(ctx, peer.WorkspaceID)
	if err != nil {
		return nil, err
	}
	policies, err := b.policies.ListActiveByWorkspace(ctx, peer.WorkspaceID)
	if err != nil {
		return nil, err
	}
	identities, err := b.identities.ListByNetwork(ctx, peer.WorkspaceID)
	if err != nil {
		return nil, err
	}

	current := b.toInfraPeer(peer)
	current.PrivateKey = peer.PrivateKey // the owner gets its own key back
	if b.selfRelayURL != "" {
		current.RelayURL = b.selfRelayURL
	}
	network := &infra.Network{
		NetworkId:   peer.WorkspaceID,
		NetworkName: peer.WorkspaceID,
		Peers:       make([]*infra.Peer, 0, len(rows)),
	}
	selectedProviderIDs, err := b.routeSelections.ListProviderIDsForConsumer(ctx, peer.WorkspaceID, peer.ID)
	if err != nil {
		return nil, fmt.Errorf("list route selections: %w", err)
	}
	selected := make(map[string]struct{}, len(selectedProviderIDs))
	for _, id := range selectedProviderIDs {
		selected[id] = struct{}{}
	}

	computedPeers := make([]*infra.Peer, 0, len(rows))
	for _, row := range rows {
		if row.ApprovalStatus != "" && row.ApprovalStatus != models.ApprovalApproved {
			continue // pending/revoked peers are invisible to the mesh
		}
		if row.Address == "" {
			continue // still enrolling; not part of the mesh yet
		}
		p := b.toInfraPeer(row)
		if b.relayURL != "" {
			p.RelayURL = b.relayURL
		}
		if _, ok := selected[row.ID]; ok && (b.providerLive == nil || b.providerLive(row.AppID)) {
			if extra := b.providerRoutes(row); len(extra) > 0 {
				p.AllowedIPs = strings.Join(append([]string{p.AllowedIPs}, extra...), ",")
			}
		}
		network.Peers = append(network.Peers, p)
		if row.ID != peer.ID {
			computedPeers = append(computedPeers, p)
		}
	}
	sort.Slice(network.Peers, func(i, j int) bool {
		return network.Peers[i].Name < network.Peers[j].Name
	})

	calculator := NewPeerRuleCalculator(NewPolicyIdentityResolver(identities))
	wirePolicies, err := calculator.BuildWirePolicies(policies)
	if err != nil {
		return nil, err
	}
	computedRules, err := calculator.ComputeForPeer(ctx, policies, network.Peers, current)
	if err != nil {
		return nil, err
	}

	msg := &infra.Message{
		EventType:     infra.EventTypeNodeUpdate,
		ConfigVersion: versionFor(current, network, wirePolicies, computedRules),
		Timestamp:     time.Now().Unix(),
		Current:       current,
		Network:       network,
		Policies:      wirePolicies,
		ComputedPeers: computedPeers,
		ComputedRules: computedRules,
	}
	return msg, nil
}

// versionFor derives a content-stable config version: polling agents use
// it to skip applying unchanged state.
func versionFor(current *infra.Peer, network *infra.Network, policies []*infra.Policy, rules *infra.FirewallRule) string {
	blob, _ := json.Marshal(struct {
		Current *infra.Peer
		Network *infra.Network
		Pols    []*infra.Policy
		Rules   *infra.FirewallRule
	}{current, network, policies, rules})
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:8])
}

// HasAdvertisedRoutes reports whether the AdvertisedRoutes column declares at
// least one valid route, i.e. whether the peer can act as a route provider.
func HasAdvertisedRoutes(raw string) bool { return len(parseAdvertisedRoutes(raw)) > 0 }

// parseAdvertisedRoutes decodes the AdvertisedRoutes JSON-array column.
// Malformed or empty input yields no routes rather than an error — a peer
// that never declared anything (or has a stale/corrupt value) should just
// offer nothing, not break netmap building for everyone who selected it.
func parseAdvertisedRoutes(raw string) []string {
	if raw == "" {
		return nil
	}
	var routes []string
	if err := json.Unmarshal([]byte(raw), &routes); err != nil {
		return nil
	}
	valid := routes[:0]
	for _, r := range routes {
		if _, _, err := net.ParseCIDR(r); err == nil {
			valid = append(valid, r)
		}
	}
	return valid
}

// pendingMessage returns the minimal netmap shown to a peer that has not
// been approved yet: its own identity, no address, no mesh peers.
func (b *NetmapBuilder) pendingMessage(peer *models.Peer) *infra.Message {
	addr := ""
	current := dbToInfraPeer(peer)
	current.Address = &addr
	current.PrivateKey = peer.PrivateKey // legacy agents need their key back
	return &infra.Message{Current: current}
}

// dbToInfraPeer converts the registry record into its wire form.
func dbToInfraPeer(p *models.Peer) *infra.Peer {
	ip := new(string)
	*ip = p.Address
	peer := &infra.Peer{
		Name:             p.Name,
		AppID:            p.AppID,
		Address:          ip,
		Endpoint:         p.Endpoint,
		Hostname:         p.Hostname,
		Platform:         p.Platform,
		NetworkId:        p.WorkspaceID,
		PublicKey:        p.PublicKey,
		ApprovalStatus:   p.ApprovalStatus,
		AdvertisedRoutes: parseAdvertisedRoutes(p.AdvertisedRoutes),
	}
	if p.Labels != "" {
		_ = json.Unmarshal([]byte(p.Labels), &peer.Labels)
	}
	if net.ParseIP(p.Address) != nil {
		peer.AllowedIPs = p.Address + "/32"
	}
	return peer
}
