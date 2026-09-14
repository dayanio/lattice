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
	"cmp"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"github.com/alatticeio/lattice/internal/agent/infra"
	"github.com/alatticeio/lattice/internal/agent/log"
	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/license"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/models"
	managementnats "github.com/alatticeio/lattice/internal/server/nats"
	"github.com/alatticeio/lattice/internal/server/reconcilers"
	"github.com/alatticeio/lattice/internal/server/resource"
	"github.com/alatticeio/lattice/internal/server/vo"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
	"gorm.io/gorm"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/alatticeio/lattice/api/v1alpha1"
)

var (
	_ PeerService = (*peerService)(nil)
)

type PeerService interface {
	Register(ctx context.Context, dto *dto.PeerDto) (*infra.Peer, error)
	UpdateStatus(ctx context.Context, status int) error
	GetNetmap(ctx context.Context, namespace string, appId string) (*infra.Message, error)
	PolicyDeliveryStatus(ctx context.Context, workspaceID string) (*vo.PolicyDeliveryStatusVo, error)
	FlowStats(ctx context.Context, workspaceID string, days int) (*vo.FlowStatsVo, error)
	CreateToken(ctx context.Context, tokenDto *dto.TokenDto) ([]byte, error)
	bootstrap(ctx context.Context, provideToken string) error

	//Peer tenant
	ListPeers(ctx context.Context, pageParam *dto.PageRequest) (*dto.PageResult[vo.PeerVo], error)
	UpdatePeer(ctx context.Context, peerDto *dto.PeerDto) (*vo.PeerVo, error)
	DisablePeer(ctx context.Context, namespace, name string) error
	EnablePeer(ctx context.Context, namespace, name string) error
	DeletePeer(ctx context.Context, namespace, name string) error
	SetAdvertisedRoutes(ctx context.Context, name string, routes []string) error
	SetRouteSelection(ctx context.Context, consumerName, providerName string, selected bool) error
	ListRouteSelections(ctx context.Context, consumerName string) ([]string, error)
}

type peerService struct {
	logger          *log.Logger
	client          *resource.Client
	store           store.Store
	presence        *managementnats.NodePresenceStore
	licenseVerifier license.Verifier
	// netmapBuilder serves netmaps from the standalone DB registry when
	// no K8s client exists (client == nil).
	netmapBuilder *reconcilers.NetmapBuilder
}

const (
	displayNameAnnotation = "lattice.io/display-name"
	disabledAnnotation    = "lattice.io/disabled"
)

func (p *peerService) UpdatePeer(ctx context.Context, peerDto *dto.PeerDto) (*vo.PeerVo, error) {
	if p.netmapBuilder != nil {
		return p.updatePeerStandalone(ctx, peerDto)
	}
	var peer v1alpha1.LatticePeer
	if err := p.client.GetAPIReader().Get(ctx, types.NamespacedName{Namespace: peerDto.Namespace, Name: peerDto.Name}, &peer); err != nil {
		return nil, err
	}

	// Update labels
	peerLabels := peer.GetLabels()
	if peerLabels == nil {
		peerLabels = make(map[string]string)
	}
	if peerDto.Labels != nil {
		for k, v := range peerDto.Labels {
			if v == "" {
				delete(peerLabels, k)
			} else {
				peerLabels[k] = v
			}
		}
	}
	peer.SetLabels(peerLabels)

	// Update display name annotation
	annotations := peer.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	if peerDto.DisplayName != "" {
		annotations[displayNameAnnotation] = peerDto.DisplayName
	} else {
		delete(annotations, displayNameAnnotation)
	}
	peer.SetAnnotations(annotations)

	if err := p.client.Update(ctx, &peer); err != nil {
		return nil, err
	}

	return &vo.PeerVo{
		Name:        peer.Name,
		DisplayName: annotations[displayNameAnnotation],
		AppID:       peer.Spec.AppId,
		Labels:      peerLabels,
		PublicKey:   peer.Spec.PublicKey,
		Platform:    peer.Spec.Platform,
		Address:     peer.Status.AllocatedAddress,
	}, nil
}

