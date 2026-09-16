package vo

import (
	"github.com/alatticeio/lattice/api/v1alpha1"
	"github.com/alatticeio/lattice/internal/server/dto"
)

type PolicyVo struct {
	Name        string   `json:"name"`
	Action      string   `json:"action"`
	Description string   `json:"description"`
	Namespace   string   `json:"namespace"`
	PolicyTypes []string `json:"policyTypes"`
	// Status reflects the DB record status: pending / approved / active / failed
	Status                      string `json:"status,omitempty"`
	CreatedBy                   string `json:"createdBy,omitempty"`
	CreatedByName               string `json:"createdByName,omitempty"`
	CreatedAt                   string `json:"createdAt,omitempty"`
	UpdatedBy                   string `json:"updatedBy,omitempty"`
	UpdatedByName               string `json:"updatedByName,omitempty"`
	UpdatedAt                   string `json:"updatedAt,omitempty"`
	*v1alpha1.LatticePolicySpec `json:",inline"`
}

// PolicyPreviewRule is one rendered traffic decision from a preview diff.
type PolicyPreviewRule struct {
	Direction string   `json:"direction"` // ingress | egress
	Peers     []string `json:"peers"`
	Port      int      `json:"port"`
	Protocol  string   `json:"protocol"`
	Action    string   `json:"action"` // ACCEPT | DROP
}

// PolicyPeerPreview is the per-node effect of a draft policy.
type PolicyPeerPreview struct {
	Name    string              `json:"name"`
	Address string              `json:"address,omitempty"`
	Added   []PolicyPreviewRule `json:"added"`
	Removed []PolicyPreviewRule `json:"removed"`
}

// PolicyPreviewVo is the deterministic effect preview of a draft policy.
type PolicyPreviewVo struct {
	Warnings      []string            `json:"warnings,omitempty"`
	AffectedPeers []PolicyPeerPreview `json:"affectedPeers"`
}

// PolicyTranslationVo is the LLM translation of a natural-language
// description into a draft PolicySpec, plus summary and warnings.
type PolicyTranslationVo struct {
	Spec     dto.PolicySpec `json:"spec"`
	Summary  string         `json:"summary"`
	Warnings []string       `json:"warnings,omitempty"`
}

// PolicyDeliveryStatusVo is the workspace-level policy convergence view:
// expected netmap version vs what each node reports applied.
type PolicyDeliveryStatusVo struct {
	Total          int                        `json:"total"`
	ConvergedCount int                        `json:"convergedCount"`
	Converged      bool                       `json:"converged"`
	Peers          []PolicyDeliveryStatusPeer `json:"peers"`
}

// PolicyDeliveryStatusPeer is one node's convergence state.
type PolicyDeliveryStatusPeer struct {
	Name           string `json:"name"`
	Address        string `json:"address,omitempty"`
	AppliedVersion string `json:"appliedVersion,omitempty"`
	Converged      bool   `json:"converged"`
}

// FlowStatsVo aggregates observed traffic for a workspace.
type FlowStatsVo struct {
	WorkspaceID string           `json:"workspaceId"`
	Since       string           `json:"since"`
	Days        int              `json:"days"`
	TotalFlows  int64            `json:"totalFlows"`
	TotalBytes  int64            `json:"totalBytes"`
	PerAgent    []FlowAgentStats `json:"perAgent"`
}

// FlowAgentStats is one agent's traffic totals within the window.
type FlowAgentStats struct {
	AgentID string `json:"agentId"`
	Name    string `json:"name,omitempty"`
	Flows   int64  `json:"flows"`
	Bytes   int64  `json:"bytes"`
}

// PolicyImportItem is one policy's import outcome.
type PolicyImportItem struct {
	Name     string   `json:"name"`
	DryRun   bool     `json:"dryRun"`
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
	Action   string   `json:"action,omitempty"` // applied / validated
	Warnings []string `json:"warnings,omitempty"`
}

// PolicyImportVo reports per-item import outcomes.
type PolicyImportVo struct {
	DryRun bool               `json:"dryRun"`
	Total  int                `json:"total"`
	OK     int                `json:"ok"`
	Failed int                `json:"failed"`
	Items  []PolicyImportItem `json:"items"`
}
