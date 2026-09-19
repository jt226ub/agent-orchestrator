package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// Every read is a CLI invocation against one account's home, so cached
	// readings live longer than the device-wide coordinator's.
	agyAccountCapacityDisplayTTL  = 5 * time.Minute
	agyAccountCapacityReadTimeout = 15 * time.Second
)

type agyAccountCapacityReadCall struct {
	done     chan struct{}
	previous domain.AgyCapacitySnapshot
}

type agyAccountCapacityState struct {
	snapshot    domain.AgyCapacitySnapshot
	checkedAt   time.Time
	failures    int
	nextRetryAt time.Time
	call        *agyAccountCapacityReadCall
}

// agyAccountCapacityCoordinator keeps one plan-capacity snapshot per saved
// account, read with the CLI's quota command against that account's home.
// It is the per-account counterpart of the device-wide agyCapacityCoordinator,
// which keeps serving quota admission for the active account.
type agyAccountCapacityCoordinator struct {
	manager *agyAccountManager
	ctx     context.Context
	now     func() time.Time
	mu      sync.Mutex
	states  map[string]*agyAccountCapacityState
}

func newAgyAccountCapacityCoordinator(manager *agyAccountManager) *agyAccountCapacityCoordinator {
	return &agyAccountCapacityCoordinator{manager: manager, ctx: manager.ctx, now: func() time.Time { return manager.now() }, states: map[string]*agyAccountCapacityState{}}
}

func uncheckedAgyAccountCapacity() domain.AgyCapacitySnapshot { return uncheckedAgyCapacity() }

func unavailableAgyCapacity() domain.AgyCapacitySnapshot {
	return staticAgyCapacity(domain.AgyCapacityUnknown, domain.AgyCapacityReasonSkippedSignedOut, "Sign in to Antigravity to see plan capacity.")
}

func staticAgyCapacity(state domain.AgyCapacityState, code, reason string) domain.AgyCapacitySnapshot {
	return domain.AgyCapacitySnapshot{State: state, Freshness: domain.AgentReadinessStale, ReasonCode: code, Reason: reason, AdditionalBuckets: []domain.AgyCapacityBucket{}}
}

func (c *agyAccountCapacityCoordinator) snapshot(accountID string) domain.AgyCapacitySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	if state := c.states[accountID]; state != nil {
		return state.snapshot
	}
	return uncheckedAgyAccountCapacity()
}

func (c *agyAccountCapacityCoordinator) ensureStateLocked(accountID string) *agyAccountCapacityState {
	state := c.states[accountID]
	if state == nil {
		state = &agyAccountCapacityState{snapshot: uncheckedAgyAccountCapacity()}
		c.states[accountID] = state
	}
	return state
}

