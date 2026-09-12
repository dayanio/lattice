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
	"net"
	"strconv"
	"strings"

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/llm"
	"github.com/alatticeio/lattice/internal/server/vo"
)

// PolicyIntentService implements "描述即策略": it translates a natural
// language description into a dto.PolicySpec through an LLM, then hands the
// result to deterministic validation gates. The LLM only ever TRANSLATES —
// application and enforcement are handled by the existing deterministic
// pipeline (ApplyDirect → netmap → iptables), and every generated policy
// still passes the same approval gates as a hand-created one.
type PolicyIntentService interface {
	// Translate converts a natural-language description into a draft
	// PolicySpec plus a human summary and translation warnings. It has no
	// side effects: the caller still previews and submits.
	Translate(ctx context.Context, workspaceID, description string) (*vo.PolicyTranslationVo, error)
}

type policyIntentService struct {
	llm   llm.Client
	store store.Store
}

// NewPolicyIntentService returns the LLM-backed translator. A nil client
// yields a service whose Translate reports that AI is not configured.
func NewPolicyIntentService(lm llm.Client, st store.Store) PolicyIntentService {
	return &policyIntentService{llm: lm, store: st}
}

// policyDraftLLM is the JSON contract the LLM must return.
type policyDraftLLM struct {
	Spec     dto.PolicySpec `json:"spec"`
	Summary  string         `json:"summary"`
	Warnings []string       `json:"warnings"`
}

const policyIntentSystemPrompt = `你是 Lattice 网络策略翻译器。把用户的自然语言描述翻译成 LatticePolicySpec JSON。
规则：
1. 只输出一个 JSON 对象，不输出任何解释或代码块标记。格式：
   {"spec": {...}, "summary": "<一句话中文复述>", "warnings": ["<不确定点>"]}
2. spec.egress/ingress 的 selection 只能使用 ipBlock.cidr 或 identityRef；
   identityRef 只能引用"可用身份列表"中的名字。
3. 用户未提及的维度一律省略字段；未指定端口则省略 ports（语义为全部端口）。
4. spec.network 必须是 "%s"。
可用节点（ipBlock 应覆盖其所在网段）：%s
可用身份：%s
宁可少限制并给出 warnings，也不要猜测不存在的名字。`

// Translate converts a natural-language description into a draft spec.
func (s *policyIntentService) Translate(ctx context.Context, workspaceID, description string) (*vo.PolicyTranslationVo, error) {
	if s.llm == nil {
		return nil, fmt.Errorf("AI translation is not configured (set ai.enabled + ai.api-key)")
	}
	description = strings.TrimSpace(description)
	if description == "" {
		return nil, fmt.Errorf("description is empty")
	}

	// Dynamic allow-list context: the LLM may only reference real names.
	peerNames := []string{}
	identityNames := []string{}
	if rows, err := s.store.Peers().ListByWorkspace(ctx, workspaceID); err == nil {
		for _, r := range rows {
			peerNames = append(peerNames, fmt.Sprintf("%s(%s)", r.Name, r.Address))
		}
	}
	if rows, err := s.store.PeerIdentities().ListByNetwork(ctx, workspaceID); err == nil {
		for _, r := range rows {
			identityNames = append(identityNames, r.Name)
		}
	}

	system := fmt.Sprintf(policyIntentSystemPrompt, workspaceID,
		joinOrEmpty(peerNames), joinOrEmpty(identityNames))

	resp, err := s.llm.Complete(ctx, &llm.Request{
		System: system,
		Messages: []llm.Message{
			{Role: "user", Content: description},
		},
		MaxTokens: 1024,
	})
	if err != nil {
		return nil, fmt.Errorf("llm complete: %w", err)
	}

	draft, err := parsePolicyDraft(resp.Content)
	if err != nil {
		return nil, fmt.Errorf("parse translation: %w", err)
	}

	// Gate: deterministic semantic validation. The LLM's own warnings are
	// appended, never trusted in place of the gates.
	warnings, err := validatePolicySpecSemantics(ctx, s.store, workspaceID, &draft.Spec)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, draft.Warnings...)

	draft.Spec.Network = workspaceID
	return &vo.PolicyTranslationVo{
		Spec:     draft.Spec,
		Summary:  draft.Summary,
		Warnings: warnings,
	}, nil
}

