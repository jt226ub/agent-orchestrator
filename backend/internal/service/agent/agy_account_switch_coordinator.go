package agent //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type agyAccountSwitchCoordinator struct {
	credentials                   ports.AgyAccountCredentialManager
	store                         ports.AgyAccountSwitchStore
	agyOperationGate              ports.AgyOperationGate
	agyAccountSwitchMu            sync.Mutex
	agyAccountSwitchWorkerRunning bool
	agyAccountSwitchLease         ports.AgyOperationLease
	backgroundContext             context.Context
	workers                       sync.WaitGroup
	workersMu                     sync.Mutex
	workersClosed                 bool
	clock                         func() time.Time
	publish                       func()
}

const agyAccountSwitchDurableBoundaryWait = 5 * time.Second

func agyAccountSwitchDurableContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), agyAccountSwitchDurableBoundaryWait)
}

func newAgyAccountSwitchCoordinator(
	ctx context.Context,
	credentials ports.AgyAccountCredentialManager,
	store ports.AgyAccountSwitchStore,
	gate ports.AgyOperationGate,
	clock func() time.Time,
	publish func(),
) *agyAccountSwitchCoordinator {
	if ctx == nil {
		ctx = context.Background()
	}
	if clock == nil {
		clock = time.Now
	}
	return &agyAccountSwitchCoordinator{
		credentials: credentials, store: store, agyOperationGate: gate,
		backgroundContext: ctx, clock: clock, publish: publish,
	}
}

func (m *agyAccountSwitchCoordinator) publishAgyAccountSwitchChanged() {
	if m.publish != nil {
		m.publish()
	}
}

func (m *agyAccountSwitchCoordinator) acquireAgyAccountSwitchGate(ctx context.Context) error {
	lease, err := m.agyOperationGate.AcquireExclusive(ctx)
	if err != nil {
		return err
	}
	m.agyAccountSwitchMu.Lock()
	defer m.agyAccountSwitchMu.Unlock()
	if m.agyAccountSwitchWorkerRunning || m.agyAccountSwitchLease != nil {
		lease.Release()
		return ports.ErrAgyAccountSwitchInProgress
	}
	m.agyAccountSwitchLease = lease
	m.agyAccountSwitchWorkerRunning = true
	return nil
}

func (m *agyAccountSwitchCoordinator) finishAgyAccountSwitchWorker() {
	m.agyAccountSwitchMu.Lock()
	m.agyAccountSwitchWorkerRunning = false
	release := m.agyAccountSwitchLease
	m.agyAccountSwitchLease = nil
	m.agyAccountSwitchMu.Unlock()
	if release != nil {
		release.Release()
	}
}

func (m *agyAccountSwitchCoordinator) finishAgyAccountSwitchMutation() {
	m.finishAgyAccountSwitchWorker()
	m.credentials.EndAgyAccountMutation()
}

func (m *agyAccountSwitchCoordinator) agyAccountSwitchIsActive() bool {
	return m.agyOperationGate != nil && m.agyOperationGate.ExclusivePendingOrHeld()
}

// AgyAccountSwitchInProgress is the daemon-wide credential admission fence.
func (m *agyAccountSwitchCoordinator) AgyAccountSwitchInProgress() bool {
	return m.agyAccountSwitchIsActive()
}

func (m *agyAccountSwitchCoordinator) GetAgyAccountSwitch(ctx context.Context, id string) (domain.AgyAccountSwitch, bool, error) {
	if m.store == nil {
		return domain.AgyAccountSwitch{}, false, errors.New("agy account switch store is unavailable")
	}
	return m.store.GetAgyAccountSwitch(ctx, id)
}

