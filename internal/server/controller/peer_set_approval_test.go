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

package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/alatticeio/lattice/internal/server/service"
)

// stubPeerService records SetPeerApproval calls. It embeds the
// service.PeerService interface so the unexported bootstrap method (which
// external types cannot implement) is satisfied without a real service.
type stubPeerService struct {
	service.PeerService

	calls        int
	gotNamespace string
	gotName      string
	gotStatus    string
	err          error
}

func (s *stubPeerService) SetPeerApproval(_ context.Context, namespace, name, status string) error {
	s.calls++
	s.gotNamespace = namespace
	s.gotName = name
	s.gotStatus = status
	return s.err
}

func TestPeerControllerSetPeerApprovalForwardsToService(t *testing.T) {
	stub := &stubPeerService{}
	p := &peerController{peerService: stub}

	err := p.SetPeerApproval(context.Background(), "ws-abc123", "device-p", "approved")

	if err != nil {
		t.Fatalf("SetPeerApproval returned error: %v", err)
	}
	if stub.calls != 1 {
		t.Fatalf("service calls = %d, want 1", stub.calls)
	}
	if stub.gotNamespace != "ws-abc123" || stub.gotName != "device-p" || stub.gotStatus != "approved" {
		t.Fatalf("service got (ns=%q name=%q status=%q), want (ws-abc123, device-p, approved)",
			stub.gotNamespace, stub.gotName, stub.gotStatus)
	}
}

func TestPeerControllerSetPeerApprovalPropagatesServiceError(t *testing.T) {
	wantErr := errors.New("peer \"device-p\" not found")
	stub := &stubPeerService{err: wantErr}
	p := &peerController{peerService: stub}

	err := p.SetPeerApproval(context.Background(), "ws-abc123", "device-p", "revoked")

	if !errors.Is(err, wantErr) {
		t.Fatalf("SetPeerApproval error = %v, want %v", err, wantErr)
	}
	if stub.gotStatus != "revoked" {
		t.Fatalf("service got status %q, want %q", stub.gotStatus, "revoked")
	}
}