type peerItem struct {
	name        string
	displayName string
	appId       string
	publicKey   string
	namespace   string
	address     *string
	labels      map[string]string
	disabled    bool
}

func (p *peerService) ListPeers(ctx context.Context, pageParam *dto.PageRequest) (*dto.PageResult[vo.PeerVo], error) {
	if p.client == nil {
		return p.listPeersStandalone(ctx, pageParam)
	}
	var (
		peerList v1alpha1.LatticePeerList
		err      error
	)

	workspaceV := ctx.Value(infra.WorkspaceKey)
	var workspaceId string
	if workspaceV != nil {
		workspaceId = workspaceV.(string)
	}

	workspace, err := p.store.Workspaces().GetByID(ctx, workspaceId)
	if err != nil {
		return nil, err
	}

	err = p.client.GetAPIReader().List(ctx, &peerList, client.InNamespace(workspace.Namespace))
	if err != nil {
		return nil, err
	}

	allPeers := make([]peerItem, 0, len(peerList.Items))
	for _, n := range peerList.Items {
		allPeers = append(allPeers, peerItem{
			name:        n.Name,
			displayName: n.GetAnnotations()[displayNameAnnotation],
			appId:       n.Spec.AppId,
			publicKey:   n.Spec.PublicKey,
			namespace:   n.Namespace,
			address:     n.Status.AllocatedAddress,
			labels:      n.GetLabels(),
			disabled:    n.GetAnnotations()[disabledAnnotation] == "true",
		})
	}

	return p.renderPeerPage(ctx, workspace, allPeers, pageParam)
}

// listPeersStandalone serves the peer list from the t_peer registry.
func (p *peerService) listPeersStandalone(ctx context.Context, pageParam *dto.PageRequest) (*dto.PageResult[vo.PeerVo], error) {
	workspaceV := ctx.Value(infra.WorkspaceKey)
	var workspaceId string
	if workspaceV != nil {
		workspaceId = workspaceV.(string)
	}

	workspace, err := p.store.Workspaces().GetByID(ctx, workspaceId)
	if err != nil {
		return nil, err
	}

	rows, err := p.store.Peers().ListByWorkspace(ctx, workspaceId)
	if err != nil {
		return nil, err
	}

	allPeers := make([]peerItem, 0, len(rows))
	for _, r := range rows {
		address := r.Address
		var labels map[string]string
		_ = json.Unmarshal([]byte(r.Labels), &labels)
		allPeers = append(allPeers, peerItem{
			name:        r.Name,
			displayName: r.Description,
			appId:       r.AppID,
			publicKey:   r.PublicKey,
			namespace:   workspace.Namespace,
			address:     &address,
			labels:      labels,
			disabled:    r.Disabled,
		})
	}

	return p.renderPeerPage(ctx, workspace, allPeers, pageParam)
}

// renderPeerPage applies keyword filtering, pagination and presence status
// to a peer list. Shared by the K8s and standalone code paths so both
// return identical VO shapes.
func (p *peerService) renderPeerPage(ctx context.Context, workspace *models.Workspace, allPeers []peerItem, pageParam *dto.PageRequest) (*dto.PageResult[vo.PeerVo], error) {
	filteredPeers := allPeers
	if pageParam.Keyword != "" {
		filteredPeers = filteredPeers[:0]
		kw := pageParam.Keyword
		for _, n := range allPeers {
			addrMatch := n.address != nil && strings.Contains(*n.address, kw)
			if strings.Contains(n.name, kw) || strings.Contains(n.displayName, kw) || addrMatch {
				filteredPeers = append(filteredPeers, n)
			}
		}
	}

	total := len(filteredPeers)
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

	var vos []vo.PeerVo
	for _, n := range filteredPeers[start:end] {
		pv := vo.PeerVo{
			Namespace:            n.namespace,
			Name:                 n.name,
			DisplayName:          n.displayName,
			AppID:                n.appId,
			PublicKey:            n.publicKey,
			Address:              n.address,
			Labels:               n.labels,
			WorkspaceDisplayName: workspace.DisplayName,
			Disabled:             n.disabled,
		}
		if p.presence != nil {
			status, lastSeen := p.presence.GetStatus(n.appId)
			pv.Status = status
			if lastSeen != nil {
				t := lastSeen.Format(time.RFC3339)
				pv.LastSeen = &t
			}
		}
		vos = append(vos, pv)
	}

	return &dto.PageResult[vo.PeerVo]{
		Page:     pageParam.Page,
		PageSize: pageParam.PageSize,
		Total:    int64(len(allPeers)),
		List:     vos,
	}, nil
}

