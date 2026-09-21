package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	claudeCodeUsageDisplayTTL  = 2 * time.Minute
	claudeCodeUsageReadTimeout = 15 * time.Second
)

type claudeCodeUsageReadCall struct {
	done              chan struct{}
	startedReceivedAt time.Time
}

// claudeCodeUsageCoordinator caches the device's Claude Code subscription
// limits for display. It mirrors the Antigravity capacity coordinator: reads
// are single-flight, a fresh snapshot is served for claudeCodeUsageDisplayTTL,
// and a failed read keeps the last good snapshot as stale with retry backoff.
type claudeCodeUsageCoordinator struct {
	ctx    context.Context
	reader ports.ClaudeCodeUsageReader
	logger *slog.Logger
	now    func() time.Time

	mu          sync.Mutex
	snapshot    domain.ClaudeCodePlanUsageSnapshot
	invalidated bool
	failures    int
	nextRetryAt time.Time
	call        *claudeCodeUsageReadCall
	receivedAt  time.Time
}

func newClaudeCodeUsageCoordinator(ctx context.Context, reader ports.ClaudeCodeUsageReader, logger *slog.Logger) *claudeCodeUsageCoordinator {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &claudeCodeUsageCoordinator{ctx: ctx, reader: reader, logger: logger, now: time.Now, snapshot: uncheckedClaudeCodeUsage(), invalidated: true}
}

func uncheckedClaudeCodeUsage() domain.ClaudeCodePlanUsageSnapshot {
	return domain.ClaudeCodePlanUsageSnapshot{
		State: domain.ClaudeCodePlanUsageUnknown, Freshness: domain.AgentReadinessStale,
		ReasonCode: domain.ClaudeCodePlanUsageReasonNotChecked, Reason: "Claude Code plan usage has not been checked yet.",
		Windows: []domain.ClaudeCodePlanUsageWindow{},
	}
}

func unsupportedClaudeCodeUsage() domain.ClaudeCodePlanUsageSnapshot {
	return domain.ClaudeCodePlanUsageSnapshot{
		State: domain.ClaudeCodePlanUsageUnsupported, Freshness: domain.AgentReadinessFresh,
		ReasonCode: domain.ClaudeCodePlanUsageReasonUnsupported, Reason: "Claude Code plan usage is not available in this daemon.",
		Windows: []domain.ClaudeCodePlanUsageWindow{},
	}
}

func (c *claudeCodeUsageCoordinator) current() domain.ClaudeCodePlanUsageSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}

// ensure returns a fresh snapshot, joining an in-flight read when one exists.
// force bypasses the display TTL and the failure backoff.
func (c *claudeCodeUsageCoordinator) ensure(ctx context.Context, force bool) (domain.ClaudeCodePlanUsageSnapshot, error) { //nolint:dupl // Antigravity and Claude Code keep separate usage contracts by design (FORK.md).
	if err := ctx.Err(); err != nil {
		return domain.ClaudeCodePlanUsageSnapshot{}, err
	}
	c.mu.Lock()
	now := c.now()
	fresh := c.snapshot.CheckedAt != nil && now.Sub(*c.snapshot.CheckedAt) < claudeCodeUsageDisplayTTL
	if !force && !c.invalidated && fresh {
		snapshot := c.snapshot
		c.mu.Unlock()
		c.logger.Debug("Claude Code usage cache hit", "trigger", "display", "cache", "hit")
		return snapshot, nil
	}
	if !force && !c.nextRetryAt.IsZero() && now.Before(c.nextRetryAt) {
		snapshot := c.snapshot
		nextRetryAt := c.nextRetryAt
		c.mu.Unlock()
		c.logger.Debug("Claude Code usage retry deferred", "trigger", "display", "cache", "retry_delay", "next_retry_at", nextRetryAt)
		return snapshot, nil
	}
	if c.call != nil {
		call := c.call
		c.mu.Unlock()
		c.logger.Debug("joined Claude Code usage read", "trigger", "display", "cache", "join")
		return c.wait(ctx, call)
	}
	call := &claudeCodeUsageReadCall{done: make(chan struct{}), startedReceivedAt: c.receivedAt}
	c.call = call
	checking := c.snapshot
	checking.Freshness = domain.AgentReadinessChecking
	checking.ReasonCode = domain.ClaudeCodePlanUsageReasonChecking
	checking.Reason = "Checking Claude Code plan usage."
	attemptedAt := now
	checking.AttemptedAt = &attemptedAt
	c.snapshot = checking
	c.mu.Unlock()
	c.logger.Info("started Claude Code usage read", "trigger", "display", "cache", "new")
	go c.runRead(call, attemptedAt)
	return c.wait(ctx, call)
}