func (c *agyAccountCapacityCoordinator) ensure(ctx context.Context, records []agyAccountRecord, capabilities domain.AgyAccountCapabilities, bypassBackoff bool) error {
	var firstErr error
	for _, record := range records {
		if _, err := c.ensureOne(ctx, record, capabilities, bypassBackoff); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ensureOne returns a display-fresh snapshot for one account, reading the CLI
// when the cached one is stale and no backoff applies. Accounts that are not
// signed in, or whose credential the plan rejected, are not read.
func (c *agyAccountCapacityCoordinator) ensureOne(ctx context.Context, record agyAccountRecord, capabilities domain.AgyAccountCapabilities, bypassBackoff bool) (domain.AgyCapacitySnapshot, error) {
	if gated, ok := c.authGate(record, capabilities); ok {
		return gated, nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return domain.AgyCapacitySnapshot{}, err
		}
		c.mu.Lock()
		state := c.ensureStateLocked(record.Snapshot.ID)
		now := c.now()
		if !state.checkedAt.IsZero() && now.Sub(state.checkedAt) < agyAccountCapacityDisplayTTL && state.snapshot.Freshness != domain.AgentReadinessStale {
			out := state.snapshot
			c.mu.Unlock()
			return out, nil
		}
		if !bypassBackoff && !state.nextRetryAt.IsZero() && now.Before(state.nextRetryAt) {
			out := state.snapshot
			c.mu.Unlock()
			return out, nil
		}
		if state.call != nil {
			call := state.call
			c.mu.Unlock()
			select {
			case <-call.done:
				continue
			case <-ctx.Done():
				return domain.AgyCapacitySnapshot{}, ctx.Err()
			}
		}
		call := &agyAccountCapacityReadCall{done: make(chan struct{}), previous: state.snapshot}
		state.call = call
		state.snapshot.Freshness = domain.AgentReadinessChecking
		attemptedAt := now
		c.mu.Unlock()
		go c.runRead(record, call, attemptedAt)
		select {
		case <-call.done:
			return c.snapshot(record.Snapshot.ID), nil
		case <-ctx.Done():
			return domain.AgyCapacitySnapshot{}, ctx.Err()
		}
	}
}

func (c *agyAccountCapacityCoordinator) authGate(record agyAccountRecord, capabilities domain.AgyAccountCapabilities) (domain.AgyCapacitySnapshot, bool) {
	if record.Snapshot.Status != domain.AgyAccountStatusValid {
		return c.replace(record.Snapshot.ID, unavailableAgyCapacity(), "signed_out"), true
	}
	if capabilities.CapacityRead.State != domain.AgyCapabilitySupported {
		return c.replace(record.Snapshot.ID, staticAgyCapacity(domain.AgyCapacityUnsupported, domain.AgyCapacityReasonUnsupported, "This Antigravity version does not report plan capacity."), "unsupported"), true
	}
	switch record.Snapshot.Authentication.State {
	case domain.AgentAuthenticationUnauthorized:
		return c.replace(record.Snapshot.ID, unavailableAgyCapacity(), "signed_out"), true
	case domain.AgentAuthenticationAuthorized, domain.AgentAuthenticationUnknown:
		return domain.AgyCapacitySnapshot{}, false
	default:
		return c.replace(record.Snapshot.ID, staticAgyCapacity(domain.AgyCapacityUnknown, domain.AgyCapacityReasonNotChecked, "Plan capacity is read once the account is checked."), "auth_unknown"), true
	}
}

func (c *agyAccountCapacityCoordinator) runRead(record agyAccountRecord, call *agyAccountCapacityReadCall, attemptedAt time.Time) {
	if c.manager.factory == nil {
		c.finishFailure(record.Snapshot.ID, attemptedAt, domain.AgyCapacityReasonCheckFailed, "Antigravity could not be started to read plan capacity.", call)
		return
	}
	select {
	case c.manager.processes <- struct{}{}:
		defer func() { <-c.manager.processes }()
	case <-c.ctx.Done():
		c.finishFailure(record.Snapshot.ID, attemptedAt, domain.AgyCapacityReasonCheckStopped, "The capacity read was interrupted.", call)
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, agyAccountCapacityReadTimeout)
	defer cancel()
	account := c.manager.accountContext(record)
	releaseGlobal, err := c.manager.acquireGlobalRead(ctx, account)
	if err != nil {
		code, reason := classifyAgyCapacityReadFailure(err)
		c.finishFailure(record.Snapshot.ID, attemptedAt, code, reason, call)
		return
	}
	defer releaseGlobal()
	if c.manager.globalCredentialMissingFor(account) {
		c.retryAfterDeviceChange(record.Snapshot.ID, call)
		return
	}
	client, err := c.manager.factory.Open(ctx, account)
	if err != nil {
		c.finishFailure(record.Snapshot.ID, attemptedAt, domain.AgyCapacityReasonCheckFailed, "Antigravity could not be started to read plan capacity.", call)
		return
	}
	defer func() { _ = client.Close() }()
	observation, err := client.ReadCapacity(ctx)
	if c.manager.globalCredentialMissingFor(account) {
		c.retryAfterDeviceChange(record.Snapshot.ID, call)
		return
	}
	if errors.Is(err, ports.ErrAgyOAuthTokenRevoked) || errors.Is(err, ports.ErrAgyCapacitySignedOut) {
		c.finishCredentialRejected(record.Snapshot.ID, attemptedAt, call)
		return
	}
	if err != nil {
		code, reason := classifyAgyCapacityReadFailure(err)
		c.finishFailure(record.Snapshot.ID, attemptedAt, code, reason, call)
		return
	}
	c.finishSuccess(record.Snapshot.ID, observation, attemptedAt, call)
}

// retryAfterDeviceChange leaves the previous snapshot in place: the device
// credential moved under this read, and reconciliation, not this coordinator,
// decides what the account is now.
func (c *agyAccountCapacityCoordinator) retryAfterDeviceChange(accountID string, call *agyAccountCapacityReadCall) {
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	if state.call == call {
		state.snapshot = call.previous
		state.call = nil
		close(call.done)
	}
	c.mu.Unlock()
	_ = c.manager.reconcileGlobalWithPolicy(c.ctx, true)
}

func (c *agyAccountCapacityCoordinator) finishCredentialRejected(accountID string, attemptedAt time.Time, call *agyAccountCapacityReadCall) {
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	if state.call == call {
		state.snapshot = unavailableAgyCapacity()
		state.snapshot.AttemptedAt = &attemptedAt
		state.checkedAt = c.now()
		state.failures = 0
		state.nextRetryAt = time.Time{}
		state.call = nil
		close(call.done)
	}
	c.mu.Unlock()
	c.manager.recordProtectedAuthenticationEvidence(accountID, agyAuthenticationCredentialRejected)
}

func (c *agyAccountCapacityCoordinator) finishSuccess(accountID string, observation ports.AgyCapacityObservation, attemptedAt time.Time, call *agyAccountCapacityReadCall) {
	checkedAt := c.now()
	snapshot := agyCapacitySnapshotFromObservation(observation, attemptedAt, checkedAt)
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	if state.call == call {
		state.snapshot = snapshot
		state.checkedAt = checkedAt
		state.failures = 0
		state.nextRetryAt = time.Time{}
		state.call = nil
		close(call.done)
	}
	c.mu.Unlock()
	c.manager.publish()
}

func (c *agyAccountCapacityCoordinator) finishFailure(accountID string, attemptedAt time.Time, code, reason string, call *agyAccountCapacityReadCall) {
	c.mu.Lock()
	state := c.ensureStateLocked(accountID)
	if state.call == call {
		snapshot := call.previous
		if snapshot.ObservedAt == nil {
			snapshot = staticAgyCapacity(domain.AgyCapacityUnknown, code, reason)
		}
		snapshot.Freshness = domain.AgentReadinessStale
		snapshot.ReasonCode, snapshot.Reason, snapshot.AttemptedAt = code, reason, &attemptedAt
		state.snapshot = snapshot
		state.checkedAt = c.now()
		state.failures++
		if state.failures <= len(defaultReadinessRetryDelays) {
			state.nextRetryAt = c.now().Add(defaultReadinessRetryDelays[state.failures-1])
		} else {
			state.nextRetryAt = c.now().Add(defaultReadinessRetryDelays[len(defaultReadinessRetryDelays)-1])
		}
		state.call = nil
		close(call.done)
	}
	c.mu.Unlock()
	c.manager.publish()
}

// replace installs a synthetic snapshot (signed out, unsupported, pending) and
// clears any backoff so the next eligible read is immediate.
func (c *agyAccountCapacityCoordinator) replace(accountID string, snapshot domain.AgyCapacitySnapshot, _ string) domain.AgyCapacitySnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.ensureStateLocked(accountID)
	if state.call != nil {
		return state.snapshot
	}
	state.snapshot = snapshot
	state.checkedAt = time.Time{}
	state.failures = 0
	state.nextRetryAt = time.Time{}
	return snapshot
}

// invalidate forces the next ensure to read again; clearSnapshot also drops
// the displayed numbers so a stale reading never survives a credential change.
func (c *agyAccountCapacityCoordinator) invalidate(accountID string, clearSnapshot bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := c.ensureStateLocked(accountID)
	state.checkedAt = time.Time{}
	state.failures = 0
	state.nextRetryAt = time.Time{}
	if clearSnapshot && state.call == nil {
		state.snapshot = uncheckedAgyAccountCapacity()
	}
}

func (c *agyAccountCapacityCoordinator) removeAccounts(accountIDs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range accountIDs {
		delete(c.states, id)
	}
}