func (p *peerService) CreateToken(ctx context.Context, tokenDto *dto.TokenDto) ([]byte, error) {
	if p.client == nil {
		return p.createTokenStandalone(ctx, tokenDto)
	}
	var token v1alpha1.LatticeEnrollmentToken
	if err := p.client.Get(ctx, client.ObjectKey{Namespace: tokenDto.Namespace, Name: tokenDto.Name}, &token); err != nil {
		if errors.IsNotFound(err) {
			duration, err := time.ParseDuration(tokenDto.Expiry)
			if err != nil {
				return nil, err
			}

			expiryTimestamp := time.Now().Add(duration).Unix()

			token = v1alpha1.LatticeEnrollmentToken{
				ObjectMeta: metav1.ObjectMeta{
					Name:      strings.ToLower(tokenDto.Name),
					Namespace: tokenDto.Namespace,
					Labels: map[string]string{
						"app.kubernetes.io/managed-by": "lattice-controller",
					},
				},
				Spec: v1alpha1.LatticeEnrollmentTokenSpec{
					Token:      tokenDto.Name,
					Namespace:  tokenDto.Namespace,
					Expiry:     metav1.NewTime(time.Unix(expiryTimestamp, 0)),
					UsageLimit: tokenDto.Limit,
				},
			}

			if err = p.client.Create(ctx, &token); err != nil {
				return nil, err
			}
		}
	}

	actualToken := token.Status.Token
	if actualToken == "" {
		actualToken = token.Spec.Token
	}
	return []byte(actualToken), nil
}

func NewPeerService(client *resource.Client, st store.Store, presence *managementnats.NodePresenceStore, verifier license.Verifier) PeerService {
	svc := &peerService{
		client:          client,
		logger:          log.GetLogger("peer-service"),
		store:           st,
		presence:        presence,
		licenseVerifier: verifier,
	}
	if client == nil && st != nil {
		// Standalone mode: build netmaps from the DB peer registry.
		svc.netmapBuilder = reconcilers.NewNetmapBuilder(st.Peers(), st.Policies(), st.PeerIdentities(), st.RouteSelections())
	}
	return svc
}

func (p *peerService) GetNetmap(ctx context.Context, token string, appId string) (*infra.Message, error) {
	if p.netmapBuilder != nil {
		return p.netmapBuilder.BuildForAppID(ctx, appId, token)
	}
	return p.client.GetNetworkMap(ctx, token, appId)
}

