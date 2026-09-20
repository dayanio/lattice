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

	agentconfig "github.com/alatticeio/lattice/internal/agent/config"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/stretchr/testify/require"
)

func registerWithRelayConfig(t *testing.T, advertise, authToken string) string {
	t.Helper()
	prevAdvertise, prevToken := agentconfig.Conf.RelayAdvertiseURL, agentconfig.Conf.RelayAuthToken
	agentconfig.Conf.RelayAdvertiseURL, agentconfig.Conf.RelayAuthToken = advertise, authToken
	t.Cleanup(func() {
		agentconfig.Conf.RelayAdvertiseURL, agentconfig.Conf.RelayAuthToken = prevAdvertise, prevToken
	})

	svc, st := newRegisterService(t, &fakeVerifier{valid: false})
	seedEnrollmentToken(t, st, nil)
	node, err := svc.Register(context.Background(), &dto.PeerDto{Name: "api", AppID: "app-1", Token: "enr-test-token"})
	require.NoError(t, err)
	return node.RelayURL
}

// Agents create their relay client only when the registration response carries
// a relay URL, so a control plane that runs a relay must hand it out.
func TestRegisterStandalone_AdvertisesRelayURL(t *testing.T) {
	require.Equal(t, "relay.example:6266", registerWithRelayConfig(t, "relay.example:6266", ""))
}

// The relay rejects clients that do not present the shared token, so the
// advertised URL must carry it.
func TestRegisterStandalone_RelayURLCarriesAuthToken(t *testing.T) {
	require.Equal(t, "relay.example:6266?token=s3cret", registerWithRelayConfig(t, "relay.example:6266", "s3cret"))
}

func TestRegisterStandalone_RelayURLWithExplicitTokenIsKept(t *testing.T) {
	require.Equal(t, "relay.example:6266?token=abc", registerWithRelayConfig(t, "relay.example:6266?token=abc", "s3cret"))
}

func TestRegisterStandalone_NoRelayConfiguredAdvertisesNothing(t *testing.T) {
	require.Empty(t, registerWithRelayConfig(t, "", "s3cret"))
}
