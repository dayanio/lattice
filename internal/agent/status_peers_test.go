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
	"reflect"
	"testing"

	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/wireguard"
	"github.com/alatticeio/lattice/internal/daemon"
)

func TestBuildPeerStatuses(t *testing.T) {
	peers := []*infra.Peer{
		{AppID: "iPhone", Name: "iPhone", PublicKey: "key-phone"},
		{AppID: "cloud-node-1", Name: "cloud-node-1", PublicKey: "key-cloud"},
		{AppID: "no-key"},
	}
	states := map[string]string{"cloud-node-1": "ice-ready", "stale-probe": "failed"}

	got := buildPeerStatuses(peers, states)

	want := []daemon.PeerStatus{
		{AppID: "cloud-node-1", Name: "cloud-node-1", PublicKey: "key-cloud", Transport: "ice-ready"},
		{AppID: "iPhone", Name: "iPhone", PublicKey: "key-phone", Transport: "none"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildPeerStatuses() = %+v\nwant %+v", got, want)
	}
}

func TestPeerLabels(t *testing.T) {
	info := &daemon.StatusInfo{Peers: []daemon.PeerStatus{
		{AppID: "macbook-pro.local", Name: "mac-cloud-node-1", PublicKey: "k1", Transport: "ice-ready"},
		{AppID: "cloud-node-1", Name: "cloud-node-1", PublicKey: "k2", Transport: "relay-ready"},
		{AppID: "iPhone", PublicKey: "k3", Transport: "none"},
	}}

	got := peerLabels(info)

	want := map[string]wireguard.PeerLabel{
		"k1": {Name: "mac-cloud-node-1 (macbook-pro.local)", Transport: "ice-ready"},
		"k2": {Name: "cloud-node-1", Transport: "relay-ready"},
		"k3": {Name: "iPhone", Transport: "none"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("peerLabels() = %+v\nwant %+v", got, want)
	}
}

func TestPeerLabelsNilInfo(t *testing.T) {
	if got := peerLabels(nil); got != nil {
		t.Errorf("peerLabels(nil) = %+v, want nil", got)
	}
}
