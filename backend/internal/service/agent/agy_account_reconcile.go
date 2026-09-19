package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const agyDeviceReconciliationAutomaticAttempts = 3

// Account-store initialization covers AO-owned local state only. Device-global
// discovery is deliberately a separate, repeatable operation below.
func (m *agyAccountManager) waitAccountStore(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.accountStoreReady {
		m.mu.Unlock()
		return nil
	}
	call := m.accountStoreCall
	if call == nil {
		if m.accountStoreErr != nil {
			var failure *agyAccountLocalFailure
			if !errors.As(m.accountStoreErr, &failure) || !failure.retryable || m.now().Before(m.accountStoreNextRetry) {
				err := m.accountStoreErr
				m.mu.Unlock()
				return err
			}
		}
		if err := m.ctx.Err(); err != nil {
			m.mu.Unlock()
			return err
		}
		call = &agyAccountReconcileCall{done: make(chan struct{})}
		m.accountStoreCall = call
		go m.runAccountStoreInitialization(call)
	}
	m.mu.Unlock()
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *agyAccountManager) runAccountStoreInitialization(call *agyAccountReconcileCall) {
	err := m.initializeAccountStore()
	m.mu.Lock()
	m.accountStoreErr = err
	m.accountStoreReady = err == nil
	if err != nil {
		m.accountStoreFailures++
		// A bounded cooldown prevents repeated local callers from hammering a
		// temporarily unavailable disk or database.
		delay := time.Second << min(m.accountStoreFailures-1, 5)
		m.accountStoreNextRetry = m.now().Add(delay)
		var failure *agyAccountLocalFailure
		if errors.As(err, &failure) {
			m.logger.Warn("Antigravity account store initialization failed", "reasonCode", failure.reason, "retryable", failure.retryable)
		}
	}
	call.err = err
	m.accountStoreCall = nil
	close(call.done)
	m.mu.Unlock()
	m.publish()
}

// agyAccountLocalFailure contains only a safe category. The underlying
// filesystem error is deliberately not retained because it can contain paths.
type agyAccountLocalFailure struct {
	reason    string
	retryable bool
}

func (e *agyAccountLocalFailure) Error() string { return e.reason }

func agyAccountStoreFailure(reason string, retryable bool) error {
	return &agyAccountLocalFailure{reason: reason, retryable: retryable}
}

func agyAccountStoreStorageFailure(err error) error {
	// Filesystem validation failures are plain errors, deliberately fail closed.
	// Only recognizable I/O failures are eligible for another attempt. The
	// secure-file helpers summarize their cause behind an opaque, path-free
	// message but preserve the underlying os error through Unwrap, so a transient
	// disk or I/O fault is not misclassified as unsafe storage and left blocked
	// until AO restarts.
	var pathErr *os.PathError
	var linkErr *os.LinkError
	isIOFault := errors.As(err, &pathErr) || errors.As(err, &linkErr)
	retryable := isIOFault && !errors.Is(err, os.ErrPermission) && !errors.Is(err, os.ErrExist) && !errors.Is(err, syscall.ENOTDIR) && !errors.Is(err, syscall.ELOOP)
	if !retryable {
		return agyAccountStoreFailure("account_storage_unsafe", false)
	}
	return agyAccountStoreFailure("account_storage_unavailable", true)
}

func (m *agyAccountManager) initializeAccountStore() error {
	if err := agyCleanupPendingCredentialHomes(m.pendingRoot); err != nil {
		return agyAccountStoreStorageFailure(err)
	}
	// Durable switches keep their private target snapshot here across a daemon
	// restart. The switch coordinator removes terminal operation data.
	if err := ensurePrivateDirectory(m.switchStagingRoot); err != nil {
		return agyAccountStoreStorageFailure(err)
	}
	if err := m.catalog.refresh(); err != nil {
		return agyAccountStoreStorageFailure(err)
	}
	return nil
}

func agyDeviceReconciliationFailure(reason string, retryable bool) error {
	return &agyAccountLocalFailure{reason: reason, retryable: retryable}
}

func agyDeviceReconciliationStorageFailure(err error) error {
	return agyAccountStoreStorageFailure(err)
}

func (m *agyAccountManager) reconcileGlobal(ctx context.Context) error {
	return m.reconcileGlobalWithPolicy(ctx, false)
}

