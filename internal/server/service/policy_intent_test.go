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

package service_test

import (
	"context"
	"testing"

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/db/gormstore"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/llm"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/service"
	"github.com/alatticeio/lattice/internal/server/vo"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// fakeLLM returns a canned completion; used to test the translation gates
// without a real provider.
type fakeLLM struct {
	content string
	err     error
}

func (f *fakeLLM) Complete(_ context.Context, _ *llm.Request) (*llm.Response, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &llm.Response{Content: f.content}, nil
}

func newIntentService(t *testing.T, completion string) (service.PolicyIntentService, store.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Peer{}, &models.EnrollmentToken{}, &models.Policy{},
		&models.Workspace{}, &models.UserProfile{}, &models.PeerIdentity{}, &models.PolicyVersion{},
	))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	return service.NewPolicyIntentService(&fakeLLM{content: completion}, st), st
}

const translateCompletion = `{"spec": {
    "network": "ws1",
    "egress": [{"to": [{"identityRef": "prod-db"}], "ports": [{"port": 5432, "protocol": "TCP"}]}]
  }, "summary": "只放行到 prod-db 的 5432 端口", "warnings": []}`

func TestPolicyIntent_Translate(t *testing.T) {
	svc, st := newIntentService(t, translateCompletion)
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Dev",
	}))
	require.NoError(t, st.PeerIdentities().Create(ctx, &models.PeerIdentity{
		NetworkID: "ws1", Name: "prod-db", PeerRef: "db", ResolvedPeerIP: "10.96.0.3",
	}))

	vo, err := svc.Translate(ctx, "ws1", "只允许访问数据库的 5432 端口")
	require.NoError(t, err)
	assert.Equal(t, "ws1", vo.Spec.Network, "network is forced to the session workspace")
	assert.Contains(t, vo.Summary, "5432")

	// Semantic gates ran: identityRef resolved (no warning for it).
	for _, w := range vo.Warnings {
		assert.NotContains(t, w, "prod-db 不存在")
	}
}

func TestPolicyIntent_UnknownIdentityWarns(t *testing.T) {
	svc, st := newIntentService(t, `{"spec": {"egress": [{"to": [{"identityRef": "ghost"}]}]}, "summary": "s", "warnings": []}`)
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1",
	}))

	vo, err := svc.Translate(ctx, "ws1", "whatever")
	require.NoError(t, err)
	require.NotEmpty(t, vo.Warnings, "unknown identityRef must produce a warning")
	assert.Contains(t, vo.Warnings[0], "ghost")
}

func TestPolicyIntent_InvalidCIDRRejected(t *testing.T) {
	svc, st := newIntentService(t, `{"spec": {"egress": [{"to": [{"ipBlock": {"cidr": "not-a-cidr"}}]}]}, "summary": "s"}`)
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1",
	}))

	_, err := svc.Translate(ctx, "ws1", "whatever")
	require.Error(t, err, "invalid CIDR must be a hard failure")
}

// Injection attempt: the LLM tries to escape the schema or declare another
// workspace — the workspace is forced by the service, unknown fields are
// dropped by strict decoding of the known contract.
func TestPolicyIntent_TranslationCannotEscapeWorkspace(t *testing.T) {
	svc, st := newIntentService(t, `{"spec": {"network": "other-workspace", "egress": [{"to": [{"ipBlock": {"cidr": "0.0.0.0/0"}}]}]}, "summary": "s"}`)
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1",
	}))

	vo, err := svc.Translate(ctx, "ws1", "whatever")
	require.NoError(t, err)
	assert.Equal(t, "ws1", vo.Spec.Network, "LLM-declared network must be overridden by the session workspace")
}

func TestPolicyIntent_NotConfigured(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	st, err := gormstore.New(db)
	require.NoError(t, err)
	svc := service.NewPolicyIntentService(nil, st)

	_, err = svc.Translate(context.Background(), "ws1", "whatever")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}

func TestPolicyIntent_FencedJSONParsed(t *testing.T) {
	svc, st := newIntentService(t, "```json\n{\"spec\": {}, \"summary\": \"ok\"}\n```")
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1",
	}))

	vo, err := svc.Translate(ctx, "ws1", "whatever")
	require.NoError(t, err)
	assert.Equal(t, "ok", vo.Summary)
}

// ---- preview ----

// newPreviewStore returns a store migrated with every table the policy
// preview path touches.
func newPreviewStore(t *testing.T) store.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&models.Peer{}, &models.EnrollmentToken{}, &models.Policy{},
		&models.Workspace{}, &models.UserProfile{}, &models.PeerIdentity{}, &models.PolicyVersion{},
	))
	st, err := gormstore.New(db)
	require.NoError(t, err)
	return st
}

func TestPolicyService_PreviewPolicy(t *testing.T) {
	st := newPreviewStore(t)
	ctx := context.Background()
	seedEnrollmentToken(t, st, nil)
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Dev",
	}))

	peerSvc := service.NewPeerService(nil, st, nil, &fakeVerifier{valid: false})
	policySvc := service.NewPolicyService(nil, st)
	for _, p := range []string{"a", "b"} {
		_, err := peerSvc.Register(ctx, &dto.PeerDto{Name: p, AppID: "app-" + p, Token: "enr-test-token"})
		require.NoError(t, err)
	}

	preview, err := policySvc.PreviewPolicy(ctx, "ws1", dto.PolicyDto{
		Name:   "allow-db",
		Action: "Allow",
		PolicySpec: dto.PolicySpec{
			Egress: []dto.EgressRule{{
				To:    []dto.PeerSelection{{IPBlock: &dto.IPBlock{CIDR: "10.96.0.3/32"}}},
				Ports: []dto.NetworkPolicyPort{{Port: 5432, Protocol: "TCP"}},
			}},
		},
	})
	require.NoError(t, err)
	require.Len(t, preview.AffectedPeers, 2, "both peers are affected")

	var nodeA vo.PolicyPeerPreview
	for _, pp := range preview.AffectedPeers {
		if pp.Name == "a" {
			nodeA = pp
		}
	}
	require.Len(t, nodeA.Added, 1)
	assert.Equal(t, "ACCEPT", nodeA.Added[0].Action)
	assert.Equal(t, []string{"10.96.0.3/32"}, nodeA.Added[0].Peers)
}