// registerStandalone is the DB-path registration: validate the workspace
// enrollment token, resume or create the t_peer record with a per-peer
// credential, and apply the license node limit for new peers.
func (p *peerService) registerStandalone(ctx context.Context, dto *dto.PeerDto) (*infra.Peer, error) {
	if dto.Token == "" {
		return nil, fmt.Errorf("token is empty")
	}
	tok, err := p.store.EnrollmentTokens().GetByToken(ctx, dto.Token)
	if err != nil {
		return nil, fmt.Errorf("token not exists")
	}
	if time.Now().After(tok.ExpiresAt) {
		return nil, fmt.Errorf("token is expired")
	}
	// Re-registration always resumes, regardless of the usage limit.
	existing, existingErr := p.store.Peers().GetByAppID(ctx, dto.AppID)
	if existingErr != nil && !stderrors.Is(existingErr, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if existingErr == nil && existing.WorkspaceID != tok.WorkspaceID {
		return nil, fmt.Errorf("peer %q is bound to another workspace", dto.AppID)
	}
	if existingErr != nil {
		if limitErr := p.checkNodeLimitStandalone(ctx); limitErr != nil {
			return nil, limitErr
		}
	}
	if tok.UsageLimit > 0 && existingErr != nil && tok.UsedCount >= tok.UsageLimit {
		return nil, fmt.Errorf("token usage limit reached (%d)", tok.UsageLimit)
	}
	if incErr := p.store.EnrollmentTokens().IncrementUsedCount(ctx, tok.ID); incErr != nil {
		return nil, incErr
	}

	peer := existing
	if peer == nil {
		rows, listErr := p.store.Peers().ListByWorkspace(ctx, tok.WorkspaceID)
		if listErr != nil {
			return nil, listErr
		}
		taken := make([]string, 0, len(rows))
		for _, r := range rows {
			taken = append(taken, r.Address)
		}
		address, allocErr := reconcilers.AllocateAddress(taken)
		if allocErr != nil {
			return nil, allocErr
		}
		peer = &models.Peer{
			WorkspaceID: tok.WorkspaceID,
			Name:        cmp.Or(dto.Name, dto.AppID), // agents may register without a display name
			AppID:       dto.AppID,
			Token:       dto.Token, // K8s semantics: the agent polls GetNetMap with its enrollment token
			Address:     address,
		}
	}
	// The control plane owns the WireGuard keypair (same as the K8s path):
	// generate on first enrollment, reuse on re-registration.
	var key wgtypes.Key
	if peer.PrivateKey != "" {
		key, err = wgtypes.ParseKey(peer.PrivateKey)
		if err != nil {
			return nil, fmt.Errorf("parse stored key: %w", err)
		}
	} else {
		key, err = wgtypes.GeneratePrivateKey()
		if err != nil {
			return nil, fmt.Errorf("generate key: %w", err)
		}
		peer.PrivateKey = key.String()
	}
	peer.PublicKey = key.PublicKey().String()
	peer.Endpoint = dto.Endpoint
	peer.Hostname = dto.Hostname
	peer.Platform = dto.Platform
	if peer.Name == "" {
		peer.Name = cmp.Or(dto.Name, dto.AppID) // backfill legacy registrations
	}
	now := time.Now()
	peer.LastSeenAt = &now
	if err := p.store.Peers().Update(ctx, peer); err != nil {
		return nil, err
	}

	address := peer.Address
	node := &infra.Peer{
		Name:       peer.Name,
		AppID:      peer.AppID,
		Address:    &address,
		Token:      peer.Token,
		PrivateKey: peer.PrivateKey,
		PublicKey:  peer.PublicKey,
		Endpoint:   peer.Endpoint,
		Hostname:   peer.Hostname,
		Platform:   peer.Platform,
		NetworkId:  peer.WorkspaceID,
	}

	// Look up enforcer_mode from the workspace owner's profile (best effort,
	// same as the K8s path).
	if workspace, wsErr := p.store.Workspaces().GetByID(ctx, tok.WorkspaceID); wsErr == nil && workspace.CreatedBy != "" {
		if profile, profErr := p.store.Profiles().Get(ctx, workspace.CreatedBy); profErr == nil && profile.EnforcerMode != "" {
			node.EnforcerMode = profile.EnforcerMode
		}
	}
	return node, nil
}

// checkNodeLimitStandalone counts registered peers across the whole
// deployment against the license's MaxNodes (Community: no restriction).
func (p *peerService) checkNodeLimitStandalone(ctx context.Context) error {
	lic, status, _ := p.licenseVerifier.Verify()
	if status != license.StatusValid || lic == nil || lic.Limits.MaxNodes <= 0 {
		return nil
	}
	count, err := p.store.Peers().CountAll(ctx)
	if err != nil {
		return fmt.Errorf("check node limit: %w", err)
	}
	if count >= int64(lic.Limits.MaxNodes) {
		return fmt.Errorf("node limit reached (%d/%d) — upgrade at https://alattice.io/pro",
			count, lic.Limits.MaxNodes)
	}
	return nil
}

func (p *peerService) UpdateStatus(_ context.Context, _ int) error { return nil }

func (p *peerService) DisablePeer(ctx context.Context, namespace, name string) error {
	if p.netmapBuilder != nil {
		return p.setPeerDisabledStandalone(ctx, name, true)
	}
	var peer v1alpha1.LatticePeer
	if err := p.client.GetAPIReader().Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &peer); err != nil {
		return err
	}
	annotations := peer.GetAnnotations()
	if annotations == nil {
		annotations = make(map[string]string)
	}
	annotations[disabledAnnotation] = "true"
	peer.SetAnnotations(annotations)
	return p.client.Update(ctx, &peer)
}

