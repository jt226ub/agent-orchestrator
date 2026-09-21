package ports

import (
	"context"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Safe Claude Code usage failures cross the adapter/service boundary without
// retaining credential material or raw provider responses.
var (
	ErrClaudeCodePlanUsageUnavailable = errors.New("plan usage unavailable for Claude Code")
	ErrClaudeCodePlanUsageRateLimited = errors.New("plan usage rate limited for Claude Code")
	ErrClaudeCodePlanUsageInvalid     = errors.New("plan usage response invalid for Claude Code")
	ErrClaudeCodePlanUsageSignedOut   = errors.New("plan usage signed out for Claude Code")
	ErrClaudeCodePlanUsageUnsupported = errors.New("plan usage unsupported for Claude Code")
)

// ClaudeCodePlanUsageObservation is the provider usage response before the
// daemon-owned coordinator applies cache freshness and display reasons.
type ClaudeCodePlanUsageObservation struct {
	ObservedAt time.Time
	Plan       *string
	Identity   *domain.ClaudeCodePlanUsageIdentity
	Promotion  *domain.ClaudeCodePlanPromotion
	Windows    []domain.ClaudeCodePlanUsageWindow
}

// ClaudeCodeUsageReader is implemented by the Claude Code adapter: it reads
// the device's signed-in subscription limits with the CLI's own credential.
type ClaudeCodeUsageReader interface {
	ReadPlanUsage(ctx context.Context) (ClaudeCodePlanUsageObservation, error)
}