func TestPolicyService_PreviewEditingExcludesSelf(t *testing.T) {
	st := newPreviewStore(t)
	ctx := context.Background()
	seedEnrollmentToken(t, st, nil)
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Dev",
	}))
	peerSvc := service.NewPeerService(nil, st, nil, &fakeVerifier{valid: false})
	policySvc := service.NewPolicyService(nil, st)
	_, err := peerSvc.Register(ctx, &dto.PeerDto{Name: "a", AppID: "app-a", Token: "enr-test-token"})
	require.NoError(t, err)

	// Existing policy with the same name (its old rules must be excluded
	// from the "before" baseline so the diff shows the net effect).
	require.NoError(t, st.Policies().Create(ctx, &models.Policy{
		WorkspaceID: "ws1", Name: "old-rule", Action: "Deny", Status: models.PolicyStatusActive,
		Spec: `{"egress":[{"to":[{"ipBlock":{"cidr":"10.96.0.3/32"}}]}]}`,
	}))

	preview, err := policySvc.PreviewPolicy(ctx, "ws1", dto.PolicyDto{
		Name:   "old-rule",
		Action: "Allow",
		PolicySpec: dto.PolicySpec{
			Egress: []dto.EgressRule{{
				To:    []dto.PeerSelection{{IPBlock: &dto.IPBlock{CIDR: "10.96.0.3/32"}}},
				Ports: []dto.NetworkPolicyPort{{Port: 8080, Protocol: "TCP"}},
			}},
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, preview.AffectedPeers)

	pp := preview.AffectedPeers[0]
	require.NotEmpty(t, pp.Removed, "the old rule must show as removed")
	assert.Equal(t, []string{"10.96.0.3/32"}, pp.Removed[0].Peers)
	require.NotEmpty(t, pp.Added, "the new rule must show as added")
}

func TestPolicyService_ExportImportRoundtrip(t *testing.T) {
	st := newPreviewStore(t)
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1", DisplayName: "Dev",
	}))
	policySvc := service.NewPolicyService(nil, st)

	original := dto.PolicyDto{
		Name:   "allow-db",
		Action: "Allow",
		Intent: "只允许访问数据库 5432",
		PolicySpec: dto.PolicySpec{
			Egress: []dto.EgressRule{{
				To:    []dto.PeerSelection{{IPBlock: &dto.IPBlock{CIDR: "10.96.0.3/32"}}},
				Ports: []dto.NetworkPolicyPort{{Port: 5432, Protocol: "TCP"}},
			}},
		},
	}
	_, err := policySvc.ApplyDirect(ctx, "ws1", "op", "Op", &original)
	require.NoError(t, err)

	yamlOut, err := policySvc.ExportPolicies(ctx, "ws1")
	require.NoError(t, err)
	assert.Contains(t, yamlOut, "allow-db")
	assert.Contains(t, yamlOut, "只允许访问数据库 5432")
	assert.Contains(t, yamlOut, "lattice.io/v1alpha1")

	// Re-import into the same workspace as dry-run: validated, nothing duplicated.
	res, err := policySvc.ImportPolicies(ctx, "ws1", yamlOut, true, "op", "Op")
	require.NoError(t, err)
	assert.True(t, res.DryRun)
	require.Len(t, res.Items, 1)
	assert.True(t, res.Items[0].OK)

	// Real import into a second workspace: policy is applied there.
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws2"}, Namespace: "wf-ws2", DisplayName: "Prod",
	}))
	res, err = policySvc.ImportPolicies(ctx, "ws2", yamlOut, false, "op", "Op")
	require.NoError(t, err)
	assert.Equal(t, 1, res.OK)
	assert.Zero(t, res.Failed)

	got, err := st.Policies().GetByName(ctx, "ws2", "allow-db")
	require.NoError(t, err)
	assert.Equal(t, models.PolicyStatusActive, got.Status)
	assert.Equal(t, "只允许访问数据库 5432", got.Intent, "intent must travel with the policy")
}

func TestPolicyService_ImportRejectsInvalidCIDR(t *testing.T) {
	st := newPreviewStore(t)
	ctx := context.Background()
	require.NoError(t, st.Workspaces().Create(ctx, &models.Workspace{
		Model: models.Model{ID: "ws1"}, Namespace: "wf-ws1",
	}))
	bundle := `apiVersion: lattice.io/v1alpha1
kind: LatticePolicy
metadata:
  name: bad
  workspaceId: ws1
action: Allow
spec:
  egress:
    - to:
        - ipBlock:
            cidr: not-a-cidr
`
	res, err := policySvcForTest(st).ImportPolicies(ctx, "ws1", bundle, true, "op", "Op")
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	assert.NotEmpty(t, res.Items[0].Error, "invalid CIDR must fail the item")
}

func policySvcForTest(st store.Store) service.PolicyService {
	return service.NewPolicyService(nil, st)
}
