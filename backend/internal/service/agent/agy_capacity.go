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
	agyCapacityDisplayTTL  = 2 * time.Minute
	agyCapacityReadTimeout = 15 * time.Second
)

type agyCapacityReadCall struct {
	done              chan struct{}
	startedReceivedAt time.Time
}

// agyCapacityCoordinator caches the Antigravity CLI's plan limits for display.
// It mirrors the Codex capacity coordinator for one account: reads are
// single-flight, a fresh snapshot is served for agyCapacityDisplayTTL, and a
// failed read keeps the last good snapshot as stale with retry backoff.
type agyCapacityCoordinator struct {
	ctx    context.Context
	reader ports.AgyCapacityReader
	logger *slog.Logger
	now    func() time.Time

	mu          sync.Mutex
	snapshot    domain.AgyCapacitySnapshot
	invalidated bool
	failures    int
	nextRetryAt time.Time
	call        *agyCapacityReadCall
	receivedAt  time.Time
}

func newAgyCapacityCoordinator(ctx context.Context, reader ports.AgyCapacityReader, logger *slog.Logger) *agyCapacityCoordinator {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &agyCapacityCoordinator{ctx: ctx, reader: reader, logger: logger, now: time.Now, snapshot: uncheckedAgyCapacity(), invalidated: true}
}

func uncheckedAgyCapacity() domain.AgyCapacitySnapshot {
	return domain.AgyCapacitySnapshot{
		State: domain.AgyCapacityUnknown, Freshness: domain.AgentReadinessStale,
		ReasonCode: domain.AgyCapacityReasonNotChecked, Reason: "Antigravity capacity has not been checked yet.",
		AdditionalBuckets: []domain.AgyCapacityBucket{},
	}
}

func unsupportedAgyCapacity() domain.AgyCapacitySnapshot {
	return domain.AgyCapacitySnapshot{
		State: domain.AgyCapacityUnsupported, Freshness: domain.AgentReadinessFresh,
		ReasonCode: domain.AgyCapacityReasonUnsupported, Reason: "Antigravity capacity is not available in this daemon.",
		AdditionalBuckets: []domain.AgyCapacityBucket{},
	}
}

func (c *agyCapacityCoordinator) current() domain.AgyCapacitySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshot
}

// ensure returns a fresh snapshot, joining an in-flight read when one exists.
// force bypasses the display TTL and the failure backoff.
func (c *agyCapacityCoordinator) ensure(ctx context.Context, force bool) (domain.AgyCapacitySnapshot, error) { //nolint:dupl // Antigravity and Claude Code keep separate usage contracts by design (FORK.md).
	if err := ctx.Err(); err != nil {
		return domain.AgyCapacitySnapshot{}, err
	}
	c.mu.Lock()
	now := c.now()
	fresh := c.snapshot.CheckedAt != nil && now.Sub(*c.snapshot.CheckedAt) < agyCapacityDisplayTTL
	if !force && !c.invalidated && fresh {
		snapshot := c.snapshot
		c.mu.Unlock()
		c.logger.Debug("Antigravity capacity cache hit", "trigger", "display", "cache", "hit")
		return snapshot, nil
	}
	if !force && !c.nextRetryAt.IsZero() && now.Before(c.nextRetryAt) {
		snapshot := c.snapshot
		nextRetryAt := c.nextRetryAt
		c.mu.Unlock()
		c.logger.Debug("Antigravity capacity retry deferred", "trigger", "display", "cache", "retry_delay", "next_retry_at", nextRetryAt)
		return snapshot, nil
	}
	if c.call != nil {
		call := c.call
		c.mu.Unlock()
		c.logger.Debug("joined Antigravity capacity read", "trigger", "display", "cache", "join")
		return c.wait(ctx, call)
	}
	call := &agyCapacityReadCall{done: make(chan struct{}), startedReceivedAt: c.receivedAt}
	c.call = call
	checking := c.snapshot
	checking.Freshness = domain.AgentReadinessChecking
	checking.ReasonCode = domain.AgyCapacityReasonChecking
	checking.Reason = "Antigravity capacity is being checked."
	attemptedAt := now
	checking.AttemptedAt = &attemptedAt
	c.snapshot = checking
	c.mu.Unlock()
	c.logger.Info("started Antigravity capacity read", "trigger", "display", "cache", "new")
	go c.runRead(call, attemptedAt)
	return c.wait(ctx, call)
}

func (c *agyCapacityCoordinator) wait(ctx context.Context, call *agyCapacityReadCall) (domain.AgyCapacitySnapshot, error) {
	select {
	case <-call.done:
		return c.current(), nil
	case <-ctx.Done():
		return domain.AgyCapacitySnapshot{}, ctx.Err()
	}
}

// runRead performs one CLI read on the coordinator's own context so a
// cancelled request never aborts a read other waiters share.
func (c *agyCapacityCoordinator) runRead(call *agyCapacityReadCall, attemptedAt time.Time) {
	ctx, cancel := context.WithTimeout(c.ctx, agyCapacityReadTimeout)
	observation, err := c.reader.ReadAgyCapacity(ctx)
	cancel()
	if err != nil {
		code, reason := classifyAgyCapacityReadFailure(err)
		c.finishFailure(attemptedAt, code, reason, call)
		return
	}
	c.finishSuccess(observation, attemptedAt, call)
}

