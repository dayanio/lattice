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

package engine

import (
	wgdevice "golang.zx2c4.com/wireguard/device"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/provision"
)

// neProvisioner adapts the agent's Provisioner to the Apple Network Extension
// environment, mirroring internal/agent/gvisor's sandboxProvisioner without
// dragging the gVisor netstack into the mobile framework:
//
//   - WireGuard peer operations go through wireguard-go's IpcSet so the
//     in-process device receives peer configuration.
//   - Route/IP/policy operations are no-ops: on Apple platforms the Swift
//     side owns interface addresses and routes via NEPacketTunnelNetworkSettings,
//     and per-peer policy enforcement arrives with the netstack milestone.
type neProvisioner struct {
	device    *wgdevice.Device
	localIP   string
	ifaceName string
}

func newNEProvisionerFactory(localIP, ifaceName string) func(*wgdevice.Device) provision.Provisioner {
	return func(dev *wgdevice.Device) provision.Provisioner {
		return &neProvisioner{
			device:    dev,
			localIP:   localIP,
			ifaceName: ifaceName,
		}
	}
}

func (p *neProvisioner) SetupInterface(conf *infra.DeviceConfig) error {
	return p.device.IpcSet(conf.String())
}

func (p *neProvisioner) AddPeer(peer *provision.SetPeer) error {
	return p.device.IpcSet(peer.String())
}

func (p *neProvisioner) RemovePeer(peer *provision.SetPeer) error {
	return p.device.IpcSet(peer.String())
}

func (p *neProvisioner) RemoveAllPeers() {
	p.device.RemoveAllPeers()
}

func (p *neProvisioner) GetAddress() string                    { return p.localIP }
func (p *neProvisioner) GetIfaceName() string                  { return p.ifaceName }
func (p *neProvisioner) ApplyRoute(_, _, _ string) error       { return nil }
func (p *neProvisioner) ApplyIP(_, _, _ string) error          { return nil }
func (p *neProvisioner) Name() string                          { return "ne" }
func (p *neProvisioner) Provision(_ *infra.FirewallRule) error { return nil }
func (p *neProvisioner) Cleanup() error                        { return nil }
func (p *neProvisioner) SetupNAT(_ string) error               { return nil }

var _ provision.Provisioner = (*neProvisioner)(nil)
