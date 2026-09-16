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

package provision

import (
	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
)

// NoopEnforcer is a PolicyEnforcer that enforces nothing. It backs
// EnforcerMode none on platforms without a kernel firewall (Apple: the
// Network Extension enforces policy at the route level; containers and
// CI: rules are irrelevant).
type NoopEnforcer struct {
	logger *log.Logger
}

// NewNoopEnforcer returns the no-op enforcer.
func NewNoopEnforcer(logger *log.Logger) *NoopEnforcer {
	return &NoopEnforcer{logger: logger}
}

func (e *NoopEnforcer) Name() string { return "none" }

// Provision records the rule set but applies nothing.
func (e *NoopEnforcer) Provision(rule *infra.FirewallRule) error {
	if e.logger != nil && rule != nil {
		e.logger.Debug("noop enforcer: rules not applied (mode none)", "policy", rule.PolicyName)
	}
	return nil
}

func (e *NoopEnforcer) Cleanup() error { return nil }

func (e *NoopEnforcer) SetupNAT(interfaceName string) error { return nil }
