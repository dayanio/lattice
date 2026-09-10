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
	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/reconcile"
	"github.com/go-logr/logr"
)

// RegisterAll registers every standalone identity TTL reconciler on the
// runner. It does not start the runner; the caller owns Start/Stop. The
// default resync interval applies, so convergence does not depend on any
// notification reaching the runner.
func RegisterAll(runner *reconcile.Runner, st store.Store, logger logr.Logger, opts ...GCOption) error {
	agents := st.AgentIdentities()
	peers := st.PeerIdentities()

	if err := runner.RegisterWithResync(KindAgentIdentity,
		NewAgentIdentityGC(agents, logger.WithName("agent-identity-gc"), opts...),
		NewAgentIdentityKeyLister(agents),
		reconcile.DefaultResyncInterval,
	); err != nil {
		return err
	}
	return runner.RegisterWithResync(KindPeerIdentity,
		NewPeerIdentityGrace(peers, logger.WithName("peer-identity-grace"), opts...),
		NewPeerIdentityKeyLister(peers),
		reconcile.DefaultResyncInterval,
	)
}