// StartAgyAccountSwitch admits and starts one account-service-owned global switch.
// Existing controllers are deliberately outside this transaction: the operation
// changes and verifies the device credential only.
func (m *agyAccountSwitchCoordinator) StartAgyAccountSwitch(ctx context.Context, cfg ports.AgyAccountSwitchConfig) (domain.AgyAccountSwitch, error) {
	cfg.TargetAccountID = strings.TrimSpace(cfg.TargetAccountID)
	cfg.IdempotencyKey = strings.TrimSpace(cfg.IdempotencyKey)
	if cfg.IdempotencyKey == "" {
		return domain.AgyAccountSwitch{}, errors.New("idempotency key is required")
	}
	if m.credentials == nil || m.store == nil {
		return domain.AgyAccountSwitch{}, errors.New("agy account switching is unavailable")
	}
	if existing, ok, readErr := m.store.GetAgyAccountSwitchByIdempotency(ctx, cfg.IdempotencyKey); readErr != nil {
		return domain.AgyAccountSwitch{}, readErr
	} else if ok {
		if existing.TargetAccountID != cfg.TargetAccountID {
			return existing, ports.ErrAgyAccountSwitchIdempotencyConflict
		}
		return existing, nil
	}
	if _, active, readErr := m.store.GetActiveAgyAccountSwitch(ctx); readErr != nil {
		return domain.AgyAccountSwitch{}, readErr
	} else if active {
		return domain.AgyAccountSwitch{}, ports.ErrAgyAccountSwitchInProgress
	}
	if err := m.credentials.WaitAgyAccountStoreReady(ctx); err != nil {
		return domain.AgyAccountSwitch{}, err
	}
	if err := m.acquireAgyAccountSwitchGate(ctx); err != nil {
		return domain.AgyAccountSwitch{}, err
	}
	releaseSwitchGate := true
	defer func() {
		if releaseSwitchGate {
			m.finishAgyAccountSwitchWorker()
		}
	}()
	if err := m.credentials.BeginAgyAccountMutation(ctx); err != nil {
		return domain.AgyAccountSwitch{}, err
	}
	releaseMutation := true
	defer func() {
		if releaseMutation {
			m.credentials.EndAgyAccountMutation()
		}
	}()
	if err := m.credentials.CleanupInactiveAgyAccountSwitches(ctx, ""); err != nil {
		return domain.AgyAccountSwitch{}, err
	}

	switchID := uuid.NewString()
	source, err := m.credentials.PrepareAgyAccountForSwitch(ctx, switchID, cfg.TargetAccountID)
	if err != nil {
		return domain.AgyAccountSwitch{}, err
	}
	if source.Kind == domain.AgyAccountSwitchSourceManaged && source.AccountID == cfg.TargetAccountID {
		_ = m.credentials.CleanupAgyAccountSwitch(ctx, switchID)
		return domain.AgyAccountSwitch{}, ports.ErrAgyAccountAlreadyActive
	}
	now := m.clock()
	sw := domain.AgyAccountSwitch{
		ID: switchID, SourceKind: source.Kind, SourceAccountID: source.AccountID,
		TargetAccountID: cfg.TargetAccountID, Phase: domain.AgyAccountSwitchActivatingAccount,
		IdempotencyKey: cfg.IdempotencyKey,
		CreatedAt:      now, UpdatedAt: now,
	}
	created, inserted, err := m.store.CreateAgyAccountSwitch(ctx, sw)
	if err != nil {
		_ = m.credentials.CleanupAgyAccountSwitch(ctx, switchID)
		return domain.AgyAccountSwitch{}, err
	}
	sw = created
	if !inserted {
		_ = m.credentials.CleanupAgyAccountSwitch(ctx, switchID)
		return sw, nil
	}

	if !m.startWorker(func() {
		m.runAgyAccountSwitch(m.backgroundContext, sw)
	}) {
		// Shutdown raced with admission before the credential mutation worker
		// could start. Terminally cancel this pre-mutation journal now so it does
		// not leave a fake recovery state for the next daemon.
		settleCtx, cancel := agyAccountSwitchDurableContext(ctx)
		sw.FailureCode = "switch_cancelled_before_mutation"
		m.failAndCleanupAgyAccountSwitch(settleCtx, &sw)
		cancel()
		return sw, context.Canceled
	}
	releaseMutation = false
	releaseSwitchGate = false
	return sw, nil
}