func (c *claudeCodeUsageCoordinator) wait(ctx context.Context, call *claudeCodeUsageReadCall) (domain.ClaudeCodePlanUsageSnapshot, error) {
	select {
	case <-call.done:
		return c.current(), nil
	case <-ctx.Done():
		return domain.ClaudeCodePlanUsageSnapshot{}, ctx.Err()
	}
}

// runRead performs one provider read on the coordinator's own context so a
// cancelled request never aborts a read other waiters share.
func (c *claudeCodeUsageCoordinator) runRead(call *claudeCodeUsageReadCall, attemptedAt time.Time) {
	ctx, cancel := context.WithTimeout(c.ctx, claudeCodeUsageReadTimeout)
	observation, err := c.reader.ReadPlanUsage(ctx)
	cancel()
	if err != nil {
		code, reason := classifyClaudeCodeUsageReadFailure(err)
		c.finishFailure(observation, attemptedAt, code, reason, call)
		return
	}
	c.finishSuccess(observation, attemptedAt, call)
}

func classifyClaudeCodeUsageReadFailure(err error) (string, string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return domain.ClaudeCodePlanUsageReasonCheckTimeout, "The plan-usage check timed out."
	case errors.Is(err, context.Canceled):
		return domain.ClaudeCodePlanUsageReasonCheckStopped, "The plan-usage check was interrupted."
	case errors.Is(err, ports.ErrClaudeCodePlanUsageUnsupported):
		return domain.ClaudeCodePlanUsageReasonUnsupported, "Plan usage is not supported here."
	case errors.Is(err, ports.ErrClaudeCodePlanUsageSignedOut):
		return domain.ClaudeCodePlanUsageReasonSignedOut, "Sign in to Claude Code to see plan usage."
	case errors.Is(err, ports.ErrClaudeCodePlanUsageRateLimited):
		return domain.ClaudeCodePlanUsageReasonRateLimited, "Plan usage will refresh again shortly."
	case errors.Is(err, ports.ErrClaudeCodePlanUsageInvalid):
		return domain.ClaudeCodePlanUsageReasonInvalidResponse, "Claude did not return usable plan usage."
	default:
		return domain.ClaudeCodePlanUsageReasonUnavailable, "Plan usage is temporarily unavailable."
	}
}

func (c *claudeCodeUsageCoordinator) finishSuccess(observation ports.ClaudeCodePlanUsageObservation, attemptedAt time.Time, call *claudeCodeUsageReadCall) {
	receivedAt := c.now()
	snapshot := claudeCodeUsageSnapshotFromObservation(observation, attemptedAt, receivedAt)
	c.mu.Lock()
	if !c.receivedAt.After(receivedAt) {
		c.snapshot = snapshot
		c.receivedAt = receivedAt
		c.invalidated = false
		c.failures = 0
		c.nextRetryAt = time.Time{}
	}
	if c.call == call {
		c.call = nil
		close(call.done)
	}
	result := c.snapshot
	c.mu.Unlock()
	c.logger.Info("Claude Code usage updated", "trigger", "usage", "duration_ms", receivedAt.Sub(attemptedAt).Milliseconds(), "outcome", result.State)
}

