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

package agent

import (
	"context"
	"runtime"

	"github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/agent/provision"
)

// IPv6Egress reports whether this node, acting as an exit node, has probed a
// working IPv6 egress. False on every node that is not an exit, and before the
// first probe. The heartbeat carries it to the control plane.
func (c *Node) IPv6Egress() bool {
	p := c.ipv6Prober.Load()
	return p != nil && p.Egress()
}

// StartIPv6Probe runs the IPv6 egress probe until ctx is cancelled. Linux only
// (the exit gateway is Linux only); it returns immediately elsewhere. The
// forwarding and NAT66 rules are installed by the prober's setup step, right
// before egress is first reported, so it is never advertised without them.
func (c *Node) StartIPv6Probe(ctx context.Context) {
	if runtime.GOOS != "linux" {
		return
	}
	logger := log.GetLogger("ipv6-probe")
	p := provision.NewIPv6Prober(provision.IPv6ProberConfig{
		IsExit: c.isExitNode,
		Setup: func() error {
			return provision.EnsureExitGateway6(infra.ExecCommand, c.GetDeviceName(), runtime.GOOS)
		},
		OnChange: func(egress bool) { logger.Info("IPv6 egress changed", "egress", egress) },
	})
	c.ipv6Prober.Store(p)
	p.Run(ctx)
}

// isExitNode reports whether this node advertises routes, the same test that
// makes the message handler set up the IPv4 exit gateway.
func (c *Node) isExitNode() bool {
	lp := c.GetPeerManager().GetPeer(config.Conf.AppId)
	return lp != nil && len(lp.AdvertisedRoutes) > 0
}
