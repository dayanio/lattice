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
	latticev1alpha1 "github.com/alatticeio/lattice/api/v1alpha1"
	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/resource"
	"github.com/alatticeio/lattice/internal/server/vo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type NetworkService interface {
	CreateNetwork(ctx context.Context, networkId, cidr string) (*infra.Network, error)
	JoinNetwork(ctx context.Context, appIds []string, networkId string) error
	LeaveNetwork(ctx context.Context, appIds []string, networkId string) error
	ListTokens(ctx context.Context, pageParam *dto.PageRequest) (*dto.PageResult[vo.TokenVo], error)
}

type networkService struct {
	client *resource.Client
	store  store.Store
}

// listTokensStandalone serves enrollment tokens from the DB registry.
func (s *networkService) listTokensStandalone(ctx context.Context, pageParam *dto.PageRequest) (*dto.PageResult[vo.TokenVo], error) {
	workspaceV := ctx.Value(infra.WorkspaceKey)
	var workspaceId string
	if workspaceV != nil {
		workspaceId = workspaceV.(string)
	}
	workspace, err := s.store.Workspaces().GetByID(ctx, workspaceId)
	if err != nil {
		empty := &dto.PageResult[vo.TokenVo]{List: []vo.TokenVo{}, Total: 0}
		return empty, nil
	}
	rows, err := s.store.EnrollmentTokens().ListByWorkspace(ctx, workspaceId)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	allTokens := []*vo.TokenVo{}
	for _, r := range rows {
		allTokens = append(allTokens, &vo.TokenVo{
			Token:                r.Token,
			Namespace:            workspace.Namespace,
			WorkspaceDisplayName: workspace.DisplayName,
			UsageLimit:           r.UsageLimit,
			Expiry:               metav1.NewTime(r.ExpiresAt),
			UsedCount:            r.UsedCount,
			IsExpired:            now.After(r.ExpiresAt),
			Phase:                "active",
		})
	}

	total := len(allTokens)
	page := pageParam.Page
	if page < 1 {
		page = 1
	}
	start := (page - 1) * pageParam.PageSize
	end := start + pageParam.PageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}
	vals := make([]vo.TokenVo, 0, len(allTokens[start:end]))
	for _, tk := range allTokens[start:end] {
		vals = append(vals, *tk)
	}
	return &dto.PageResult[vo.TokenVo]{
		Page:     page,
		PageSize: pageParam.PageSize,
		Total:    int64(total),
		List:     vals,
	}, nil
}

func (s *networkService) ListTokens(ctx context.Context, pageParam *dto.PageRequest) (*dto.PageResult[vo.TokenVo], error) {
	if s.client == nil {
		return s.listTokensStandalone(ctx, pageParam)
	}
	var (
		tokenList latticev1alpha1.LatticeEnrollmentTokenList
		err       error
	)

	workspaceV := ctx.Value(infra.WorkspaceKey)
	var workspaceId string
	if workspaceV != nil {
		workspaceId = workspaceV.(string)
	}

	workspace, err := s.store.Workspaces().GetByID(ctx, workspaceId)
	if err != nil {
		// workspace not found — return empty list instead of propagating the error
		empty := &dto.PageResult[vo.TokenVo]{List: []vo.TokenVo{}, Total: 0}
		return empty, nil
	}

	err = s.client.GetAPIReader().List(ctx, &tokenList, client.InNamespace(workspace.Namespace))
	if err != nil {
		return nil, err
	}

	allTokens := []*vo.TokenVo{}

	for _, item := range tokenList.Items {
		workspaceDisplayName := ""
		if ws, err := s.store.Workspaces().GetByNamespace(ctx, item.Namespace); err == nil && ws != nil {
			workspaceDisplayName = ws.DisplayName
		}
		allTokens = append(allTokens, &vo.TokenVo{
			Namespace:            item.Namespace,
			WorkspaceDisplayName: workspaceDisplayName,
			Token:                item.Status.Token,
			Expiry:               item.Spec.Expiry,
			UsageLimit:           item.Spec.UsageLimit,
			BoundPeers:           item.Status.BoundPeers,
			UsedCount:            item.Status.UsedCount,
			IsExpired:            item.Status.IsExpired,
			Phase:                item.Status.Phase,
		})
	}

	var filteredTokens []*vo.TokenVo
	if pageParam.Keyword != "" {
		for _, n := range allTokens {
			if strings.Contains(n.Token, pageParam.Keyword) {
				filteredTokens = append(filteredTokens, n)
			}
		}
	} else {
		filteredTokens = allTokens
	}

	total := len(filteredTokens)
	start := (pageParam.Page - 1) * pageParam.PageSize
	end := start + pageParam.PageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	var res []vo.TokenVo
	for _, n := range filteredTokens[start:end] {
		res = append(res, *n)
	}

	return &dto.PageResult[vo.TokenVo]{
		Page:     pageParam.Page,
		PageSize: pageParam.PageSize,
		Total:    int64(len(allTokens)),
		List:     res,
	}, nil
}

func NewNetworkService(client *resource.Client, st store.Store) NetworkService {
	return &networkService{
		client: client,
		store:  st,
	}
}

func (s *networkService) CreateNetwork(ctx context.Context, networkId, cidr string) (*infra.Network, error) {
	network, err := s.client.CreateNetwork(ctx, networkId, cidr)
	if err != nil {
		return nil, err
	}
	return &infra.Network{NetworkName: network.Name}, nil
}

func (s *networkService) JoinNetwork(ctx context.Context, appIds []string, networkId string) error {
	if networkId == "" {
		return nil
	}
	for _, appId := range appIds {
		if err := s.client.UpdateNodeSepc(ctx, "default", appId, func(node *latticev1alpha1.LatticePeer) {
			node.Spec.Network = &networkId
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *networkService) LeaveNetwork(ctx context.Context, appIds []string, networkId string) error {
	if networkId == "" {
		return nil
	}
	for _, appId := range appIds {
		if err := s.client.UpdateNodeSepc(ctx, "default", appId, func(node *latticev1alpha1.LatticePeer) {
			node.Spec.Network = nil
		}); err != nil {
			return err
		}
	}
	return nil
}