func (m *agyAccountSwitchCoordinator) runAgyAccountSwitch(ctx context.Context, sw domain.AgyAccountSwitch) {
	defer func() {
		m.finishAgyAccountSwitchMutation()
		if sw.Phase.Terminal() {
			// The device credential is the source of truth. Reconcile after every
			// terminal outcome so an external login is matched or imported.
			_ = m.credentials.EnsureAgyDeviceAccountReconciled(m.backgroundContext)
		}
	}()
	delay := time.Second
	for !sw.Phase.Terminal() {
		m.dispatchAgyAccountSwitch(ctx, &sw)
		if sw.Phase.Terminal() {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
		current, found, err := m.store.GetAgyAccountSwitch(ctx, sw.ID)
		if err != nil || !found {
			continue
		}
		sw = current
	}
}

// runAgyAccountSwitchRecovery never resumes an interrupted user request.
// It only recognizes an already-installed target; every other local state
// terminally cancels the old journal and lets reconciliation adopt reality.
func (m *agyAccountSwitchCoordinator) runAgyAccountSwitchRecovery(ctx context.Context, sw domain.AgyAccountSwitch) {
	defer func() {
		m.finishAgyAccountSwitchMutation()
		_ = m.credentials.EnsureAgyDeviceAccountReconciled(m.backgroundContext)
	}()
	delay := time.Second
	for !sw.Phase.Terminal() {
		if m.settleAgyAccountSwitch(ctx, &sw, "interrupted_switch_cancelled") {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func (m *agyAccountSwitchCoordinator) dispatchAgyAccountSwitch(ctx context.Context, sw *domain.AgyAccountSwitch) {
	// Switches created before source_kind was introduced are managed-account
	// switches. Keep that compatibility at the credential coordinator boundary.
	if sw.SourceKind == "" {
		sw.SourceKind = domain.AgyAccountSwitchSourceManaged
	}
	for {
		switch sw.Phase {
		case domain.AgyAccountSwitchRequested, domain.AgyAccountSwitchCheckpointCredential:
			// Compatibility only: current switches are created directly at the
			// activating phase because both credentials are already staged.
			if m.advanceAgyAccountSwitch(ctx, sw, domain.AgyAccountSwitchActivatingAccount, "") != nil {
				return
			}
		case domain.AgyAccountSwitchActivatingAccount:
			state, inspectErr := m.credentials.InspectAgyAccountSwitch(ctx, sw.ID, sw.SourceKind, sw.SourceAccountID, sw.TargetAccountID)
			if inspectErr != nil {
				return
			}
			if state != domain.AgyAccountSwitchTargetInstalled {
				if err := m.credentials.ActivatePreparedAgyAccountSwitch(ctx, sw.SourceKind, sw.ID, sw.TargetAccountID); err != nil {
					if errors.Is(err, ports.ErrAgyAccountSwitchNotCommitted) {
						sw.FailureCode = "activation_failed"
						m.failAndCleanupAgyAccountSwitch(ctx, sw)
						return
					}
					m.settleAgyAccountSwitch(ctx, sw, "activation_failed")
					return
				}
			}
			committedAt := m.clock()
			sw.CredentialsCommittedAt = &committedAt
			m.completeAgyAccountSwitch(ctx, sw)
			return
		case domain.AgyAccountSwitchRecoveryRequired:
			// Compatibility for a switch journal written by an older build. There
			// is no user-driven recovery anymore: settle it from the current local
			// credential, then let normal reconciliation adopt that credential.
			m.settleAgyAccountSwitch(ctx, sw, "legacy_switch_interrupted")
			return
		case domain.AgyAccountSwitchCompleted, domain.AgyAccountSwitchFailed:
			return
		default:
			sw.FailureCode = "switch_state_unavailable"
			m.failAndCleanupAgyAccountSwitch(ctx, sw)
			return
		}
	}
}

// settleAgyAccountSwitch resolves an interrupted operation from local state.
// If the target is installed, the switch completes. Otherwise the switch ends
// and ordinary reconciliation adopts whatever credential the device now has.
func (m *agyAccountSwitchCoordinator) settleAgyAccountSwitch(ctx context.Context, sw *domain.AgyAccountSwitch, failureCode string) bool {
	state, err := m.credentials.InspectAgyAccountSwitch(ctx, sw.ID, sw.SourceKind, sw.SourceAccountID, sw.TargetAccountID)
	if err != nil {
		return false
	}
	if state == domain.AgyAccountSwitchTargetInstalled {
		if sw.CredentialsCommittedAt == nil {
			committedAt := m.clock()
			sw.CredentialsCommittedAt = &committedAt
		}
		m.completeAgyAccountSwitch(ctx, sw)
		return true
	}
	sw.FailureCode = failureCode
	m.failAndCleanupAgyAccountSwitch(ctx, sw)
	return true
}

func (m *agyAccountSwitchCoordinator) completeAgyAccountSwitch(ctx context.Context, sw *domain.AgyAccountSwitch) {
	completed := m.clock()
	sw.CompletedAt = &completed
	if m.advanceAgyAccountSwitch(ctx, sw, domain.AgyAccountSwitchCompleted, "") == nil {
		_ = m.credentials.CleanupAgyAccountSwitch(ctx, sw.ID)
	}
}

func (m *agyAccountSwitchCoordinator) failAndCleanupAgyAccountSwitch(ctx context.Context, sw *domain.AgyAccountSwitch) {
	completed := m.clock()
	sw.CompletedAt = &completed
	if m.advanceAgyAccountSwitch(ctx, sw, domain.AgyAccountSwitchFailed, sw.FailureCode) == nil {
		_ = m.credentials.CleanupAgyAccountSwitch(ctx, sw.ID)
	}
}

func (m *agyAccountSwitchCoordinator) advanceAgyAccountSwitch(ctx context.Context, sw *domain.AgyAccountSwitch, next domain.AgyAccountSwitchPhase, code string) error {
	expected := sw.Phase
	candidate := *sw
	candidate.Phase, candidate.FailureCode, candidate.UpdatedAt = next, code, m.clock()
	ok, err := m.store.UpdateAgyAccountSwitch(ctx, candidate, expected)
	if err == nil && ok {
		*sw = candidate
		m.publishAgyAccountSwitchChanged()
		return nil
	}
	settleCtx, cancel := agyAccountSwitchDurableContext(ctx)
	defer cancel()
	current, found, readErr := m.store.GetAgyAccountSwitch(settleCtx, sw.ID)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	if found && current.Phase == candidate.Phase && current.FailureCode == candidate.FailureCode {
		*sw = current
		return nil
	}
	if found {
		*sw = current
	}
	if err != nil {
		return err
	}
	return errors.New("agy account switch changed concurrently")
}

// GetActiveAgyAccountSwitch returns the sole nonterminal switch when present.
func (m *agyAccountSwitchCoordinator) GetActiveAgyAccountSwitch(ctx context.Context) (domain.AgyAccountSwitch, bool, error) {
	if m.store == nil {
		return domain.AgyAccountSwitch{}, false, errors.New("agy account switch store is unavailable")
	}
	sw, ok, err := m.store.GetActiveAgyAccountSwitch(ctx)
	if err != nil || !ok {
		return sw, ok, err
	}
	return sw, true, nil
}

// ReconcileAgyAccountSwitches starts the best-effort recovery supervisor.
// Account-switch recovery must never prevent the rest of the daemon starting.
func (m *agyAccountSwitchCoordinator) ReconcileAgyAccountSwitches(ctx context.Context) error {
	if m.credentials == nil || m.store == nil {
		return nil //nolint:nilerr // account switching is optional when its feature wiring is absent.
	}
	// Publish the Agy launch/mutation fence before the first fallible journal
	// read. A crashed switch may already have crossed the credential mutation
	// boundary even when SQLite is temporarily unavailable.
	if err := m.acquireAgyAccountSwitchGate(ctx); err != nil {
		return err
	}
	sw, ok, err := m.store.GetActiveAgyAccountSwitch(ctx)
	if err != nil {
		m.startAgyAccountSwitchRecoveryRetryWithHeldGate()
		return err
	}
	if !ok {
		cleanupErr := m.credentials.CleanupInactiveAgyAccountSwitches(ctx, "")
		m.finishAgyAccountSwitchWorker()
		return cleanupErr
	}
	if err := m.startAgyAccountSwitchRecoveryWithHeldGate(ctx, sw); err != nil {
		m.startAgyAccountSwitchRecoveryRetryWithHeldGate()
		return err
	}
	return nil
}

func (m *agyAccountSwitchCoordinator) startAgyAccountSwitchRecoveryWithHeldGate(ctx context.Context, sw domain.AgyAccountSwitch) error {
	if err := m.credentials.WaitAgyAccountStoreReady(ctx); err != nil {
		return err
	}
	if err := m.credentials.BeginAgyAccountMutation(ctx); err != nil {
		return err
	}
	if err := m.credentials.CleanupInactiveAgyAccountSwitches(ctx, sw.ID); err != nil {
		m.credentials.EndAgyAccountMutation()
		return err
	}
	if !m.startWorker(func() {
		m.runAgyAccountSwitchRecovery(m.backgroundContext, sw)
	}) {
		m.credentials.EndAgyAccountMutation()
		return context.Canceled
	}
	return nil
}

func (m *agyAccountSwitchCoordinator) startAgyAccountSwitchRecoveryRetryWithHeldGate() {
	if !m.startWorker(func() {
		defer func() {
			if m.backgroundContext.Err() != nil {
				m.finishAgyAccountSwitchWorker()
			}
		}()
		delay := time.Second
		for {
			select {
			case <-m.backgroundContext.Done():
				return
			case <-time.After(delay):
			}
			sw, ok, err := m.store.GetActiveAgyAccountSwitch(m.backgroundContext)
			if err != nil {
				if delay < 30*time.Second {
					delay *= 2
					if delay > 30*time.Second {
						delay = 30 * time.Second
					}
				}
				continue
			}
			if !ok {
				_ = m.credentials.CleanupInactiveAgyAccountSwitches(m.backgroundContext, "")
				m.finishAgyAccountSwitchWorker()
				return
			}
			if err := m.startAgyAccountSwitchRecoveryWithHeldGate(m.backgroundContext, sw); err != nil {
				continue
			}
			return
		}
	}) {
		m.finishAgyAccountSwitchWorker()
	}
}

func (m *agyAccountSwitchCoordinator) startWorker(run func()) bool {
	m.workersMu.Lock()
	defer m.workersMu.Unlock()
	if m.workersClosed {
		return false
	}
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		run()
	}()
	return true
}

func (m *agyAccountSwitchCoordinator) Wait(ctx context.Context) error {
	m.workersMu.Lock()
	m.workersClosed = true
	m.workersMu.Unlock()
	done := make(chan struct{})
	go func() {
		m.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