func (m *agyAccountManager) reconcileGlobalWithPolicy(ctx context.Context, force bool) error { //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	started := false
	m.mu.Lock()
	call := m.reconcile
	if call == nil {
		if !force && m.reconciliation.Status == domain.AgyDeviceReconciliationTemporarilyUnavailable && m.reconciliation.NextRetryAt != nil && m.now().Before(*m.reconciliation.NextRetryAt) {
			reason := m.reconciliation.ReasonCode
			m.mu.Unlock()
			return agyDeviceReconciliationFailure(reason, true)
		}
		call = &agyAccountReconcileCall{done: make(chan struct{})}
		m.reconcile = call
		now := m.now()
		m.reconciliation.Status = domain.AgyDeviceReconciliationChecking
		m.reconciliation.ActiveAccountVerified = false
		m.reconciliation.ReasonCode = "checking"
		m.reconciliation.Retryable = false
		m.reconciliation.AttemptedAt = timePointer(now)
		m.reconciliation.NextRetryAt = nil
		// Stop routing the last-known active slot through the global home until
		// this attempt has matched the current credential. Otherwise an external
		// A -> B login can write B's observations into A's saved slot. Keep the
		// last matched device account only as a safety hint for mutation routing;
		// the API/UI must not present it as active until this check verifies it.
		m.deferredAccountID = m.deviceAccountID
		m.deviceCredentialPresent = false
		started = true
		go m.runGlobalReconciliation(call)
	}
	m.mu.Unlock()
	if started {
		m.publish()
	}
	select {
	case <-call.done:
		return call.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// requestGlobalReconciliationIfNeeded schedules background device discovery
// only when no fresh result or existing attempt can answer the request.
func (m *agyAccountManager) requestGlobalReconciliationIfNeeded() {
	m.mu.Lock()
	if m.reconcile != nil || m.reconcileRequested || m.reconcileScheduled {
		m.mu.Unlock()
		return
	}
	now := m.now()
	needed := m.reconciliation.Status == domain.AgyDeviceReconciliationNotChecked ||
		(m.reconciliation.Status == domain.AgyDeviceReconciliationVerified &&
			(m.reconciliation.VerifiedAt == nil || now.Sub(*m.reconciliation.VerifiedAt) >= agyAccountDisplayTTL))
	if !needed {
		m.mu.Unlock()
		return
	}
	m.reconcileRequested = true
	m.mu.Unlock()

	go func() {
		_ = m.reconcileGlobal(m.ctx)
		m.mu.Lock()
		m.reconcileRequested = false
		m.mu.Unlock()
	}()
}

func (m *agyAccountManager) runGlobalReconciliation(call *agyAccountReconcileCall) {
	ctx, cancel := context.WithTimeout(m.ctx, agyAccountReconcileTimeout)
	defer cancel()
	call.err = m.reconcileGlobalInner(ctx)
	now := m.now()
	var schedule bool
	var delay time.Duration
	m.mu.Lock()
	if call.err == nil {
		m.reconcileFailures = 0
		m.reconciliation.Retryable = false
		m.reconciliation.NextRetryAt = nil
		m.reconciliation.Status = domain.AgyDeviceReconciliationVerified
		m.reconciliation.ActiveAccountVerified = m.deviceAccountID != ""
		m.reconciliation.ReasonCode = "verified"
		m.reconciliation.VerifiedAt = timePointer(now)
	} else if !errors.Is(call.err, context.Canceled) || m.ctx.Err() == nil {
		failure := agyClassifyDeviceReconciliationFailure(call.err)
		// The retained deviceAccountID is presentation-only while the local check
		// is running. Once that check fails it is no longer safe to present the
		// previous association as current.
		m.deviceAccountID = ""
		m.reconciliation.ActiveAccountVerified = false
		m.reconciliation.ReasonCode = failure.reason
		m.reconciliation.Retryable = failure.retryable
		if failure.retryable {
			m.reconciliation.Status = domain.AgyDeviceReconciliationTemporarilyUnavailable
			m.reconcileFailures++
			delay = time.Second << min(m.reconcileFailures-1, 5)
			next := now.Add(delay)
			m.reconciliation.NextRetryAt = timePointer(next)
			// Make the first three local attempts automatic (immediate, +1s,
			// +2s). After that the UI becomes actionable instead of retrying
			// forever in the background. A manual retry remains available only
			// for failures classified as transient.
			schedule = m.reconcileFailures < agyDeviceReconciliationAutomaticAttempts && !m.reconcileScheduled
			if schedule {
				m.reconcileScheduled = true
			}
		} else {
			m.reconciliation.Status = domain.AgyDeviceReconciliationBlocked
			m.reconciliation.NextRetryAt = nil
		}
		m.logger.Warn("Antigravity device reconciliation failed", "reasonCode", failure.reason, "retryable", failure.retryable)
	}
	if m.reconcile == call {
		m.reconcile = nil
	}
	close(call.done)
	m.mu.Unlock()
	m.publish()
	if schedule {
		m.scheduleGlobalReconciliation(delay)
	}
}

func agyClassifyDeviceReconciliationFailure(err error) *agyAccountLocalFailure {
	var failure *agyAccountLocalFailure
	if errors.As(err, &failure) {
		return failure
	}
	if errors.Is(err, ports.ErrAgyGlobalAccountChanged) {
		return &agyAccountLocalFailure{reason: "global_account_changed", retryable: true}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &agyAccountLocalFailure{reason: "account_reconciliation_timeout", retryable: true}
	}
	return &agyAccountLocalFailure{reason: "account_reconciliation_unavailable", retryable: true}
}

func (m *agyAccountManager) scheduleGlobalReconciliation(delay time.Duration) {
	go func() {
		select {
		case <-m.after(delay):
			m.mu.Lock()
			m.reconcileScheduled = false
			shouldRetry := m.reconciliation.Status == domain.AgyDeviceReconciliationTemporarilyUnavailable
			m.mu.Unlock()
			if shouldRetry {
				_ = m.reconcileGlobalWithPolicy(m.ctx, true)
			}
		case <-m.ctx.Done():
			m.mu.Lock()
			m.reconcileScheduled = false
			m.mu.Unlock()
		}
	}()
}

func (m *agyAccountManager) markDeviceReconciledLocked(active bool, at time.Time) {
	m.reconcileFailures = 0
	m.reconciliation.Status = domain.AgyDeviceReconciliationVerified
	m.reconciliation.ActiveAccountVerified = active
	m.reconciliation.ReasonCode = "verified"
	m.reconciliation.Retryable = false
	m.reconciliation.AttemptedAt = timePointer(at)
	m.reconciliation.VerifiedAt = timePointer(at)
	m.reconciliation.NextRetryAt = nil
}

func (m *agyAccountManager) reconcileGlobalInner(ctx context.Context) error { //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).
	exclusive, err := m.acquireGlobalMutation(ctx)
	if err != nil {
		return err
	}
	if exclusive != nil {
		defer exclusive.Release()
	}
	release, err := m.acquireAccountMutation(ctx)
	if err != nil {
		return err
	}
	defer release()
	if m.globalHome == "" {
		return agyDeviceReconciliationFailure("account_discovery_unavailable", false)
	}
	if err := m.catalog.refresh(); err != nil {
		return agyDeviceReconciliationStorageFailure(err)
	}
	globalCredential, admitted, credentialErr := readCodexFileState(m.globalCredentialPath(), true)
	if credentialErr != nil {
		return agyDeviceReconciliationStorageFailure(credentialErr)
	}
	if !admitted.exists {
		m.mu.Lock()
		m.deviceAccountID = ""
		m.deferredAccountID = ""
		m.deviceCredentialPresent = false
		m.mu.Unlock()
		return nil
	}

	if m.validateGlobalCredentialStore() != nil {
		return agyDeviceReconciliationFailure("global_credential_store_unsupported", false)
	}
	identity, identityErr := parseAgyCredentialIdentity(globalCredential)
	record, match := m.matchGlobalCredentialForReconciliation(globalCredential, identity, identityErr)
	if match == agyCredentialMatchAmbiguous {
		return agyDeviceReconciliationFailure("global_account_ambiguous", false)
	}
	if match == agyCredentialMatchNone && identityErr != nil {
		return agyDeviceReconciliationFailure("global_credential_invalid", false)
	}
	latestGlobal, latestState, latestErr := readCodexFileState(m.globalCredentialPath(), false)
	if latestErr != nil || !sameCodexFileState(admitted, latestState) || !bytes.Equal(globalCredential, latestGlobal) {
		return agyDeviceReconciliationFailure("global_account_changed", true)
	}
	credentialChanged := false
	imported := false
	if match == agyCredentialMatchNone {
		var importErr error
		record, importErr = m.importGlobalCredential(globalCredential, identity)
		if importErr != nil {
			return agyDeviceReconciliationStorageFailure(importErr)
		}
		imported = true
	} else {
		saved, savedErr := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
		credentialChanged = savedErr != nil || !bytes.Equal(saved, globalCredential)
		if err := writePrivateFileAtomic(filepath.Join(record.Home, agyCredentialFilename), globalCredential); err != nil {
			return agyDeviceReconciliationStorageFailure(err)
		}
	}
	discardImport := func() {
		if imported {
			_ = m.catalog.discardCommitted(record.Snapshot.ID)
		}
	}
	if err := m.catalog.refresh(); err != nil {
		discardImport()
		return agyDeviceReconciliationStorageFailure(err)
	}
	if err := m.catalog.updateCredentialIdentity(ctx, record.Snapshot.ID, globalCredential); err != nil {
		discardImport()
		return agyDeviceReconciliationStorageFailure(err)
	}
	if err := m.catalog.refresh(); err != nil {
		discardImport()
		return agyDeviceReconciliationStorageFailure(err)
	}
	finalGlobal, finalState, finalErr := readCodexFileState(m.globalCredentialPath(), false)
	if finalErr != nil || !sameCodexFileState(admitted, finalState) || !bytes.Equal(finalGlobal, globalCredential) {
		discardImport()
		return agyDeviceReconciliationFailure("global_account_changed", true)
	}
	m.setManagedGlobal(record.Snapshot.ID)
	if credentialChanged {
		m.invalidateCredentialEvidence(record.Snapshot.ID)
	}
	return nil
}

type agyCredentialMatch uint8

const (
	agyCredentialMatchNone agyCredentialMatch = iota
	agyCredentialMatchManaged
	agyCredentialMatchAmbiguous
)

func (m *agyAccountManager) matchGlobalCredentialForReconciliation(globalCredential []byte, identity agyCredentialIdentity, identityErr error) (agyAccountRecord, agyCredentialMatch) {
	records, err := m.catalog.recordsFor(nil)
	if err != nil {
		return agyAccountRecord{}, agyCredentialMatchNone
	}
	exact := make([]agyAccountRecord, 0, 1)
	for _, record := range records {
		if record.Snapshot.Status == domain.AgyAccountStatusValid && agyCredentialMatchesRecord(record, globalCredential) {
			exact = append(exact, record)
		}
	}
	if len(exact) == 1 {
		return exact[0], agyCredentialMatchManaged
	}
	if len(exact) > 1 {
		return agyAccountRecord{}, agyCredentialMatchAmbiguous
	}
	if identityErr != nil || identity.ProviderAccountID == "" {
		return agyAccountRecord{}, agyCredentialMatchNone
	}
	matches := make([]agyAccountRecord, 0, 1)
	for _, record := range records {
		if record.Snapshot.Status != domain.AgyAccountStatusValid && record.Snapshot.Status != domain.AgyAccountStatusSignedOut {
			continue
		}
		providerID := record.ProviderAccountID
		if providerID == "" && record.Snapshot.Status == domain.AgyAccountStatusValid {
			if saved, readErr := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename)); readErr == nil {
				if savedIdentity, parseErr := parseAgyCredentialIdentity(saved); parseErr == nil {
					providerID = savedIdentity.ProviderAccountID
				}
			}
		}
		if providerID == identity.ProviderAccountID {
			matches = append(matches, record)
		}
	}
	if len(matches) == 1 {
		return matches[0], agyCredentialMatchManaged
	}
	if len(matches) > 1 {
		return agyAccountRecord{}, agyCredentialMatchAmbiguous
	}
	return agyAccountRecord{}, agyCredentialMatchNone
}

