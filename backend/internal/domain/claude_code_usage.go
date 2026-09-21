package domain

import "time"

// ClaudeCodePlanUsageState classifies whether the signed-in Claude Code
// subscription's limits can be shown. Usage is advisory: it is displayed
// beside the Codex and Antigravity subscriptions and never gates a launch.
type ClaudeCodePlanUsageState string

// Claude Code plan-usage states.
const (
	ClaudeCodePlanUsageAvailable   ClaudeCodePlanUsageState = "available"
	ClaudeCodePlanUsageUnknown     ClaudeCodePlanUsageState = "unknown"
	ClaudeCodePlanUsageUnsupported ClaudeCodePlanUsageState = "unsupported"
)

// ClaudeCodePlanUsageWindow is one provider-reported subscription limit.
type ClaudeCodePlanUsageWindow struct {
	ID          string     `json:"id"`
	DisplayName string     `json:"displayName"`
	UsedPercent float64    `json:"usedPercent" minimum:"0" maximum:"100"`
	ResetsAt    *time.Time `json:"resetsAt,omitempty"`
}

// ClaudeCodePlanPromotion is a display-safe active subscription promotion.
type ClaudeCodePlanPromotion struct {
	PercentIncrease int    `json:"percentIncrease" minimum:"1"`
	EndsOn          string `json:"endsOn"`
}

// ClaudeCodePlanUsageIdentity is the allowlisted non-secret identity of the
// device's signed-in Claude Code account, read from Claude's own config.
type ClaudeCodePlanUsageIdentity struct {
	EmailAddress     string `json:"emailAddress,omitempty"`
	DisplayName      string `json:"displayName,omitempty"`
	OrganizationName string `json:"organizationName,omitempty"`
}

// ClaudeCodePlanUsageSnapshot is the display-safe cached usage state for the
// device's signed-in Claude Code subscription. Tokens never appear here.
type ClaudeCodePlanUsageSnapshot struct {
	State            ClaudeCodePlanUsageState     `json:"state" enum:"available,unknown,unsupported"`
	Freshness        AgentReadinessFreshness      `json:"freshness" enum:"fresh,stale,checking"`
	Plan             *string                      `json:"plan,omitempty"`
	Identity         *ClaudeCodePlanUsageIdentity `json:"identity,omitempty"`
	Promotion        *ClaudeCodePlanPromotion     `json:"promotion,omitempty"`
	RemainingPercent *float64                     `json:"remainingPercent,omitempty" minimum:"0" maximum:"100"`
	Windows          []ClaudeCodePlanUsageWindow  `json:"windows"`
	ObservedAt       *time.Time                   `json:"observedAt,omitempty"`
	CheckedAt        *time.Time                   `json:"checkedAt,omitempty"`
	AttemptedAt      *time.Time                   `json:"attemptedAt,omitempty"`
	ReasonCode       string                       `json:"reasonCode"`
	Reason           string                       `json:"reason"`
}

// Claude Code plan-usage reason codes are stable, display-safe explanations.
const (
	ClaudeCodePlanUsageReasonNotChecked      = "plan_usage_not_checked"
	ClaudeCodePlanUsageReasonChecking        = "plan_usage_checking"
	ClaudeCodePlanUsageReasonAvailable       = "plan_usage_available"
	ClaudeCodePlanUsageReasonSignedOut       = "plan_usage_signed_out"
	ClaudeCodePlanUsageReasonUnavailable     = "plan_usage_unavailable"
	ClaudeCodePlanUsageReasonRateLimited     = "plan_usage_rate_limited"
	ClaudeCodePlanUsageReasonUnsupported     = "plan_usage_unsupported"
	ClaudeCodePlanUsageReasonInvalidResponse = "plan_usage_invalid_response"
	ClaudeCodePlanUsageReasonCheckTimeout    = "plan_usage_check_timeout"
	ClaudeCodePlanUsageReasonCheckStopped    = "plan_usage_check_stopped"
)
