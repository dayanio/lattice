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

package reconcilers_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/reconcilers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// specJSON marshals a dto.PolicySpec the same way the policy service
// persists it into t_policy.spec.
func specJSON(t *testing.T, spec dto.PolicySpec) string {
	t.Helper()
	b, err := json.Marshal(spec)
	require.NoError(t, err)
	return string(b)
}

func addr(s string) *string { return &s }

// peerWithIdentity: an identityRef selecting a PeerIdentity whose resolved
// IP (and grace IP, while active) must appear in the computed rule.
func TestPeerRuleCalculator_IdentityRefResolvesToIPs(t *testing.T) {
	ctx := context.Background()
	grace := time.Now().Add(time.Hour)

	policies := []*models.Policy{{
		WorkspaceID: "ws1", Name: "api-to-db", Action: "ALLOW", Status: models.PolicyStatusActive,
		Spec: specJSON(t, dto.PolicySpec{
			Network: "net-1",
			Egress: []dto.EgressRule{{
				To:    []dto.PeerSelection{{IdentityRef: "prod-db"}},
				Ports: []dto.NetworkPolicyPort{{Port: 5432, Protocol: "TCP"}},
			}},
		}),
	}}
	peers := []*infra.Peer{
		{Name: "api", Address: addr("10.0.0.10")},
		{Name: "db", Address: addr("10.0.0.5")},
	}
	identities := []*models.PeerIdentity{{
		NetworkID: "net-1", Name: "prod-db", PeerRef: "db",
		ResolvedPeerIP: "10.0.0.5", PreviousPeerIP: "10.0.0.4",
		GracePeriodExpiresAt: &grace,
	}}

	calc := reconcilers.NewPeerRuleCalculator(reconcilers.NewPolicyIdentityResolver(identities))
	rule, err := calc.ComputeForPeer(ctx, policies, peers, peers[1])
	require.NoError(t, err)
	require.NotNil(t, rule)

	var egressIPs []string
	for _, tr := range rule.Egress {
		egressIPs = append(egressIPs, tr.Peers...)
	}
	assert.ElementsMatch(t, []string{"10.0.0.5", "10.0.0.4"}, egressIPs,
		"identityRef must resolve to the current IP plus the grace-period IP")
}

// ipblock passthrough: CIDR selections survive into the computed rule.
func TestPeerRuleCalculator_IPBlockPassthrough(t *testing.T) {
	ctx := context.Background()
	policies := []*models.Policy{{
		WorkspaceID: "ws1", Name: "allow-cidr", Action: "ALLOW", Status: models.PolicyStatusActive,
		Spec: specJSON(t, dto.PolicySpec{
			Ingress: []dto.IngressRule{{
				From:  []dto.PeerSelection{{IPBlock: &dto.IPBlock{CIDR: "10.0.0.0/24"}}},
				Ports: []dto.NetworkPolicyPort{{Port: 8080, Protocol: "TCP"}},
			}},
		}),
	}}
	peers := []*infra.Peer{{Name: "web", Address: addr("10.0.0.20")}}
	var identities []*models.PeerIdentity

	calc := reconcilers.NewPeerRuleCalculator(reconcilers.NewPolicyIdentityResolver(identities))
	rule, err := calc.ComputeForPeer(ctx, policies, peers, peers[0])
	require.NoError(t, err)
	require.NotNil(t, rule)

	var ingressIPs []string
	for _, tr := range rule.Ingress {
		ingressIPs = append(ingressIPs, tr.Peers...)
	}
	assert.Contains(t, ingressIPs, "10.0.0.0/24", "CIDR selection must pass through")
}

