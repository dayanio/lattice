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

package standalone_e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/pkg/utils/resp"
	"github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// The full zero-K8s control-plane loop: user → workspace → enrollment
// token → peer registration → netmap with computed rules → policy
// distribution → TTL expiry governance. Every step goes through the
// running latticed's public surfaces (REST + NATS), never the database.
var _ = Describe("Standalone control plane", Ordered, func() {
	var (
		accessToken string
		workspaceID string
		joinToken   string
		peerToken   string
		nc          *nats.Conn
	)

	BeforeAll(func() {
		// Login as the bootstrapped admin: the default config seeds
		// initadmins (admin/123456, platform_admin) on first boot. Do NOT
		// register a second "admin" — t_user.username has no unique
		// constraint, so a duplicate row makes login randomly pick either
		// password's record.
		status, data := apiPOST(serverURL+"/api/v1/users/login", "", map[string]any{
			"username": "admin", "password": "123456",
		})
		Expect(status).To(Equal(200), "login failed: %s", data.Msg)
		accessToken = dataMap(data)["token"]
		Expect(accessToken).NotTo(BeEmpty(), "login must yield an access token")

		var err error
		nc, err = nats.Connect(natsURL, nats.Timeout(5*time.Second))
		Expect(err).NotTo(HaveOccurred(), "connect to embedded NATS failed")
	})

	AfterAll(func() {
		if nc != nil {
			nc.Close()
		}
	})

	It("creates a workspace", func() {
		status, data := apiPOST(serverURL+"/api/v1/workspaces/add", accessToken, map[string]any{
			"namespace":    "e2e-net",
			"displayName":  "Standalone E2E",
			"slug":         fmt.Sprintf("e2e-%d", time.Now().UnixMilli()),
			"maxNodeCount": 10,
		})
		Expect(status).To(Equal(200), "create workspace failed: %s", data.Msg)
		workspaceID = dataMap(data)["id"]
		Expect(workspaceID).NotTo(BeEmpty())
	})

	It("generates an enrollment token bound to the workspace", func() {
		req, err := newRequest("POST", serverURL+"/api/v1/token/generate", nil, accessToken, workspaceID)
		Expect(err).NotTo(HaveOccurred())
		status, data := doAPI(req)
		Expect(status).To(Equal(200), "generate token failed: %s", data.Msg)
		joinToken = dataMap(data)["token"]
		Expect(joinToken).NotTo(BeEmpty())

		// Token creation seeds the workspace's default-deny policy (active).
		status, data = apiGET(serverURL+"/api/v1/policies/list", accessToken, header(workspaceID))
		Expect(status).To(Equal(200))
		Expect(data.Data).NotTo(BeNil())
		listJSON, _ := json.Marshal(data.Data)
		Expect(string(listJSON)).To(ContainSubstring("default-deny"),
			"default-deny policy should have been seeded with the token")
	})

	It("registers a peer over NATS and returns its overlay identity", func() {
		// ADR-0003: the server validates client-submitted public keys, so
		// the fixture must carry a real WireGuard public key (any valid
		// key works — the spec asserts deterministic address allocation).
		deviceKey, err := wgtypes.GeneratePrivateKey()
		Expect(err).NotTo(HaveOccurred())
		payload, _ := json.Marshal(dto.PeerDto{
			Name: "e2e-node", AppID: "e2e-node-1", Token: joinToken,
			Platform: "linux", Hostname: "e2e-host", PublicKey: deviceKey.PublicKey().String(),
		})
		raw, err := nc.Request("lattice.signals.peer.register", payload, 10*time.Second)
		Expect(err).NotTo(HaveOccurred(), "register request failed")

		var node infra.Peer
		Expect(json.Unmarshal(raw.Data, &node)).To(Succeed())
		Expect(node.Name).To(Equal("e2e-node"))
		require(node.Address != nil && *node.Address == "10.96.0.2",
			"first peer should receive 10.96.0.2, got %v", node.Address)
		Expect(node.Token).NotTo(BeEmpty(), "per-peer credential must be issued")
		peerToken = node.Token
	})

	It("serves a netmap with computed rules over NATS", func() {
		msg := getNetMap(nc, "e2e-node-1", peerToken)
		Expect(msg.Current.Name).To(Equal("e2e-node"))
		Expect(msg.Network.Peers).NotTo(BeEmpty())
		Expect(msg.Network.NetworkId).NotTo(BeEmpty())
		Expect(msg.ComputedRules).NotTo(BeNil(), "computed rules (incl. default-deny) must be present")
		Expect(msg.ConfigVersion).NotTo(BeEmpty())
	})

	It("distributes a submitted and approved policy into the netmap", func() {
		status, data := apiPOST(serverURL+"/api/v1/policies/create", accessToken,
			dto.PolicyDto{
				Name:        "allow-db-5432",
				Action:      "Allow",
				PolicyTypes: []string{"Egress"},
				PolicySpec: dto.PolicySpec{
					Network: "e2e-net",
					Egress: []dto.EgressRule{{
						To:    []dto.PeerSelection{{IPBlock: &dto.IPBlock{CIDR: "10.96.0.3/32"}}},
						Ports: []dto.NetworkPolicyPort{{Port: 5432, Protocol: "TCP"}},
					}},
				},
			}, header(workspaceID))
		// Non-admin users get 202 (workflow submitted); platform admins
		// get 200 (applied directly, Data is the policy VO).
		Expect(status == 202 || status == 200).To(BeTrue(), "policy create failed: %s", data.Msg)
		if status == 202 {
			reqID := dataMap(data)["id"]
			if reqID == "" {
				reqID = workflowRequestID(accessToken, workspaceID, "allow-db-5432")
			}
			Expect(reqID).NotTo(BeEmpty())
			req, err := newRequest("POST",
				fmt.Sprintf("%s/api/v1/workspaces/%s/workflow-requests/%s/approve", serverURL, workspaceID, reqID),
				map[string]string{"note": "e2e"}, accessToken, workspaceID)
			Expect(err).NotTo(HaveOccurred())
			status, data = doAPI(req)
			Expect(status).To(Equal(200), "approve failed: %s", data.Msg)
		}

		// The netmap the agent polls must eventually carry the rule.
		Eventually(func() bool {
			msg := getNetMap(nc, "e2e-node-1", peerToken)
			var egressIPs []string
			for _, tr := range msg.ComputedRules.Egress {
				egressIPs = append(egressIPs, tr.Peers...)
			}
			for _, ip := range egressIPs {
				if ip == "10.96.0.3/32" {
					return true
				}
			}
			return false
		}, 15*time.Second, 500*time.Millisecond).Should(BeTrue(),
			"approved policy must reach the agent's computed rules")
	})

	It("expires a TTL policy and removes it from the netmap", func() {
		expiry := time.Now().Add(8 * time.Second).Format(time.RFC3339)
		status, data := apiPOST(serverURL+"/api/v1/policies/create", accessToken,
			dto.PolicyDto{
				Name:        "temp-allow",
				Action:      "Allow",
				PolicyTypes: []string{"Egress"},
				PolicySpec: dto.PolicySpec{
					Network:   "e2e-net",
					ExpiresAt: ptrTime(expiry),
					Egress: []dto.EgressRule{{
						To:    []dto.PeerSelection{{IPBlock: &dto.IPBlock{CIDR: "10.99.0.0/16"}}},
						Ports: []dto.NetworkPolicyPort{{Port: 80, Protocol: "TCP"}},
					}},
				},
			}, header(workspaceID))
		Expect(status == 200 || status == 202).To(BeTrue(), "policy create failed: %s", data.Msg)
		if reqID := dataMap(data)["id"]; reqID != "" && status == 202 {
			req, err := newRequest("POST",
				fmt.Sprintf("%s/api/v1/workspaces/%s/workflow-requests/%s/approve", serverURL, workspaceID, reqID),
				map[string]string{"note": "e2e"}, accessToken, workspaceID)
			Expect(err).NotTo(HaveOccurred())
			status, data = doAPI(req)
			Expect(status).To(Equal(200), "approve failed: %s", data.Msg)
		}

		// First the rule appears...
		Eventually(func() bool {
			return netmapHasCIDR(nc, "e2e-node-1", peerToken, "10.99.0.0/16")
		}, 10*time.Second, 500*time.Millisecond).Should(BeTrue())

		// ...then the TTL reconciler flips it to expired and the netmap
		// stops distributing it (resync accelerated to 1s via env).
		Eventually(func() bool {
			return !netmapHasCIDR(nc, "e2e-node-1", peerToken, "10.99.0.0/16")
		}, 30*time.Second, time.Second).Should(BeTrue(),
			"expired policy must be dropped from the netmap")
	})
})