func (p *peerService) EnablePeer(ctx context.Context, namespace, name string) error {
	if p.netmapBuilder != nil {
		return p.setPeerDisabledStandalone(ctx, name, false)
	}
	var peer v1alpha1.LatticePeer
	if err := p.client.GetAPIReader().Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &peer); err != nil {
		return err
	}
	annotations := peer.GetAnnotations()
	delete(annotations, disabledAnnotation)
	peer.SetAnnotations(annotations)
	return p.client.Update(ctx, &peer)
}

func (p *peerService) DeletePeer(ctx context.Context, namespace, name string) error {
	if p.netmapBuilder != nil {
		peer, err := p.standalonePeerByName(ctx, name)
		if err != nil {
			return err
		}
		return p.store.Peers().Delete(ctx, peer.ID)
	}
	var peer v1alpha1.LatticePeer
	if err := p.client.GetAPIReader().Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &peer); err != nil {
		return err
	}
	if err := p.client.Delete(ctx, &peer); err != nil {
		return err
	}
	// Best-effort cleanup of the associated ConfigMap created by the controller
	var cm corev1.ConfigMap
	if err := p.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: fmt.Sprintf("%s-config", name)}, &cm); err == nil {
		_ = p.client.Delete(ctx, &cm)
	}
	return nil
}

// standalonePeerByName finds a t_peer row by name within the workspace
// carried on ctx (standalone mode has no K8s namespace indirection).
func (p *peerService) standalonePeerByName(ctx context.Context, name string) (*models.Peer, error) {
	workspaceID, _ := ctx.Value(infra.WorkspaceKey).(string)
	rows, err := p.store.Peers().ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Name == name {
			return r, nil
		}
	}
	return nil, fmt.Errorf("peer %q not found", name)
}

// updatePeerStandalone applies display-name and label changes to the t_peer
// registry row. The peer's Name is its WG identity and never changes.
func (p *peerService) updatePeerStandalone(ctx context.Context, peerDto *dto.PeerDto) (*vo.PeerVo, error) {
	peer, err := p.standalonePeerByName(ctx, peerDto.Name)
	if err != nil {
		return nil, err
	}
	if peerDto.DisplayName != "" {
		peer.Description = peerDto.DisplayName
	}
	if peerDto.Labels != nil {
		filtered := make(map[string]string, len(peerDto.Labels))
		for k, v := range peerDto.Labels {
			if v != "" {
				filtered[k] = v
			}
		}
		if blob, jerr := json.Marshal(filtered); jerr == nil {
			peer.Labels = string(blob)
		}
	}
	if err := p.store.Peers().Update(ctx, peer); err != nil {
		return nil, err
	}
	var labels map[string]string
	_ = json.Unmarshal([]byte(peer.Labels), &labels)
	address := peer.Address
	return &vo.PeerVo{
		Name:        peer.Name,
		DisplayName: peer.Description,
		AppID:       peer.AppID,
		Labels:      labels,
		PublicKey:   peer.PublicKey,
		Platform:    peer.Platform,
		Address:     &address,
		Disabled:    peer.Disabled,
	}, nil
}

// setPeerDisabledStandalone toggles the t_peer Disabled flag. Disabled peers
// are excluded from netmaps by the builder, so agents stop dialing them and
// their own netmap requests come back empty.
func (p *peerService) setPeerDisabledStandalone(ctx context.Context, name string, disabled bool) error {
	peer, err := p.standalonePeerByName(ctx, name)
	if err != nil {
		return err
	}
	peer.Disabled = disabled
	return p.store.Peers().Update(ctx, peer)
}

