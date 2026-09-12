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
	"fmt"
	"io"
	"strings"

	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/vo"
	"github.com/goccy/go-yaml"
)

// policyBundleItem is the YAML unit of the policy-as-code bundle.
type policyBundleItem struct {
	APIVersion string           `yaml:"apiVersion" json:"apiVersion"`
	Kind       string           `yaml:"kind" json:"kind"`
	Metadata   policyBundleMeta `yaml:"metadata" json:"metadata"`
	Intent     string           `yaml:"intent,omitempty" json:"intent,omitempty"`
	Action     string           `yaml:"action" json:"action"`
	Spec       dto.PolicySpec   `yaml:"spec" json:"spec"`
}

type policyBundleMeta struct {
	Name        string `yaml:"name" json:"name"`
	WorkspaceID string `yaml:"workspaceId" json:"workspaceId"`
}

// ExportPolicies renders the workspace's active policies as a multi-document
// YAML bundle — the "策略即代码" artifact for Git and CI.
func (p *policyService) ExportPolicies(ctx context.Context, wsID string) (string, error) {
	rows, err := p.store.Policies().ListActiveByWorkspace(ctx, wsID)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	var b strings.Builder
	for i, row := range rows {
		var spec dto.PolicySpec
		if err := yaml.NewDecoder(strings.NewReader(row.Spec)).Decode(&spec); err != nil {
			return "", fmt.Errorf("parse spec of policy %q: %w", row.Name, err)
		}
		item := policyBundleItem{
			APIVersion: "lattice.io/v1alpha1",
			Kind:       "LatticePolicy",
			Metadata:   policyBundleMeta{Name: row.Name, WorkspaceID: row.WorkspaceID},
			Intent:     row.Intent,
			Action:     row.Action,
			Spec:       spec,
		}
		doc, err := yaml.Marshal(item)
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString("---\n")
		}
		b.Write(doc)
	}
	return b.String(), nil
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

// ImportPolicies parses a YAML bundle and validates each policy through the
// semantic gates. With dryRun=true nothing is persisted; otherwise each valid
// policy is applied (ApplyDirect semantics: upsert + activate).
func (p *policyService) ImportPolicies(ctx context.Context, wsID, content string, dryRun bool, operatorID, operatorName string) (*vo.PolicyImportVo, error) {
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("empty bundle")
	}

	dec := yaml.NewDecoder(strings.NewReader(content))
	out := &vo.PolicyImportVo{DryRun: dryRun, Items: []vo.PolicyImportItem{}}

	for doc := 1; ; doc++ {
		var item policyBundleItem
		err := dec.Decode(&item)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse document %d: %w", doc, err)
		}

		result := vo.PolicyImportItem{Name: item.Metadata.Name, DryRun: dryRun}

		if item.Kind != "" && item.Kind != "LatticePolicy" {
			result.Error = "unsupported kind " + item.Kind
			out.Failed++
			out.Items = append(out.Items, result)
			continue
		}
		if item.Metadata.Name == "" {
			result.Error = "missing metadata.name"
			out.Failed++
			out.Items = append(out.Items, result)
			continue
		}

		warnings, vErr := validatePolicySpecSemantics(ctx, p.store, wsID, &item.Spec)
		if vErr == nil {
			action := strings.ToUpper(strings.TrimSpace(item.Action))
			if action != "ALLOW" && action != "DENY" {
				vErr = fmt.Errorf("invalid action %q", item.Action)
			}
		}
		if vErr != nil {
			result.Error = vErr.Error()
			out.Failed++
		} else if dryRun {
			result.OK = true
			result.Action = "validated"
			result.Warnings = warnings
			out.OK++
		} else {
			_, vErr = p.ApplyDirect(ctx, wsID, operatorID, operatorName, &dto.PolicyDto{
				Name:        item.Metadata.Name,
				Action:      normalizeBundleAction(item.Action),
				Description: "imported from policy bundle",
				PolicyTypes: deriveBundlePolicyTypes(&item.Spec),
				PolicySpec:  item.Spec,
				Intent:      item.Intent,
			})
			if vErr != nil {
				result.Error = vErr.Error()
				out.Failed++
			} else {
				result.OK = true
				result.Action = "applied"
				result.Warnings = warnings
				out.OK++
			}
		}
		out.Items = append(out.Items, result)
	}
	return out, nil
}

// deriveBundlePolicyTypes derives direction flags from the spec contents.
func deriveBundlePolicyTypes(spec *dto.PolicySpec) []string {
	types := []string{}
	if len(spec.Ingress) > 0 {
		types = append(types, "Ingress")
	}
	if len(spec.Egress) > 0 {
		types = append(types, "Egress")
	}
	if len(types) == 0 {
		types = []string{"Ingress", "Egress"}
	}
	return types
}

func normalizeBundleAction(a string) string {
	switch strings.ToUpper(strings.TrimSpace(a)) {
	case "DENY":
		return "Deny"
	default:
		return "Allow"
	}
}

// compile-time checks that the DB models referenced here stay aligned.
var (
	_ models.PolicyStatus = models.PolicyStatusActive
	_                     = fmt.Sprintf
)