// ---- helpers ----

func header(wsID string) map[string]string { return map[string]string{"X-Workspace-Id": wsID} }

func ptrTime(s string) *time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return &t
}

// newRequest builds an API request with auth and the workspace header.
func newRequest(method, url string, body any, accessToken, wsID string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("X-Workspace-Id", wsID)
	return req, nil
}

func dataMap(data *resp.Response) map[string]string {
	m, _ := data.Data.(map[string]any)
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func require(cond bool, msgFormat string, args ...any) {
	if !cond {
		Fail(fmt.Sprintf(msgFormat, args...))
	}
}

// getNetMap requests the peer's netmap over NATS, failing the spec on error.
func getNetMap(nc *nats.Conn, appID, token string) *infra.Message {
	payload, _ := json.Marshal(dto.PeerDto{AppID: appID, Token: token})
	raw, err := nc.Request("lattice.signals.peer.GetNetMap", payload, 10*time.Second)
	Expect(err).NotTo(HaveOccurred(), "GetNetMap request failed")
	msg := &infra.Message{}
	Expect(json.Unmarshal(raw.Data, msg)).To(Succeed())
	return msg
}

func netmapHasCIDR(nc *nats.Conn, appID, token, cidr string) bool {
	msg := getNetMap(nc, appID, token)
	for _, tr := range msg.ComputedRules.Egress {
		for _, ip := range tr.Peers {
			if ip == cidr {
				return true
			}
		}
	}
	return false
}

// workflowRequestID finds the pending workflow request for a policy.
func workflowRequestID(accessToken, wsID, policyName string) string {
	url := fmt.Sprintf("%s/api/v1/workspaces/%s/workflow-requests", serverURL, wsID)
	status, data := apiGET(url, accessToken, header(wsID))
	Expect(status).To(Equal(200), "list workflow requests failed: %s", data.Msg)
	// Data may be a bare array or a paginated {list: [...], total} object.
	var reqs []struct {
		ID           string `json:"id"`
		Status       string `json:"status"`
		Action       string `json:"action"`
		ResourceType string `json:"resourceType"`
		ResourceName string `json:"resourceName"`
	}
	blob, _ := json.Marshal(data.Data)
	var asArray []map[string]any
	var asObject struct {
		List []map[string]any `json:"list"`
	}
	switch {
	case json.Unmarshal(blob, &asArray) == nil && asArray != nil:
	case json.Unmarshal(blob, &asObject) == nil && asObject.List != nil:
		blob, _ = json.Marshal(asObject.List)
		_ = json.Unmarshal(blob, &reqs)
	}
	_ = json.Unmarshal(blob, &reqs)
	for _, r := range reqs {
		if r.ResourceType == "policy" && r.ResourceName == policyName && r.Status == "pending" {
			return r.ID
		}
	}
	Fail(fmt.Sprintf("pending workflow request for policy %q not found in %+v", policyName, reqs))
	return ""
}