func (m *agyAccountManager) importGlobalCredential(credential []byte, identity agyCredentialIdentity) (agyAccountRecord, error) {
	pendingDir, home, err := agyCreatePendingCredentialHome(m.pendingRoot, uuid.NewString())
	if err != nil {
		return agyAccountRecord{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(pendingDir)
		}
	}()
	if err := writePrivateFileAtomic(filepath.Join(home, agyCredentialFilename), credential); err != nil {
		return agyAccountRecord{}, err
	}
	record, err := m.catalog.commitPending(pendingDir, ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationUnknown,
		Method:         identity.Method,
	})
	if err != nil {
		return agyAccountRecord{}, err
	}
	committed = true
	return record, nil
}

func agyCredentialMatchesRecord(record agyAccountRecord, credential []byte) bool {
	stored, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	return err == nil && bytes.Equal(stored, credential)
}

func (m *agyAccountManager) setManagedGlobal(accountID string) {
	m.mu.Lock()
	m.deviceAccountID = accountID
	m.deferredAccountID = ""
	m.deviceCredentialPresent = true
	m.reconciliation.ActiveAccountVerified = accountID != ""
	m.mu.Unlock()
}

func mapUnknownAgyAccount(err error) error {
	var unknown unknownAgyAccountError
	if errors.As(err, &unknown) {
		return apierr.Invalid("INVALID_AGY_ACCOUNT_ID", "Unknown Antigravity account", map[string]any{"accountId": unknown.id})
	}
	return err
}
