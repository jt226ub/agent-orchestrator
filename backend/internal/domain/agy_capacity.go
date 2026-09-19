package domain

import "time"

// AgyCapacityState is the display-safe plan-capacity classification for the
// Antigravity CLI's signed-in Google account. Capacity is advisory, matching
// Codex capacity, unless a role profile opts into admission with a Quota.
type AgyCapacityState string

// Agy capacity states classify the provider-reported overall bucket.
const (
	AgyCapacityAvailable   AgyCapacityState = "available"
	AgyCapacityNearLimit   AgyCapacityState = "near_limit"
	AgyCapacityExhausted   AgyCapacityState = "exhausted"
	AgyCapacityUnknown     AgyCapacityState = "unknown"
	AgyCapacityUnsupported AgyCapacityState = "unsupported"
)

// AgyCapacityWindow is one plan rate-limit window as the CLI reports it.
type AgyCapacityWindow struct {
	UsedPercent           float64    `json:"usedPercent" minimum:"0" maximum:"100"`
	WindowDurationMinutes *int64     `json:"windowDurationMinutes,omitempty"`
	ResetsAt              *time.Time `json:"resetsAt,omitempty"`
}

// AgyCapacityBucket is one model group's meter: the CLI shares a 5-hour and a
// weekly limit between every model in a group.
type AgyCapacityBucket struct {
	LimitID     string             `json:"limitId"`
	DisplayName *string            `json:"displayName,omitempty"`
	Primary     *AgyCapacityWindow `json:"primary,omitempty"`
	Secondary   *AgyCapacityWindow `json:"secondary,omitempty"`
}

// AgyCapacitySnapshot is the daemon-memory capacity observation for the
// Antigravity CLI. Raw provider payloads are deliberately absent.
type AgyCapacitySnapshot struct {
	State             AgyCapacityState        `json:"state" enum:"available,near_limit,exhausted,unknown,unsupported"`
	Freshness         AgentReadinessFreshness `json:"freshness" enum:"fresh,stale,checking"`
	UsedPercent       *float64                `json:"usedPercent,omitempty" minimum:"0" maximum:"100"`
	RemainingPercent  *float64                `json:"remainingPercent,omitempty" minimum:"0" maximum:"100"`
	ResetsAt          *time.Time              `json:"resetsAt,omitempty"`
	ObservedAt        *time.Time              `json:"observedAt,omitempty"`
	CheckedAt         *time.Time              `json:"checkedAt,omitempty"`
	AttemptedAt       *time.Time              `json:"attemptedAt,omitempty"`
	ReasonCode        string                  `json:"reasonCode"`
	Reason            string                  `json:"reason"`
	Overall           *AgyCapacityBucket      `json:"overall,omitempty"`
	AdditionalBuckets []AgyCapacityBucket     `json:"additionalBuckets"`
}

// Agy capacity reason codes are stable, display-safe explanations.
const (
	AgyCapacityReasonNotChecked        = "capacity_not_checked"
	AgyCapacityReasonChecking          = "capacity_checking"
	AgyCapacityReasonAvailable         = "capacity_available"
	AgyCapacityReasonNearLimit         = "capacity_near_limit"
	AgyCapacityReasonExhausted         = "capacity_exhausted"
	AgyCapacityReasonUnsupported       = "capacity_unsupported"
	AgyCapacityReasonNotInstalled      = "capacity_not_installed"
	AgyCapacityReasonSkippedSignedOut  = "capacity_skipped_signed_out"
	AgyCapacityReasonCheckInconclusive = "capacity_check_inconclusive"
	AgyCapacityReasonCheckTimeout      = "capacity_check_timeout"
	AgyCapacityReasonCheckFailed       = "capacity_check_failed"
	AgyCapacityReasonProviderRejected  = "capacity_provider_rejected"
	AgyCapacityReasonCheckStopped      = "capacity_check_stopped"
)
