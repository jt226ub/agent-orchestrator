package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

func (m *agyAccountManager) openLoginTerminal(ctx context.Context, targetAccountID string) (AgyAccountLoginTerminalStart, error) {
	release, err := m.acquireAccountMutation(ctx)
	if err != nil {
		return AgyAccountLoginTerminalStart{}, err
	}
	defer release()
	if m.terminal == nil || m.executable == nil {
		return AgyAccountLoginTerminalStart{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy login terminal is unavailable")
	}
	targetAccountID = strings.TrimSpace(targetAccountID)
	deviceCredential, deviceState, deviceErr := readCodexFileState(m.globalCredentialPath(), true)
	if deviceErr != nil {
		if targetAccountID != "" {
			return AgyAccountLoginTerminalStart{}, apierr.Unavailable("AGY_ACCOUNT_RECONCILIATION_UNAVAILABLE", "The device Agy account could not be refreshed")
		}
		// Normal Add remains available when the device store cannot be inspected,
		// but it must never auto-activate based on an unsafe absence assumption.
		deviceState.exists = true
	}
	if targetAccountID != "" {
		record, ok := m.catalog.record(targetAccountID)
		if !ok || (record.Snapshot.Status != domain.AgyAccountStatusValid && record.Snapshot.Status != domain.AgyAccountStatusSignedOut) {
			return AgyAccountLoginTerminalStart{}, apierr.NotFound("AGY_ACCOUNT_NOT_FOUND", "Agy account not found")
		}
	}
	id := m.newID()
	now := m.now()
	loginReason := "Sign in with the same Agy account."
	if targetAccountID == "" {
		loginReason = "Sign in to add a Agy account."
	} else if record, ok := m.catalog.record(targetAccountID); ok && record.Snapshot.AccountEmail != nil && safeAccountEmail(*record.Snapshot.AccountEmail) {
		loginReason = "Sign in with " + strings.TrimSpace(*record.Snapshot.AccountEmail) + ". A different account will not be saved."
	}
	targetWasActive := targetAccountID != "" && m.activeAccountID() == targetAccountID
	snapshot := domain.AgyAccountLoginOperation{OperationID: id, AccountID: targetAccountID, Status: domain.AgyAccountLoginPending, ReasonCode: domain.AgyAccountLoginReasonPending, Reason: loginReason, ExpiresAt: now.Add(agyAccountLoginLifetime)}
	m.mu.Lock()
	if m.login != nil && !agyTerminalLoginStatus(m.login.snapshot.Status) {
		m.mu.Unlock()
		return AgyAccountLoginTerminalStart{}, apierr.Conflict("AGY_ACCOUNT_LOGIN_IN_PROGRESS", "A Agy account login is already in progress", nil)
	}
	previous := m.login
	m.login = &agyAccountLoginOperation{
		snapshot:                 snapshot,
		targetAccountID:          targetAccountID,
		deviceState:              deviceState,
		startingGlobalCredential: bytes.Clone(deviceCredential),
		targetWasActive:          targetWasActive,
	}
	m.mu.Unlock()
	if previous != nil {
		m.cleanupLoginFiles(previous)
	}
	pendingDir, home, err := agyCreatePendingCredentialHome(m.pendingRoot, id)
	if err != nil {
		m.clearLoginReservation(id)
		return AgyAccountLoginTerminalStart{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy login could not be prepared")
	}
	executable, err := m.executable()
	if err != nil || strings.TrimSpace(executable) == "" {
		_ = os.RemoveAll(pendingDir)
		m.clearLoginReservation(id)
		return AgyAccountLoginTerminalStart{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy login terminal is unavailable")
	}
	title := "Add Agy account"
	if targetAccountID != "" {
		title = "Sign in to Agy account"
	}
	// The CLI keys its config directory off HOME, so the login terminal (and
	// only it) runs with HOME at the pending skeleton; worker sessions never do.
	terminal, err := m.terminal.OpenCommandTerminal(ctx, shellterm.OpenCommandTerminalInput{Argv: []string{executable, "agy-login"}, Env: map[string]string{"HOME": home}, WorkingDir: home, Title: title})
	if err != nil {
		_ = os.RemoveAll(pendingDir)
		m.clearLoginReservation(id)
		return AgyAccountLoginTerminalStart{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy login terminal could not be opened")
	}
	m.mu.Lock()
	if m.login == nil || m.login.snapshot.OperationID != id {
		m.mu.Unlock()
		_ = m.terminal.CloseShellTerminal(context.WithoutCancel(ctx), terminal.HandleID)
		_ = os.RemoveAll(pendingDir)
		return AgyAccountLoginTerminalStart{}, apierr.Conflict("AGY_ACCOUNT_LOGIN_IN_PROGRESS", "A Agy account login changed concurrently", nil)
	}
	m.login.pendingDir, m.login.home, m.login.terminalHandle = pendingDir, home, terminal.HandleID
	m.login.terminalTitle, m.login.terminalCreated = terminal.Title, terminal.CreatedAt
	m.mu.Unlock()
	go m.expireLogin(m.ctx, id, snapshot.ExpiresAt)
	go m.pollLogin(m.ctx, id, filepath.Join(home, agyCredentialFilename))
	m.publish()
	return AgyAccountLoginTerminalStart{Operation: snapshot, ShellTerminal: terminal}, nil
}

// pollLogin verifies the pending login on its own once the CLI has written a
// token into the staged home: the CLI's browser sign-in completes without any
// further keystroke, so waiting for a "check again" click would let the short
// login window pass. Verification stays idempotent for a manual check.
func (m *agyAccountManager) pollLogin(ctx context.Context, operationID, credentialPath string) {
	for {
		select {
		case <-m.after(agyAccountLoginPollInterval):
		case <-ctx.Done():
			return
		}
		m.mu.Lock()
		op := m.login
		live := op != nil && op.snapshot.OperationID == operationID && !agyTerminalLoginStatus(op.snapshot.Status) && !op.closing
		m.mu.Unlock()
		if !live {
			return
		}
		if _, state, err := readCodexFileState(credentialPath, true); err != nil || !state.exists {
			continue
		}
		result, err := m.verifyLogin(ctx, operationID)
		if err != nil || agyTerminalLoginStatus(result.Status) {
			return
		}
	}
}

func (m *agyAccountManager) clearLoginReservation(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.login != nil && m.login.snapshot.OperationID == id {
		m.login = nil
	}
}

func (m *agyAccountManager) cleanupLoginFiles(op *agyAccountLoginOperation) {
	if op == nil {
		return
	}
	if op.terminalHandle != "" && m.terminal != nil {
		_ = m.terminal.CloseShellTerminal(context.Background(), op.terminalHandle)
	}
	if op.pendingDir != "" {
		_ = os.RemoveAll(op.pendingDir)
	}
}

func agyTerminalLoginStatus(status domain.AgyAccountLoginStatus) bool {
	return status == domain.AgyAccountLoginCompleted || status == domain.AgyAccountLoginCancelled || status == domain.AgyAccountLoginExpired || status == domain.AgyAccountLoginFailed
}

func (m *agyAccountManager) verifyLogin(ctx context.Context, operationID string) (domain.AgyAccountLoginOperation, error) {
	m.mu.Lock()
	op := m.login
	if op == nil || op.snapshot.OperationID != operationID {
		m.mu.Unlock()
		return domain.AgyAccountLoginOperation{}, apierr.NotFound("AGY_ACCOUNT_LOGIN_NOT_FOUND", "Agy account login operation not found")
	}
	if agyTerminalLoginStatus(op.snapshot.Status) {
		result := op.snapshot
		m.mu.Unlock()
		return result, nil
	}
	if op.closing || op.committing {
		result := op.snapshot
		m.mu.Unlock()
		return result, nil
	}
	if op.snapshot.Status == domain.AgyAccountLoginVerifying {
		result := op.snapshot
		m.mu.Unlock()
		return result, nil
	}
	op.snapshot.Status = domain.AgyAccountLoginVerifying
	op.snapshot.Reason = "Verifying the Agy account."
	home, pendingDir, terminalHandle := op.home, op.pendingDir, op.terminalHandle
	m.mu.Unlock()
	m.publish()
	pendingPath := filepath.Join(home, agyCredentialFilename)
	pendingCredential, admitted, credentialErr := readCodexFileState(pendingPath, false)
	if credentialErr != nil {
		return m.finishLogin(operationID, domain.AgyAccountLoginUnauthorized, domain.AgyAccountLoginReasonUnauthorized, "Agy is still signed out.", nil), nil
	}
	identity, identityErr := parseAgyCredentialIdentity(pendingCredential)
	latestCredential, latest, latestErr := readCodexFileState(pendingPath, false)
	if identityErr != nil || latestErr != nil || !sameCodexFileState(admitted, latest) || !bytes.Equal(pendingCredential, latestCredential) {
		return m.finishLoginRetryable(operationID), nil
	}
	observation := ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationUnknown, Method: identity.Method, Email: identity.Email}
	exclusive, exclusiveErr := m.acquireGlobalMutation(ctx)
	if exclusiveErr != nil {
		return domain.AgyAccountLoginOperation{}, exclusiveErr
	}
	if exclusive != nil {
		defer exclusive.Release()
	}
	releaseMutation, mutationErr := m.acquireAccountMutation(ctx)
	if mutationErr != nil {
		return domain.AgyAccountLoginOperation{}, mutationErr
	}
	defer releaseMutation()
	m.mu.Lock()
	op = m.login
	if op == nil || op.snapshot.OperationID != operationID || agyTerminalLoginStatus(op.snapshot.Status) || op.closing {
		var result domain.AgyAccountLoginOperation
		if op != nil && op.snapshot.OperationID == operationID {
			result = op.snapshot
		}
		m.mu.Unlock()
		return result, nil
	}
	op.committing = true
	op.commitDone = make(chan struct{})
	commitDone := op.commitDone
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.login != nil && m.login.snapshot.OperationID == operationID && m.login.commitDone == commitDone {
			m.login.committing = false
			close(commitDone)
			m.login.commitDone = nil
		}
		m.mu.Unlock()
	}()
	targetAccountID := op.targetAccountID
	var record agyAccountRecord
	if targetAccountID != "" {
		target, targetFound := m.catalog.record(targetAccountID)
		if !targetFound || !agyLoginCredentialIdentifiesRecord(target, identity, pendingCredential) {
			return m.finishLogin(operationID, domain.AgyAccountLoginFailed, domain.AgyAccountLoginReasonFailed, "Sign in with the same Agy account to replace its credentials.", nil), nil
		}
		currentGlobal, currentState, currentErr := readCodexFileState(m.globalCredentialPath(), true)
		if currentErr != nil || !sameCodexFileState(op.deviceState, currentState) || !bytes.Equal(op.startingGlobalCredential, currentGlobal) {
			return m.finishLoginRetryable(operationID), nil
		}
		credential := pendingCredential
		if op.targetWasActive {
			if activateErr := m.activateFromCredentialLocked(ctx, targetAccountID, filepath.Join(home, agyCredentialFilename), op.startingGlobalCredential); activateErr != nil {
				if errors.Is(activateErr, ports.ErrAgyGlobalAccountChanged) {
					return m.finishLoginRetryable(operationID), nil
				}
				return m.finishLogin(operationID, domain.AgyAccountLoginFailed, domain.AgyAccountLoginReasonFailed, "The account was verified but could not be activated.", nil), nil
			}
			record, _ = m.catalog.record(targetAccountID)
		} else {
			var replaceErr error
			record, replaceErr = m.catalog.replaceCredential(ctx, targetAccountID, credential, observation)
			if replaceErr != nil {
				return m.finishLogin(operationID, domain.AgyAccountLoginFailed, domain.AgyAccountLoginReasonFailed, "The verified Agy account could not be saved.", nil), nil
			}
		}
		_ = os.RemoveAll(pendingDir)
		m.clearReauthenticationRequired(targetAccountID)
	} else {
		if existing, found := m.matchCredentialAccount(pendingCredential, identity); found {
			var replaceErr error
			record, replaceErr = m.catalog.replaceCredential(ctx, existing.Snapshot.ID, pendingCredential, observation)
			if replaceErr != nil {
				return m.finishLogin(operationID, domain.AgyAccountLoginFailed, domain.AgyAccountLoginReasonFailed, "The verified Agy account could not be saved.", nil), nil
			}
			_ = os.RemoveAll(pendingDir)
			m.clearReauthenticationRequired(existing.Snapshot.ID)
		} else {
			var err error
			record, err = m.catalog.commitPending(pendingDir, observation)
			if err != nil {
				return m.finishLogin(operationID, domain.AgyAccountLoginFailed, domain.AgyAccountLoginReasonFailed, "The verified Agy account could not be saved.", nil), nil
			}
		}
		activationCredentialPath := filepath.Join(record.Home, agyCredentialFilename)
		activateFirst := !op.deviceState.exists
		if activateFirst {
			// First-account convenience is only safe while the device store is
			// still empty. The empty expected value is an explicit compare-and-
			// swap guard, so an external login that appeared during the terminal
			// flow is never overwritten.
			activationErr := m.activateFromCredentialLocked(ctx, record.Snapshot.ID, activationCredentialPath, []byte{})
			if errors.Is(activationErr, ports.ErrAgyGlobalAccountChanged) {
				// An external login appeared after Add started. The account is still
				// safely saved; leave the device untouched and let reconciliation
				// project the newly authoritative device account.
				m.requestGlobalReconciliationIfNeeded()
				activateFirst = false
			} else if activationErr != nil {
				result := m.finishLogin(operationID, domain.AgyAccountLoginFailed, domain.AgyAccountLoginReasonFailed, "The account was saved but could not be activated.", &record.Snapshot)
				if m.terminal != nil && terminalHandle != "" {
					_ = m.terminal.CloseShellTerminal(context.WithoutCancel(ctx), terminalHandle)
				}
				return result, nil
			}
			if activateFirst {
				m.setManagedGlobal(record.Snapshot.ID)
			}
		}
	}
	m.invalidateCredentialEvidence(record.Snapshot.ID)
	// A native login just produced and committed this credential. Keep that
	// immediate success visible while the independent protected check enriches
	// email, plan and usage in the background.
	m.catalog.updateSnapshot(record.Snapshot.ID, func(snapshot *domain.AgyAccountSnapshot) {
		snapshot.Authentication = successfulAuthentication(m.now(), domain.AgentAuthenticationAuthorized, domain.AgentReadinessReasonAuthorized, "Agy is signed in.")
		if observation.Method != domain.AgyAuthMethodUnknown {
			snapshot.AuthMethod = observation.Method
		}
		if observation.Email != nil {
			snapshot.AccountEmail = observation.Email
		}
		if snapshot.AccountEmail == nil && identity.Email != nil {
			snapshot.AccountEmail = identity.Email
		}
		snapshot.Label = agyAccountLabel(snapshot.AccountEmail)
	})
	latestRecord, _ := m.catalog.record(record.Snapshot.ID)
	snapshot := latestRecord.Snapshot
	snapshot.Active = snapshot.ID == m.activeAccountID()
	snapshot.Capacity = m.capacity.snapshot(snapshot.ID)
	reason := "Agy account added."
	if targetAccountID != "" {
		reason = "Agy account signed in."
	}
	result := m.finishLogin(operationID, domain.AgyAccountLoginCompleted, domain.AgyAccountLoginReasonCompleted, reason, &snapshot)
	if m.terminal != nil && terminalHandle != "" {
		_ = m.terminal.CloseShellTerminal(context.WithoutCancel(ctx), terminalHandle)
	}
	return result, nil
}

func agyLoginCredentialIdentifiesRecord(record agyAccountRecord, identity agyCredentialIdentity, credential []byte) bool {
	if record.ProviderAccountID != "" {
		return identity.Method == domain.AgyAuthMethodGoogle && identity.ProviderAccountID == record.ProviderAccountID
	}
	return agyLocalCredentialIdentifiesRecord(record, credential)
}

func (m *agyAccountManager) matchCredentialAccount(credential []byte, identity agyCredentialIdentity) (agyAccountRecord, bool) {
	record, match := m.matchGlobalCredentialForReconciliation(credential, identity, nil)
	if match == agyCredentialMatchManaged {
		return record, true
	}
	return agyAccountRecord{}, false
}

func (m *agyAccountManager) finishLoginRetryable(id string) domain.AgyAccountLoginOperation {
	return m.finishLogin(id, domain.AgyAccountLoginRetryable, domain.AgyAccountLoginReasonFailed, "The Agy credential is not ready yet. Try again.", nil)
}
func (m *agyAccountManager) finishLogin(id string, status domain.AgyAccountLoginStatus, code, reason string, account *domain.AgyAccountSnapshot) domain.AgyAccountLoginOperation {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.login == nil || m.login.snapshot.OperationID != id {
		return domain.AgyAccountLoginOperation{}
	}
	if m.login.closing && status != domain.AgyAccountLoginCancelled && status != domain.AgyAccountLoginExpired {
		return m.login.snapshot
	}
	if agyTerminalLoginStatus(m.login.snapshot.Status) && m.login.snapshot.Status != status {
		return m.login.snapshot
	}
	m.login.snapshot.Status, m.login.snapshot.ReasonCode, m.login.snapshot.Reason, m.login.snapshot.Account = status, code, reason, account
	if account != nil {
		m.login.snapshot.AccountID = account.ID
	}
	result := m.login.snapshot
	go m.publish()
	return result
}

func (m *agyAccountManager) cancelLogin(ctx context.Context, operationID string) (domain.AgyAccountLoginOperation, error) {
	for {
		m.mu.Lock()
		op := m.login
		if op == nil || op.snapshot.OperationID != operationID {
			m.mu.Unlock()
			return domain.AgyAccountLoginOperation{}, apierr.NotFound("AGY_ACCOUNT_LOGIN_NOT_FOUND", "Agy account login operation not found")
		}
		if agyTerminalLoginStatus(op.snapshot.Status) {
			result := op.snapshot
			m.mu.Unlock()
			return result, nil
		}
		if op.committing && op.commitDone != nil {
			done := op.commitDone
			m.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return domain.AgyAccountLoginOperation{}, ctx.Err()
			}
		}
		if op.closing {
			result := op.snapshot
			m.mu.Unlock()
			return result, nil
		}
		op.closing = true
		handle, pending := op.terminalHandle, op.pendingDir
		m.mu.Unlock()
		if handle != "" && m.terminal != nil {
			if err := m.terminal.CloseShellTerminal(ctx, handle); err != nil {
				m.mu.Lock()
				if m.login != nil && m.login.snapshot.OperationID == operationID {
					m.login.closing = false
					m.login.snapshot.Status = domain.AgyAccountLoginRetryable
					m.login.snapshot.ReasonCode = domain.AgyAccountLoginReasonFailed
					m.login.snapshot.Reason = "Agy login terminal could not be closed."
				}
				m.mu.Unlock()
				m.publish()
				return domain.AgyAccountLoginOperation{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy login terminal could not be closed")
			}
		}
		_ = os.RemoveAll(pending)
		return m.finishLogin(operationID, domain.AgyAccountLoginCancelled, domain.AgyAccountLoginReasonCancelled, "Agy account login was cancelled.", nil), nil
	}
}

func (m *agyAccountManager) expireLogin(ctx context.Context, id string, at time.Time) {
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-m.ctx.Done():
		return
	}
	for {
		m.mu.Lock()
		op := m.login
		if op == nil || op.snapshot.OperationID != id || agyTerminalLoginStatus(op.snapshot.Status) || op.closing {
			m.mu.Unlock()
			return
		}
		if op.committing && op.commitDone != nil {
			done := op.commitDone
			m.mu.Unlock()
			select {
			case <-done:
				continue
			case <-m.ctx.Done():
				return
			}
		}
		op.closing = true
		pending, handle := op.pendingDir, op.terminalHandle
		m.mu.Unlock()
		if pending == "" {
			return
		}
		if handle != "" && m.terminal != nil {
			_ = m.terminal.CloseShellTerminal(ctx, handle)
		}
		_ = os.RemoveAll(pending)
		m.finishLogin(id, domain.AgyAccountLoginExpired, domain.AgyAccountLoginReasonExpired, "Agy account login expired.", nil)
		return
	}
}
