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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alatticeio/lattice/internal/agent/controller"
	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/models"
)

// PolicyIdentityResolver resolves identityRef names to overlay IPs from a
// snapshot of PeerIdentity records — the DB-path equivalent of the K8s
// identity_resolver. While a grace period is active, both the resolved and
// the previous IP are returned (zero-downtime device replacement).
type PolicyIdentityResolver struct {
	byName map[string]models.PeerIdentity
}

// NewPolicyIdentityResolver indexes the snapshot by identity name.
func NewPolicyIdentityResolver(identities []*models.PeerIdentity) *PolicyIdentityResolver {
	byName := make(map[string]models.PeerIdentity, len(identities))
	for _, id := range identities {
		if id != nil {
			byName[id.Name] = *id
		}
	}
	return &PolicyIdentityResolver{byName: byName}
}

// ResolveIPs returns the live IPs for an identity; an unknown identity or
// one without a resolved IP yields an empty list (fail-closed).
func (r *PolicyIdentityResolver) ResolveIPs(name string, now time.Time) []string {
	id, ok := r.byName[name]
	if !ok {
		return nil
	}
	var ips []string
	if id.ResolvedPeerIP != "" {
		ips = append(ips, id.ResolvedPeerIP)
	}
	if id.PreviousPeerIP != "" && id.GracePeriodExpiresAt != nil && now.Before(*id.GracePeriodExpiresAt) {
		ips = append(ips, id.PreviousPeerIP)
	}
	return ips
}

// PeerRuleCalculator computes per-peer firewall rules from t_policy rows.
// It is the standalone replacement for the K8s detector/generator chain:
// it parses the persisted PolicySpec JSON, resolves identity selections
// server-side, and reuses the agent package's PolicyEvaluator (ALLOW
// priority, default-deny tail) so both paths converge identically.
//
// Selection semantics on the DB path:
//   - IPBlock      → CIDR passthrough
//   - IdentityRef  → resolved PeerIdentity IPs as /32 CIDRs (grace-aware)
//   - PeerSelector → matches nothing today (standalone peers carry no
//     labels yet); rules select zero peers, i.e. fail closed
type PeerRuleCalculator struct {
	identityResolver *PolicyIdentityResolver
	evaluator        controller.PolicyEvaluator
}

// NewPeerRuleCalculator returns a calculator over the given identity
// snapshot.
func NewPeerRuleCalculator(resolver *PolicyIdentityResolver) *PeerRuleCalculator {
	return &PeerRuleCalculator{
		identityResolver: resolver,
		evaluator:        controller.NewPolicyEvaluatorWithoutIdentity(),
	}
}

// ComputeForPeer evaluates every policy for target and returns its
// converged firewall rule (ingress/egress + default-deny tails).
func (c *PeerRuleCalculator) ComputeForPeer(
	ctx context.Context,
	policies []*models.Policy,
	peers []*infra.Peer,
	target *infra.Peer,
) (*infra.FirewallRule, error) {
	infraPolicies, err := c.BuildWirePolicies(policies)
	if err != nil {
		return nil, err
	}

	network := &infra.Network{Peers: peers}
	return c.evaluator.Evaluate(ctx, target, network, infraPolicies)
}

// BuildWirePolicies converts persisted policy rows into their wire form,
// expanding identity selections into resolved IPs. It is exported because
// the netmap builder needs the same conversion for msg.Policies.
func (c *PeerRuleCalculator) BuildWirePolicies(policies []*models.Policy) ([]*infra.Policy, error) {
	out := make([]*infra.Policy, 0, len(policies))
	now := time.Now()
	for _, p := range policies {
		var spec dto.PolicySpec
		if err := json.Unmarshal([]byte(p.Spec), &spec); err != nil {
			return nil, fmt.Errorf("parse spec of policy %q: %w", p.Name, err)
		}
		out = append(out, c.buildPolicy(p.Name, p.Action, &spec, now))
	}
	return out, nil
}

// buildPolicy converts one persisted spec into the wire format, expanding
// identity selections into resolved /32 CIDRs.
func (c *PeerRuleCalculator) buildPolicy(name, action string, spec *dto.PolicySpec, now time.Time) *infra.Policy {
	pol := &infra.Policy{
		PolicyName: name,
		Action:     strings.ToUpper(action),
	}
	for _, r := range spec.Ingress {
		pol.Ingress = append(pol.Ingress, c.buildRules(pol.Action, r.From, r.Ports, now)...)
	}
	for _, r := range spec.Egress {
		pol.Egress = append(pol.Egress, c.buildRules(pol.Action, r.To, r.Ports, now)...)
	}
	return pol
}

// buildRules expands a policy direction into wire rules — one per declared
// port, or a single all-ports rule when no ports are declared (K8s
// NetworkPolicy semantics: omitted ports = all ports).
func (c *PeerRuleCalculator) buildRules(action string, selections []dto.PeerSelection, ports []dto.NetworkPolicyPort, now time.Time) []*infra.Rule {
	if len(ports) == 0 {
		return []*infra.Rule{c.buildRule(action, selections, dto.NetworkPolicyPort{}, now)}
	}
	rules := make([]*infra.Rule, 0, len(ports))
	for _, port := range ports {
		rules = append(rules, c.buildRule(action, selections, port, now))
	}
	return rules
}

func (c *PeerRuleCalculator) buildRule(action string, selections []dto.PeerSelection, port dto.NetworkPolicyPort, now time.Time) *infra.Rule {
	rule := &infra.Rule{
		Action:   action,
		Protocol: strings.ToUpper(port.Protocol),
		Port:     int(port.Port),
	}
	for _, sel := range selections {
		if sel.IPBlock != nil && sel.IPBlock.CIDR != "" {
			rule.CIDRs = append(rule.CIDRs, sel.IPBlock.CIDR)
		}
		if sel.IdentityRef != "" {
			// Bare host IPs, matching the K8s path's peer-IP convention.
			rule.CIDRs = append(rule.CIDRs, c.identityResolver.ResolveIPs(sel.IdentityRef, now)...)
		}
		// PeerSelector deliberately selects nothing on the DB path: peers
		// carry no labels yet, so the rule stays empty (fail closed).
	}
	return rule
}
