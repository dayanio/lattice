package controller

import (
	"context"

	"github.com/alatticeio/lattice/internal/agent/store"
	"github.com/alatticeio/lattice/internal/server/dto"
	"github.com/alatticeio/lattice/internal/server/models"
	"github.com/alatticeio/lattice/internal/server/resource"
	"github.com/alatticeio/lattice/internal/server/service"
	"github.com/alatticeio/lattice/internal/server/vo"
)

type PolicyController interface {
	ListPolicy(ctx context.Context, pageParam *dto.PageRequest) (*dto.PageResult[vo.PolicyVo], error)
	Submit(ctx context.Context, wsID, createdBy, createdByName string, policyDto *dto.PolicyDto) (*models.Policy, error)
	ApplyDirect(ctx context.Context, wsID, operatorID, operatorName string, policyDto *dto.PolicyDto) (*vo.PolicyVo, error)
	Apply(ctx context.Context, policyID string) error
	DeletePolicy(ctx context.Context, name string) error
	// PreviewPolicy computes the deterministic per-peer effect of a draft
	// policy without persisting anything.
	PreviewPolicy(ctx context.Context, wsID string, draft *dto.PolicyDto) (*vo.PolicyPreviewVo, error)
	// Translate converts a natural-language description into a draft spec.
	Translate(ctx context.Context, wsID, description string) (*vo.PolicyTranslationVo, error)
	ExportPolicies(ctx context.Context, wsID string) (string, error)
	ImportPolicies(ctx context.Context, wsID, content string, dryRun bool, operatorID, operatorName string) (*vo.PolicyImportVo, error)
}

type policyController struct {
	policyService   service.PolicyService
	policyIntentSvc service.PolicyIntentService
}

func (p *policyController) ListPolicy(ctx context.Context, pageParam *dto.PageRequest) (*dto.PageResult[vo.PolicyVo], error) {
	return p.policyService.ListPolicy(ctx, pageParam)
}

func (p *policyController) Submit(ctx context.Context, wsID, createdBy, createdByName string, policyDto *dto.PolicyDto) (*models.Policy, error) {
	return p.policyService.Submit(ctx, wsID, createdBy, createdByName, policyDto)
}

func (p *policyController) ApplyDirect(ctx context.Context, wsID, operatorID, operatorName string, policyDto *dto.PolicyDto) (*vo.PolicyVo, error) {
	return p.policyService.ApplyDirect(ctx, wsID, operatorID, operatorName, policyDto)
}

func (p *policyController) Apply(ctx context.Context, policyID string) error {
	return p.policyService.Apply(ctx, policyID)
}

func (p *policyController) DeletePolicy(ctx context.Context, name string) error {
	return p.policyService.DeletePolicy(ctx, name)
}

func (p *policyController) PreviewPolicy(ctx context.Context, wsID string, draft *dto.PolicyDto) (*vo.PolicyPreviewVo, error) {
	return p.policyService.PreviewPolicy(ctx, wsID, *draft)
}

func (p *policyController) Translate(ctx context.Context, wsID, description string) (*vo.PolicyTranslationVo, error) {
	return p.policyIntentSvc.Translate(ctx, wsID, description)
}

func (p *policyController) ExportPolicies(ctx context.Context, wsID string) (string, error) {
	return p.policyService.ExportPolicies(ctx, wsID)
}

func (p *policyController) ImportPolicies(ctx context.Context, wsID, content string, dryRun bool, operatorID, operatorName string) (*vo.PolicyImportVo, error) {
	return p.policyService.ImportPolicies(ctx, wsID, content, dryRun, operatorID, operatorName)
}

func NewPolicyController(client *resource.Client, st store.Store, policyIntentSvc service.PolicyIntentService) PolicyController {
	return &policyController{
		policyService:   service.NewPolicyService(client, st),
		policyIntentSvc: policyIntentSvc,
	}
}
