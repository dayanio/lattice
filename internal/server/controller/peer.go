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
	"encoding/json"
	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/license"
	"github.com/alatticeio/lattice/internal/server/dto"
	managementnats "github.com/alatticeio/lattice/internal/server/nats"
	"github.com/alatticeio/lattice/internal/server/resource"
	"github.com/alatticeio/lattice/internal/server/service"
	"github.com/alatticeio/lattice/internal/server/vo"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	_ PeerController = (*peerController)(nil)
)

type PeerController interface {
	Register(ctx context.Context, request []byte) ([]byte, error)
	GetNetmap(ctx context.Context, request []byte) ([]byte, error)
	CreateToken(ctx context.Context, request []byte) ([]byte, error)
	UpdateStatus(ctx context.Context, status int) error

	ListPeers(ctx context.Context, pageParam *dto.PageRequest) (*dto.PageResult[vo.PeerVo], error)
	PolicyDeliveryStatus(ctx context.Context, workspaceID string) (*vo.PolicyDeliveryStatusVo, error)
	FlowStats(ctx context.Context, workspaceID string, days int) (*vo.FlowStatsVo, error)
	UpdatePeer(ctx context.Context, peerDto *dto.PeerDto) (*vo.PeerVo, error)
	DisablePeer(ctx context.Context, namespace, name string) error
	EnablePeer(ctx context.Context, namespace, name string) error
	DeletePeer(ctx context.Context, namespace, name string) error
	SetAdvertisedRoutes(ctx context.Context, name string, routes []string) error
	SetRouteSelection(ctx context.Context, consumerName, providerName string, selected bool) error
	ListRouteSelections(ctx context.Context, consumerName string) ([]string, error)
}

func NewPeerController(client *resource.Client, st store.Store, presence *managementnats.NodePresenceStore, verifier license.Verifier, signal infra.SignalService) PeerController {
	return &peerController{
		peerService:   service.NewPeerService(client, st, presence, verifier, signal),
		policyService: service.NewPolicyService(client, st),
	}
}

type peerController struct {
	peerService   service.PeerService
	policyService service.PolicyService
}

func (p *peerController) UpdatePeer(ctx context.Context, peerDto *dto.PeerDto) (*vo.PeerVo, error) {
	return p.peerService.UpdatePeer(ctx, peerDto)
}

func (p *peerController) ListPeers(ctx context.Context, pageParam *dto.PageRequest) (*dto.PageResult[vo.PeerVo], error) {
	return p.peerService.ListPeers(ctx, pageParam)
}

func (p *peerController) PolicyDeliveryStatus(ctx context.Context, workspaceID string) (*vo.PolicyDeliveryStatusVo, error) {
	return p.peerService.PolicyDeliveryStatus(ctx, workspaceID)
}

func (p *peerController) FlowStats(ctx context.Context, workspaceID string, days int) (*vo.FlowStatsVo, error) {
	return p.peerService.FlowStats(ctx, workspaceID, days)
}

func (p *peerController) CreateToken(ctx context.Context, request []byte) ([]byte, error) {
	var (
		tokenDto dto.TokenDto
		err      error
	)
	if err = json.Unmarshal(request, &tokenDto); err != nil {
		return nil, err
	}
	res, err := p.peerService.CreateToken(ctx, &tokenDto)
	if err != nil {
		return nil, err
	}

	wsID, _ := ctx.Value(infra.WorkspaceKey).(string)
	if _, err := p.policyService.ApplyDirect(ctx, wsID, "", "", &dto.PolicyDto{
		Name:   tokenDto.Name,
		Action: "Deny",
	}); err != nil {
		return nil, err
	}

	return res, nil
}

func (p *peerController) UpdateStatus(_ context.Context, _ int) error { return nil }

func (p *peerController) DisablePeer(ctx context.Context, namespace, name string) error {
	return p.peerService.DisablePeer(ctx, namespace, name)
}

func (p *peerController) EnablePeer(ctx context.Context, namespace, name string) error {
	return p.peerService.EnablePeer(ctx, namespace, name)
}

func (p *peerController) DeletePeer(ctx context.Context, namespace, name string) error {
	return p.peerService.DeletePeer(ctx, namespace, name)
}

func (p *peerController) SetAdvertisedRoutes(ctx context.Context, name string, routes []string) error {
	return p.peerService.SetAdvertisedRoutes(ctx, name, routes)
}

func (p *peerController) SetRouteSelection(ctx context.Context, consumerName, providerName string, selected bool) error {
	return p.peerService.SetRouteSelection(ctx, consumerName, providerName, selected)
}

func (p *peerController) ListRouteSelections(ctx context.Context, consumerName string) ([]string, error) {
	return p.peerService.ListRouteSelections(ctx, consumerName)
}

func (p *peerController) Register(ctx context.Context, request []byte) ([]byte, error) {
	var req dto.PeerDto
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	peer, err := p.peerService.Register(ctx, &req)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(peer)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (p *peerController) GetNetmap(ctx context.Context, request []byte) ([]byte, error) {
	var (
		peer dto.PeerDto
		err  error
	)
	if err = json.Unmarshal(request, &peer); err != nil {
		return nil, err
	}
	networkMap, err := p.peerService.GetNetmap(ctx, peer.Token, peer.AppID)
	if err != nil {
		return nil, err
	}

	data, err := json.Marshal(networkMap)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal failed: %v", err)
	}
	return data, nil
}