// parsePolicyDraft extracts the JSON object from an LLM completion,
// tolerating markdown code fences, then normalizes known LLM quirks
// (identityRef as object, string-typed ports) before decoding.
func parsePolicyDraft(content string) (*policyDraftLLM, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	start := strings.Index(content, "{")
	end := strings.LastIndex(content, "}")
	if start < 0 || end <= start {
		return nil, fmt.Errorf("no JSON object in completion")
	}

	var raw struct {
		Spec     json.RawMessage `json:"spec"`
		Summary  string          `json:"summary"`
		Warnings []string        `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &raw); err != nil {
		return nil, err
	}

	spec, err := lenientSpecDecode(raw.Spec)
	if err != nil {
		return nil, err
	}
	return &policyDraftLLM{Spec: spec, Summary: raw.Summary, Warnings: raw.Warnings}, nil
}

// lenientSpecDecode decodes a PolicySpec tolerating LLM quirks: coercing
// identityRef objects to their name and string-typed ports to numbers.
func lenientSpecDecode(raw json.RawMessage) (dto.PolicySpec, error) {
	var spec dto.PolicySpec
	if len(raw) == 0 {
		return spec, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return spec, err
	}
	normalizeSelections := func(rules any) {
		list, ok := rules.([]any)
		if !ok {
			return
		}
		for _, rv := range list {
			rule, ok := rv.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"from", "to"} {
				sels, ok := rule[key].([]any)
				if !ok {
					continue
				}
				for i, sv := range sels {
					sel, ok := sv.(map[string]any)
					if !ok {
						continue
					}
					if ref, exists := sel["identityRef"]; exists {
						switch v := ref.(type) {
						case string:
							sel["identityRef"] = v
						case map[string]any:
							for _, k := range []string{"name", "id", "value"} {
								if s, ok := v[k].(string); ok {
									sel["identityRef"] = s
									break
								}
							}
						}
					}
					sels[i] = sel
				}
				rule[key] = sels
			}
			if ports, ok := rule["ports"].([]any); ok {
				for i, pv := range ports {
					if pm, ok := pv.(map[string]any); ok {
						switch v := pm["port"].(type) {
						case string:
							if n, err := strconv.Atoi(v); err == nil {
								pm["port"] = float64(n)
							}
						case float64:
							pm["port"] = v
						}
						ports[i] = pm
					}
				}
				rule["ports"] = ports
			}
		}
	}
	normalizeSelections(m["ingress"])
	normalizeSelections(m["egress"])

	norm, err := json.Marshal(m)
	if err != nil {
		return spec, err
	}
	if err := json.Unmarshal(norm, &spec); err != nil {
		return spec, err
	}
	return spec, nil
}

func joinOrEmpty(items []string) string {
	if len(items) == 0 {
		return "（空）"
	}
	return strings.Join(items, ", ")
}

// validatePolicySpecSemantics applies the deterministic semantic gates to a
// draft spec: CIDR syntax, identityRef existence, port sanity. It returns
// collected warnings (non-fatal) and an error for hard failures.
func validatePolicySpecSemantics(ctx context.Context, st store.Store, workspaceID string, spec *dto.PolicySpec) ([]string, error) {
	var warnings []string

	knownIdentities := map[string]bool{}
	if rows, err := st.PeerIdentities().ListByNetwork(ctx, workspaceID); err == nil {
		for _, r := range rows {
			knownIdentities[r.Name] = true
		}
	}

	checkSelections := func(dir string, selections []dto.PeerSelection) error {
		for _, sel := range selections {
			if sel.IPBlock != nil && sel.IPBlock.CIDR != "" {
				if _, _, err := net.ParseCIDR(sel.IPBlock.CIDR); err != nil {
					return fmt.Errorf("%s: invalid CIDR %q", dir, sel.IPBlock.CIDR)
				}
			}
			if sel.IdentityRef != "" && !knownIdentities[sel.IdentityRef] {
				warnings = append(warnings, fmt.Sprintf(
					"%s: identityRef %q 不存在，该 selection 将匹配零个节点（fail-closed）", dir, sel.IdentityRef))
			}
		}
		return nil
	}

	for _, r := range spec.Ingress {
		if err := checkSelections("ingress", r.From); err != nil {
			return nil, err
		}
	}
	for _, r := range spec.Egress {
		if err := checkSelections("egress", r.To); err != nil {
			return nil, err
		}
	}
	for _, r := range spec.Ingress {
		for _, port := range r.Ports {
			if port.Port < 0 || port.Port > 65535 {
				return nil, fmt.Errorf("ingress: invalid port %d", port.Port)
			}
		}
	}
	for _, r := range spec.Egress {
		for _, port := range r.Ports {
			if port.Port < 0 || port.Port > 65535 {
				return nil, fmt.Errorf("egress: invalid port %d", port.Port)
			}
		}
	}
	return warnings, nil
}