func classifyAgyCapacityReadFailure(err error) (string, string) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return domain.AgyCapacityReasonCheckTimeout, "The usage-limit check timed out."
	case errors.Is(err, context.Canceled):
		return domain.AgyCapacityReasonCheckStopped, "The usage-limit check was interrupted."
	case errors.Is(err, ports.ErrAgentBinaryNotFound):
		return domain.AgyCapacityReasonNotInstalled, "Install the Antigravity CLI to see plan capacity."
	case errors.Is(err, ports.ErrAgyCapacitySignedOut):
		return domain.AgyCapacityReasonSkippedSignedOut, "Sign in to Antigravity to see plan capacity."
	case errors.Is(err, ports.ErrAgyCapacityRequestRejected):
		return domain.AgyCapacityReasonProviderRejected, "Antigravity could not provide usage limits."
	default:
		return domain.AgyCapacityReasonCheckFailed, "Usage limits could not be checked."
	}
}

func (c *agyCapacityCoordinator) finishSuccess(observation ports.AgyCapacityObservation, attemptedAt time.Time, call *agyCapacityReadCall) {
	receivedAt := c.now()
	snapshot := agyCapacitySnapshotFromObservation(observation, attemptedAt, receivedAt)
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
	c.logger.Info("Antigravity capacity updated", "trigger", "capacity", "duration_ms", receivedAt.Sub(attemptedAt).Milliseconds(), "outcome", result.State)
}

func (c *agyCapacityCoordinator) finishFailure(attemptedAt time.Time, code, reason string, call *agyCapacityReadCall) {
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
		c.snapshot = uncheckedAgyCapacity()
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
	c.logger.Info("Antigravity capacity read completed", "trigger", "capacity", "duration_ms", c.now().Sub(attemptedAt).Milliseconds(), "outcome", result.State, "failure_category", code, "next_retry_at", nextRetryAt)
}

func agyCapacitySnapshotFromObservation(observation ports.AgyCapacityObservation, attemptedAt, checkedAt time.Time) domain.AgyCapacitySnapshot {
	snapshot := domain.AgyCapacitySnapshot{
		State: domain.AgyCapacityUnknown, Freshness: domain.AgentReadinessFresh,
		Overall:           observation.Overall,
		AdditionalBuckets: append([]domain.AgyCapacityBucket(nil), observation.AdditionalBuckets...),
		ReasonCode:        domain.AgyCapacityReasonCheckInconclusive, Reason: "Antigravity did not report a usable capacity window.",
	}
	if snapshot.AdditionalBuckets == nil {
		snapshot.AdditionalBuckets = []domain.AgyCapacityBucket{}
	}
	observedAt := observation.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = checkedAt.UTC()
	}
	attemptedAt, checkedAt = attemptedAt.UTC(), checkedAt.UTC()
	snapshot.ObservedAt, snapshot.CheckedAt, snapshot.AttemptedAt = &observedAt, &checkedAt, &attemptedAt
	if observation.Overall == nil {
		return snapshot
	}
	var best *domain.AgyCapacityWindow
	for _, window := range []*domain.AgyCapacityWindow{observation.Overall.Primary, observation.Overall.Secondary} {
		if window != nil && (best == nil || window.UsedPercent > best.UsedPercent) {
			best = window
		}
	}
	if best == nil {
		return snapshot
	}
	used := best.UsedPercent
	remaining := 100 - used
	snapshot.UsedPercent, snapshot.RemainingPercent, snapshot.ResetsAt = &used, &remaining, best.ResetsAt
	switch {
	case used >= 100:
		snapshot.State, snapshot.ReasonCode, snapshot.Reason = domain.AgyCapacityExhausted, domain.AgyCapacityReasonExhausted, "Antigravity reports that this plan has reached its limit."
	case used >= 75:
		snapshot.State, snapshot.ReasonCode, snapshot.Reason = domain.AgyCapacityNearLimit, domain.AgyCapacityReasonNearLimit, "Antigravity reports that this plan is near its limit."
	default:
		snapshot.State, snapshot.ReasonCode, snapshot.Reason = domain.AgyCapacityAvailable, domain.AgyCapacityReasonAvailable, "Antigravity reports capacity is available for this plan."
	}
	return snapshot
}

// CachedAgyCapacity returns the last Antigravity capacity snapshot without
// contacting the CLI.
func (s *Service) CachedAgyCapacity(ctx context.Context) (domain.AgyCapacitySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return domain.AgyCapacitySnapshot{}, err
	}
	if s.agyCapacity == nil {
		return unsupportedAgyCapacity(), nil
	}
	return s.agyCapacity.current(), nil
}

// EnsureAgyCapacity returns a display-fresh Antigravity capacity snapshot,
// reading the CLI when the cache is stale. force reads regardless of cache.
func (s *Service) EnsureAgyCapacity(ctx context.Context, force bool) (domain.AgyCapacitySnapshot, error) {
	if s.agyCapacity == nil {
		return unsupportedAgyCapacity(), nil
	}
	return s.agyCapacity.ensure(ctx, force)
}