// SetAdvertisedRoutes declares (or clears, if routes is empty) the CIDRs
// this peer offers to route for other peers. Standalone only for now —
// K8s mode routes through LatticeNetworkPeering instead (out of scope,
// see docs/superpowers/specs/2026-09-14-exit-node-subnet-route-design.md).
func (p *peerService) SetAdvertisedRoutes(ctx context.Context, name string, routes []string) error {
	if p.netmapBuilder == nil {
		return stderrors.New("advertised routes are not supported in K8s mode yet")
	}
	peer, err := p.standalonePeerByName(ctx, name)
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		peer.AdvertisedRoutes = ""
	} else {
		blob, err := json.Marshal(routes)
		if err != nil {
			return fmt.Errorf("marshal advertised routes: %w", err)
		}
		peer.AdvertisedRoutes = string(blob)
	}
	return p.store.Peers().Update(ctx, peer)
}

// SetRouteSelection opts consumerName in (selected=true) or out
// (selected=false) of providerName's advertised routes. A peer cannot
// select itself.
func (p *peerService) SetRouteSelection(ctx context.Context, consumerName, providerName string, selected bool) error {
	if p.netmapBuilder == nil {
		return stderrors.New("route selection is not supported in K8s mode yet")
	}
	if consumerName == providerName {
		return stderrors.New("a peer cannot select its own advertised routes")
	}
	consumer, err := p.standalonePeerByName(ctx, consumerName)
	if err != nil {
		return err
	}
	provider, err := p.standalonePeerByName(ctx, providerName)
	if err != nil {
		return err
	}
	if !selected {
		return p.store.RouteSelections().Delete(ctx, consumer.WorkspaceID, consumer.ID, provider.ID)
	}
	return p.store.RouteSelections().Create(ctx, &models.PeerRouteSelection{
		WorkspaceID:    consumer.WorkspaceID,
		ConsumerPeerID: consumer.ID,
		ProviderPeerID: provider.ID,
	})
}

// ListRouteSelections returns the names (not IDs) of providers
// consumerName has currently opted into.
func (p *peerService) ListRouteSelections(ctx context.Context, consumerName string) ([]string, error) {
	if p.netmapBuilder == nil {
		return nil, stderrors.New("route selection is not supported in K8s mode yet")
	}
	consumer, err := p.standalonePeerByName(ctx, consumerName)
	if err != nil {
		return nil, err
	}
	providerIDs, err := p.store.RouteSelections().ListProviderIDsForConsumer(ctx, consumer.WorkspaceID, consumer.ID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(providerIDs))
	for _, id := range providerIDs {
		provider, err := p.store.Peers().GetByID(ctx, id)
		if err != nil {
			continue // provider was deleted since selecting; skip rather than fail the whole list
		}
		names = append(names, provider.Name)
	}
	return names, nil
}

func (p *peerService) Register(ctx context.Context, dto *dto.PeerDto) (*infra.Peer, error) {
	p.logger.Info("Received peer", "info", dto)

	if p.netmapBuilder != nil {
		return p.registerStandalone(ctx, dto)
	}

	tokenValid, token, err := p.checkToken(ctx, dto.Token)
	if err != nil {
		return nil, err
	}

	if !tokenValid {
		return nil, fmt.Errorf("token is invalid")
	}

	// Enforce license node limit for new peers (re-registration is always allowed).
	if err = p.checkNodeLimit(ctx, token.Namespace, dto.AppID); err != nil {
		return nil, err
	}

	node, err := p.client.Register(ctx, token.Namespace, dto)
	if err != nil {
		return nil, err
	}

	// Look up user enforcer_mode from workspace owner's profile.
	workspace, wsErr := p.store.Workspaces().GetByNamespace(ctx, token.Namespace)
	if wsErr == nil && workspace.CreatedBy != "" {
		profile, profErr := p.store.Profiles().Get(ctx, workspace.CreatedBy)
		if profErr == nil && profile.EnforcerMode != "" {
			node.EnforcerMode = profile.EnforcerMode
		}
	}

	actualToken := token.Status.Token
	if actualToken == "" {
		actualToken = token.Spec.Token
	}
	node.Token = actualToken
	return node, nil
}

