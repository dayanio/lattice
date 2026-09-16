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

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/reconcilers"
	"github.com/alatticeio/lattice/internal/server/vo"
)

// PreviewPolicy computes the deterministic effect of a draft policy without
// persisting anything: for every peer in the workspace it renders the
// traffic rules "before" (current active policies) and "after" (with the
// draft applied), then diffs them. Preview is verification, not promise —
// the same calculator that feeds the netmap runs here.
func (p *policyService) PreviewPolicy(ctx context.Context, wsID string, draft dto.PolicyDto) (*vo.PolicyPreviewVo, error) {
	warnings, err := validatePolicySpecSemantics(ctx, p.store, wsID, &draft.PolicySpec)
	if err != nil {
		return nil, err
	}

	peerRows, err := p.store.Peers().ListByWorkspace(ctx, wsID)
	if err != nil {
		return nil, err
	}
	identities, err := p.store.PeerIdentities().ListByNetwork(ctx, wsID)
	if err != nil {
		return nil, err
	}
	active, err := p.store.Policies().ListActiveByWorkspace(ctx, wsID)
	if err != nil {
		return nil, err
	}

	infraPeers := make([]*infra.Peer, 0, len(peerRows))
	for _, r := range peerRows {
		if r.Address == "" {
			continue
		}
		ip := r.Address
		infraPeers = append(infraPeers, &infra.Peer{Name: r.Name, AppID: r.AppID, Address: &ip, NetworkId: wsID})
	}

	calc := reconcilers.NewPeerRuleCalculator(reconcilers.NewPolicyIdentityResolver(identities))
	draftRow := &models.Policy{
		WorkspaceID: wsID, Name: draft.Name, Action: draft.Action,
		Status: models.PolicyStatusActive, Spec: marshalSpec(draft.PolicySpec),
	}
	// Editing semantics: the draft REPLACES any active policy with the same
	// name, so "before" contains the old rules and "after" the new ones —
	// the diff shows exactly what the edit changes.
	after := make([]*models.Policy, 0, len(active)+1)
	replaced := false
	for _, row := range active {
		if draft.Name != "" && row.Name == draft.Name {
			after = append(after, draftRow)
			replaced = true
			continue
		}
		after = append(after, row)
	}
	if !replaced {
		after = append(after, draftRow)
	}

	out := &vo.PolicyPreviewVo{Warnings: warnings, AffectedPeers: []vo.PolicyPeerPreview{}}
	for _, peer := range infraPeers {
		beforeRules, err := calc.ComputeForPeer(ctx, active, infraPeers, peer)
		if err != nil {
			return nil, err
		}
		afterRules, err := calc.ComputeForPeer(ctx, after, infraPeers, peer)
		if err != nil {
			return nil, err
		}
		added, removed := diffRules(beforeRules, afterRules)
		if len(added) == 0 && len(removed) == 0 {
			continue
		}
		out.AffectedPeers = append(out.AffectedPeers, vo.PolicyPeerPreview{
			Name:    peer.Name,
			Address: deref(peer.Address),
			Added:   added,
			Removed: removed,
		})
	}
	return out, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func marshalSpec(spec dto.PolicySpec) string {
	b, _ := json.Marshal(spec)
	return string(b)
}

// ruleSig is the identity of a rendered traffic decision for diffing.
func ruleSig(direction string, tr infra.TrafficRule) string {
	peers := append([]string{}, tr.Peers...)
	sort.Strings(peers)
	return strings.Join([]string{
		direction, strings.Join(peers, ","),
		fmt.Sprintf("%d", tr.Port), tr.Protocol, tr.Action,
	}, "|")
}

// diffRules computes added/removed traffic decisions between the "before"
// and "after" computed rule sets of one peer.
func diffRules(before, after *infra.FirewallRule) (added, removed []vo.PolicyPreviewRule) {
	beforeSet := map[string]struct{}{}
	for _, tr := range before.Egress {
		for _, peer := range tr.Peers {
			beforeSet[ruleSig("egress", infra.TrafficRule{Peers: []string{peer}, Port: tr.Port, Protocol: tr.Protocol, Action: tr.Action})] = struct{}{}
		}
	}
	for _, tr := range before.Ingress {
		for _, peer := range tr.Peers {
			beforeSet[ruleSig("ingress", infra.TrafficRule{Peers: []string{peer}, Port: tr.Port, Protocol: tr.Protocol, Action: tr.Action})] = struct{}{}
		}
	}

	afterSet := map[string]struct{}{}
	for _, tr := range after.Egress {
		for _, peer := range tr.Peers {
			sig := ruleSig("egress", infra.TrafficRule{Peers: []string{peer}, Port: tr.Port, Protocol: tr.Protocol, Action: tr.Action})
			afterSet[sig] = struct{}{}
			if _, ok := beforeSet[sig]; !ok {
				added = append(added, vo.PolicyPreviewRule{Direction: "egress", Peers: []string{peer}, Port: tr.Port, Protocol: tr.Protocol, Action: tr.Action})
			}
		}
	}
	for _, tr := range after.Ingress {
		for _, peer := range tr.Peers {
			sig := ruleSig("ingress", infra.TrafficRule{Peers: []string{peer}, Port: tr.Port, Protocol: tr.Protocol, Action: tr.Action})
			afterSet[sig] = struct{}{}
			if _, ok := beforeSet[sig]; !ok {
				added = append(added, vo.PolicyPreviewRule{Direction: "ingress", Peers: []string{peer}, Port: tr.Port, Protocol: tr.Protocol, Action: tr.Action})
			}
		}
	}

	for _, tr := range before.Egress {
		for _, peer := range tr.Peers {
			sig := ruleSig("egress", infra.TrafficRule{Peers: []string{peer}, Port: tr.Port, Protocol: tr.Protocol, Action: tr.Action})
			if _, ok := afterSet[sig]; !ok {
				removed = append(removed, vo.PolicyPreviewRule{Direction: "egress", Peers: []string{peer}, Port: tr.Port, Protocol: tr.Protocol, Action: tr.Action})
			}
		}
	}
	for _, tr := range before.Ingress {
		for _, peer := range tr.Peers {
			sig := ruleSig("ingress", infra.TrafficRule{Peers: []string{peer}, Port: tr.Port, Protocol: tr.Protocol, Action: tr.Action})
			if _, ok := afterSet[sig]; !ok {
				removed = append(removed, vo.PolicyPreviewRule{Direction: "ingress", Peers: []string{peer}, Port: tr.Port, Protocol: tr.Protocol, Action: tr.Action})
			}
		}
	}
	return added, removed
}
