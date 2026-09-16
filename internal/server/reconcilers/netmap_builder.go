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

	current := dbToInfraPeer(peer)
	current.PrivateKey = peer.PrivateKey // the owner gets its own key back
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
		if row.Address == "" {
			continue // still enrolling; not part of the mesh yet
		}
		p := dbToInfraPeer(row)
		if _, ok := selected[row.ID]; ok {
			if extra := parseAdvertisedRoutes(row.AdvertisedRoutes); len(extra) > 0 {
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

// dbToInfraPeer converts the registry record into its wire form.
func dbToInfraPeer(p *models.Peer) *infra.Peer {
	ip := new(string)
	*ip = p.Address
	peer := &infra.Peer{
		Name:      p.Name,
		AppID:     p.AppID,
		Address:   ip,
		Endpoint:  p.Endpoint,
		Hostname:  p.Hostname,
		Platform:  p.Platform,
		NetworkId: p.WorkspaceID,
		PublicKey: p.PublicKey,
	}
	if p.Labels != "" {
		_ = json.Unmarshal([]byte(p.Labels), &peer.Labels)
	}
	if net.ParseIP(p.Address) != nil {
		peer.AllowedIPs = p.Address + "/32"
	}
	return peer
}