func (p *peerService) checkToken(ctx context.Context, tokenStr string) (bool, *v1alpha1.LatticeEnrollmentToken, error) {
	if tokenStr == "" {
		return false, nil, fmt.Errorf("token is empty")
	}

	var list v1alpha1.LatticeEnrollmentTokenList
	err := p.client.List(ctx, &list, client.MatchingFields{"status.token": tokenStr})
	if err != nil {
		return false, nil, fmt.Errorf("get token failed: %v", err)
	}
	if len(list.Items) == 0 {
		// Backward compatibility with old data: fall back to spec.token
		err = p.client.List(ctx, &list, client.MatchingFields{"spec.token": tokenStr})
		if err != nil {
			return false, nil, fmt.Errorf("get token failed: %v", err)
		}
	}

	if len(list.Items) == 0 {
		return false, nil, fmt.Errorf("token not exists")
	}

	var token *v1alpha1.LatticeEnrollmentToken
	for _, t := range list.Items {
		if t.Status.Token == tokenStr || t.Spec.Token == tokenStr {
			token = &t
		}
	}

	if token == nil {
		return false, nil, fmt.Errorf("token not exists")
	}

	if err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latestToken := &v1alpha1.LatticeEnrollmentToken{}
		if err = p.client.GetCache().Get(ctx, client.ObjectKeyFromObject(token), latestToken); err != nil {
			return err
		}
		latestToken.Status.UsedCount++
		return p.client.Status().Update(ctx, latestToken)
	}); err != nil {
		return false, nil, err
	}

	return true, token, nil
}

func (p *peerService) bootstrap(ctx context.Context, nsName string) error {
	if err := p.ensureNamespace(ctx, nsName); err != nil {
		return err
	}
	return p.ensureDefaultNetwork(ctx, nsName)
}

func (p *peerService) ensureNamespace(ctx context.Context, nsName string) error {
	var ns corev1.Namespace
	if err := p.client.Get(ctx, client.ObjectKey{Name: nsName}, &ns); err != nil {
		if errors.IsNotFound(err) {
			p.logger.Info("Creating namespace", "name", nsName)
			if err = p.client.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name:   nsName,
					Labels: map[string]string{"app.kubernetes.io/managed-by": "lattice-controller"},
				},
			}); err != nil {
				p.logger.Error("create namespace failed", err)
			}
		}
	}
	return nil
}

// checkNodeLimit returns an error if the license's MaxNodes limit would be exceeded
// by registering a new peer. Re-registration of an existing peer is always allowed.
func (p *peerService) checkNodeLimit(ctx context.Context, namespace, appID string) error {
	lic, status, _ := p.licenseVerifier.Verify()
	if status != license.StatusValid || lic == nil || lic.Limits.MaxNodes <= 0 {
		// Community (no license) or unlimited license: no restriction.
		return nil
	}

	// Allow re-registration of an existing peer without counting against the limit.
	var existing v1alpha1.LatticePeer
	if err := p.client.GetAPIReader().Get(ctx, types.NamespacedName{Namespace: namespace, Name: appID}, &existing); err == nil {
		return nil
	}

	var peerList v1alpha1.LatticePeerList
	if err := p.client.GetAPIReader().List(ctx, &peerList); err != nil {
		return fmt.Errorf("check node limit: %w", err)
	}

	if len(peerList.Items) >= lic.Limits.MaxNodes {
		return fmt.Errorf("node limit reached (%d/%d) — upgrade at https://alattice.io/pro",
			len(peerList.Items), lic.Limits.MaxNodes)
	}
	return nil
}

func (p *peerService) ensureDefaultNetwork(ctx context.Context, nsName string) error {
	var defaultNet v1alpha1.LatticeNetwork
	if err := p.client.Get(ctx, client.ObjectKey{Namespace: nsName, Name: "lattice-default-net"}, &defaultNet); err != nil {
		if errors.IsNotFound(err) {
			defaultNet = v1alpha1.LatticeNetwork{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "lattice-default-net",
					Namespace: nsName,
					Labels:    map[string]string{"app.kubernetes.io/managed-by": "lattice-controller"},
				},
				Spec: v1alpha1.LatticeNetworkSpec{
					Name: fmt.Sprintf("%s-net", nsName),
				},
			}

			if err := p.client.Create(ctx, &defaultNet); err != nil {
				return fmt.Errorf("failed to create default network: %v", err)
			}
		}
	}
	return nil
}

