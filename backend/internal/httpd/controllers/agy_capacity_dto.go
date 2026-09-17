package controllers

import "github.com/aoagents/agent-orchestrator/backend/internal/domain"

func newAgyCapacityResponse(input domain.AgyCapacitySnapshot) AgyCapacityResponse {
	additional := make([]AgyCapacityBucketResponse, len(input.AdditionalBuckets))
	for i := range input.AdditionalBuckets {
		additional[i] = newAgyCapacityBucketResponse(input.AdditionalBuckets[i])
	}
	response := AgyCapacityResponse{
		State: string(input.State), Freshness: string(input.Freshness),
		UsedPercent: input.UsedPercent, RemainingPercent: input.RemainingPercent,
		ResetsAt: input.ResetsAt, ObservedAt: input.ObservedAt, CheckedAt: input.CheckedAt, AttemptedAt: input.AttemptedAt,
		ReasonCode: input.ReasonCode, Reason: input.Reason, AdditionalBuckets: additional,
	}
	if input.Overall != nil {
		overall := newAgyCapacityBucketResponse(*input.Overall)
		response.Overall = &overall
	}
	return response
}

func newAgyCapacityBucketResponse(input domain.AgyCapacityBucket) AgyCapacityBucketResponse {
	response := AgyCapacityBucketResponse{DisplayName: input.DisplayName}
	if input.Primary != nil {
		response.Primary = &AgyCapacityWindowResponse{
			UsedPercent: input.Primary.UsedPercent, WindowDurationMinutes: input.Primary.WindowDurationMinutes, ResetsAt: input.Primary.ResetsAt,
		}
	}
	if input.Secondary != nil {
		response.Secondary = &AgyCapacityWindowResponse{
			UsedPercent: input.Secondary.UsedPercent, WindowDurationMinutes: input.Secondary.WindowDurationMinutes, ResetsAt: input.Secondary.ResetsAt,
		}
	}
	return response
}