func (c *claudeCodeUsageCoordinator) finishFailure(observation ports.ClaudeCodePlanUsageObservation, attemptedAt time.Time, code, reason string, call *claudeCodeUsageReadCall) {
	c.mu.Lock()
	if c.receivedAt.After(call.startedReceivedAt) {
		if c.call == call {
			c.call = nil
			close(call.done)
		}
		c.mu.Unlock()
		return
	}
	if c.snapshot.CheckedAt != nil {
		c.snapshot.Freshness = domain.AgentReadinessStale
	} else {
		c.snapshot = uncheckedClaudeCodeUsage()
	}
	if code == domain.ClaudeCodePlanUsageReasonUnsupported {
		c.snapshot.State = domain.ClaudeCodePlanUsageUnsupported
	}
	// A failed provider call still identifies the signed-in account and plan.
	if observation.Plan != nil {
		c.snapshot.Plan = observation.Plan
	}
	if observation.Identity != nil {
		identity := *observation.Identity
		c.snapshot.Identity = &identity
	}
	c.snapshot.AttemptedAt = &attemptedAt
	c.snapshot.ReasonCode, c.snapshot.Reason = code, reason
	c.invalidated = true
	c.failures++
	if c.failures <= len(defaultReadinessRetryDelays) {
		c.nextRetryAt = c.now().Add(defaultReadinessRetryDelays[c.failures-1])
	}
	nextRetryAt := c.nextRetryAt
	if c.call == call {
		c.call = nil
		close(call.done)
	}
	result := c.snapshot
	c.mu.Unlock()
	c.logger.Info("Claude Code usage read completed", "trigger", "usage", "duration_ms", c.now().Sub(attemptedAt).Milliseconds(), "outcome", result.State, "failure_category", code, "next_retry_at", nextRetryAt)
}

// claudeCodeGeneralUsageWindows are the plan-wide meters whose worst reading
// is the subscription's remaining capacity; model-scoped windows are extra.
var claudeCodeGeneralUsageWindows = map[string]struct{}{"five_hour": {}, "seven_day": {}}

func claudeCodeUsageSnapshotFromObservation(observation ports.ClaudeCodePlanUsageObservation, attemptedAt, checkedAt time.Time) domain.ClaudeCodePlanUsageSnapshot {
	snapshot := domain.ClaudeCodePlanUsageSnapshot{
		State: domain.ClaudeCodePlanUsageAvailable, Freshness: domain.AgentReadinessFresh,
		Plan: observation.Plan, Windows: append([]domain.ClaudeCodePlanUsageWindow(nil), observation.Windows...),
		ReasonCode: domain.ClaudeCodePlanUsageReasonAvailable, Reason: "Plan usage is up to date.",
	}
	if snapshot.Windows == nil {
		snapshot.Windows = []domain.ClaudeCodePlanUsageWindow{}
	}
	if observation.Identity != nil {
		identity := *observation.Identity
		snapshot.Identity = &identity
	}
	if observation.Promotion != nil {
		promotion := *observation.Promotion
		snapshot.Promotion = &promotion
	}
	observedAt := observation.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = checkedAt.UTC()
	}
	attemptedAt, checkedAt = attemptedAt.UTC(), checkedAt.UTC()
	snapshot.ObservedAt, snapshot.CheckedAt, snapshot.AttemptedAt = &observedAt, &checkedAt, &attemptedAt
	var used *float64
	for i := range snapshot.Windows {
		window := snapshot.Windows[i]
		if _, general := claudeCodeGeneralUsageWindows[window.ID]; !general {
			continue
		}
		if used == nil || window.UsedPercent > *used {
			value := window.UsedPercent
			used = &value
		}
	}
	if used != nil {
		remaining := 100 - *used
		snapshot.RemainingPercent = &remaining
	}
	return snapshot
}

// CachedClaudeCodeUsage returns the last Claude Code plan-usage snapshot
// without contacting the provider.
func (s *Service) CachedClaudeCodeUsage(ctx context.Context) (domain.ClaudeCodePlanUsageSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.ClaudeCodePlanUsageSnapshot{}, err
	}
	if s.claudeCodeUsage == nil {
		return unsupportedClaudeCodeUsage(), nil
	}
	return s.claudeCodeUsage.current(), nil
}

// EnsureClaudeCodeUsage returns a display-fresh Claude Code plan-usage
// snapshot, reading the provider when the cache is stale. force reads
// regardless of cache.
func (s *Service) EnsureClaudeCodeUsage(ctx context.Context, force bool) (domain.ClaudeCodePlanUsageSnapshot, error) {
	if s.claudeCodeUsage == nil {
		return unsupportedClaudeCodeUsage(), nil
	}
	return s.claudeCodeUsage.ensure(ctx, force)
}