// createTokenStandalone persists the workspace enrollment token in the
// database (the DB equivalent of the LatticeEnrollmentToken CRD).
func (p *peerService) createTokenStandalone(ctx context.Context, tokenDto *dto.TokenDto) ([]byte, error) {
	if existing, err := p.store.EnrollmentTokens().GetByToken(ctx, tokenDto.Name); err == nil {
		return []byte(existing.Token), nil // idempotent create
	}
	duration, err := time.ParseDuration(tokenDto.Expiry)
	if err != nil {
		return nil, fmt.Errorf("parse expiry: %w", err)
	}
	ws, err := p.store.Workspaces().GetByNamespace(ctx, tokenDto.Namespace)
	if err != nil {
		return nil, fmt.Errorf("workspace not found: %w", err)
	}
	tok := &models.EnrollmentToken{
		Token:       tokenDto.Name,
		WorkspaceID: ws.ID,
		ExpiresAt:   time.Now().Add(duration),
		UsageLimit:  tokenDto.Limit,
	}
	if err := p.store.EnrollmentTokens().Create(ctx, tok); err != nil {
		return nil, err
	}
	return []byte(tok.Token), nil
}

// FlowStats aggregates observed traffic for every agent in the workspace
// over the given window (policy hit/traffic statistics v1).
func (p *peerService) FlowStats(ctx context.Context, workspaceID string, days int) (*vo.FlowStatsVo, error) {
	if days <= 0 {
		days = 7
	}
	rows, err := p.store.Peers().ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	agentIDs := make([]string, 0, len(rows))
	nameByAgent := map[string]string{}
	for _, r := range rows {
		if r.AppID == "" {
			continue
		}
		agentIDs = append(agentIDs, r.AppID)
		nameByAgent[r.AppID] = r.Name
	}

	out := vo.FlowStatsVo{
		WorkspaceID: workspaceID,
		Since:       time.Now().AddDate(0, 0, -days).Format(time.RFC3339),
		Days:        days,
		PerAgent:    []vo.FlowAgentStats{},
	}
	if len(agentIDs) == 0 {
		return &out, nil
	}
	flows, totalBytes, err := p.store.FlowEvents().SumByAgents(ctx, agentIDs, time.Now().AddDate(0, 0, -days))
	if err != nil {
		return nil, err
	}
	out.TotalFlows = flows
	out.TotalBytes = totalBytes
	for _, id := range agentIDs {
		c, b, err := p.store.FlowEvents().SumByAgents(ctx, []string{id}, time.Now().AddDate(0, 0, -days))
		if err != nil {
			continue
		}
		out.PerAgent = append(out.PerAgent, vo.FlowAgentStats{AgentID: id, Name: nameByAgent[id], Flows: c, Bytes: b})
	}
	return &out, nil
}

// PolicyDeliveryStatus compares the workspace's expected netmap version
// against what each node reports via heartbeat ("已下发 x/y 节点" 数据源).
func (p *peerService) PolicyDeliveryStatus(ctx context.Context, workspaceID string) (*vo.PolicyDeliveryStatusVo, error) {
	if p.netmapBuilder == nil {
		return nil, fmt.Errorf("policy delivery status requires standalone mode")
	}

	rows, err := p.store.Peers().ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}

	out := vo.PolicyDeliveryStatusVo{Peers: []vo.PolicyDeliveryStatusPeer{}}
	converged := 0
	for _, r := range rows {
		if r.Address == "" || r.Disabled {
			continue
		}
		expected := ""
		if msg, buildErr := p.netmapBuilder.BuildForPeer(ctx, r); buildErr == nil {
			expected = msg.ConfigVersion
		}
		applied := ""
		if p.presence != nil {
			applied = p.presence.GetVersion(r.AppID)
		}
		isConverged := applied != "" && applied == expected
		if isConverged {
			converged++
		}
		out.Peers = append(out.Peers, vo.PolicyDeliveryStatusPeer{
			Name:           r.Name,
			Address:        r.Address,
			AppliedVersion: applied,
			Converged:      isConverged,
		})
	}
	out.Total = len(out.Peers)
	out.ConvergedCount = converged
	out.Converged = converged == out.Total && out.Total > 0
	return &out, nil
}
