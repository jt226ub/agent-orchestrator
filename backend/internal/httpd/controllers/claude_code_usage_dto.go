package controllers

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

func newClaudeCodeUsageResponse(input domain.ClaudeCodePlanUsageSnapshot) ClaudeCodeUsageResponse {
	windows := make([]ClaudeCodeUsageWindowResponse, len(input.Windows))
	for i := range input.Windows {
		windows[i] = ClaudeCodeUsageWindowResponse{ID: input.Windows[i].ID, DisplayName: input.Windows[i].DisplayName, UsedPercent: input.Windows[i].UsedPercent, ResetsAt: input.Windows[i].ResetsAt}
	}
	response := ClaudeCodeUsageResponse{
		State: string(input.State), Freshness: string(input.Freshness), Plan: input.Plan,
		RemainingPercent: input.RemainingPercent, Windows: windows,
		ObservedAt: input.ObservedAt, CheckedAt: input.CheckedAt, AttemptedAt: input.AttemptedAt,
		ReasonCode: input.ReasonCode, Reason: input.Reason,
	}
	if input.Identity != nil {
		response.Identity = &ClaudeCodeUsageIdentityResponse{EmailAddress: input.Identity.EmailAddress, DisplayName: input.Identity.DisplayName, OrganizationName: input.Identity.OrganizationName}
	}
	if input.Promotion != nil {
		response.Promotion = &ClaudeCodeUsagePromotionResponse{PercentIncrease: input.Promotion.PercentIncrease, EndsOn: input.Promotion.EndsOn}
	}
	return response
}