// The evaluator's contract holds through the calculator: computed rules
// carry a default-deny tail (empty peers) so unselected traffic is dropped.
func TestPeerRuleCalculator_DefaultDenyTail(t *testing.T) {
	ctx := context.Background()
	policies := []*models.Policy{{
		WorkspaceID: "ws1", Name: "api-to-db", Action: "ALLOW", Status: models.PolicyStatusActive,
		Spec: specJSON(t, dto.PolicySpec{
			Egress: []dto.EgressRule{{
				To:    []dto.PeerSelection{{IdentityRef: "prod-db"}},
				Ports: []dto.NetworkPolicyPort{{Port: 5432, Protocol: "TCP"}},
			}},
		}),
	}}
	peers := []*infra.Peer{
		{Name: "api", Address: addr("10.0.0.10")},
		{Name: "db", Address: addr("10.0.0.5")},
	}
	identities := []*models.PeerIdentity{{NetworkID: "net-1", Name: "prod-db", PeerRef: "db", ResolvedPeerIP: "10.0.0.5"}}

	calc := reconcilers.NewPeerRuleCalculator(reconcilers.NewPolicyIdentityResolver(identities))
	rule, err := calc.ComputeForPeer(ctx, policies, peers, peers[0])
	require.NoError(t, err)

	hasAllow := false
	hasDenyTail := false
	for _, tr := range rule.Egress {
		if len(tr.Peers) > 0 {
			hasAllow = true
		} else {
			hasDenyTail = true
		}
	}
	assert.True(t, hasAllow, "an ALLOW entry for the selected identity must exist")
	assert.True(t, hasDenyTail, "a default-deny tail must exist for unselected traffic")
}

// An identityRef pointing at an unknown identity resolves to nothing: the
// selection matches zero peers (fail-closed), matching the K8s-path
// "identityRef missing → empty list" compatibility rule.
func TestPeerRuleCalculator_UnknownIdentityFailsClosed(t *testing.T) {
	ctx := context.Background()
	policies := []*models.Policy{{
		WorkspaceID: "ws1", Name: "api-to-ghost", Action: "ALLOW", Status: models.PolicyStatusActive,
		Spec: specJSON(t, dto.PolicySpec{
			Egress: []dto.EgressRule{{
				To:    []dto.PeerSelection{{IdentityRef: "ghost"}},
				Ports: []dto.NetworkPolicyPort{{Port: 5432, Protocol: "TCP"}},
			}},
		}),
	}}
	peers := []*infra.Peer{{Name: "api", Address: addr("10.0.0.10")}}

	calc := reconcilers.NewPeerRuleCalculator(reconcilers.NewPolicyIdentityResolver(nil))
	rule, err := calc.ComputeForPeer(ctx, policies, peers, peers[0])
	require.NoError(t, err)

	for _, tr := range rule.Egress {
		for _, ip := range tr.Peers {
			assert.NotEqual(t, "0.0.0.0/0", ip, "unknown identity must not widen access")
		}
	}
}

// Expired grace periods contribute only the resolved IP, not the stale one.
func TestPeerRuleCalculator_ExpiredGraceExcluded(t *testing.T) {
	ctx := context.Background()
	stale := time.Now().Add(-time.Minute)

	policies := []*models.Policy{{
		WorkspaceID: "ws1", Name: "api-to-db", Action: "ALLOW", Status: models.PolicyStatusActive,
		Spec: specJSON(t, dto.PolicySpec{
			Egress: []dto.EgressRule{{
				To:    []dto.PeerSelection{{IdentityRef: "prod-db"}},
				Ports: []dto.NetworkPolicyPort{{Port: 5432, Protocol: "TCP"}},
			}},
		}),
	}}
	peers := []*infra.Peer{{Name: "api", Address: addr("10.0.0.10")}}
	identities := []*models.PeerIdentity{{
		NetworkID: "net-1", Name: "prod-db", PeerRef: "db",
		ResolvedPeerIP: "10.0.0.5", PreviousPeerIP: "10.0.0.4",
		GracePeriodExpiresAt: &stale,
	}}

	calc := reconcilers.NewPeerRuleCalculator(reconcilers.NewPolicyIdentityResolver(identities))
	rule, err := calc.ComputeForPeer(ctx, policies, peers, peers[0])
	require.NoError(t, err)

	var egressIPs []string
	for _, tr := range rule.Egress {
		egressIPs = append(egressIPs, tr.Peers...)
	}
	assert.NotContains(t, egressIPs, "10.0.0.4", "expired grace IP must be excluded")
	assert.Contains(t, egressIPs, "10.0.0.5")
}
