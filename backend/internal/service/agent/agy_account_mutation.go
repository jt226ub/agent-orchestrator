package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (m *agyAccountManager) logout(ctx context.Context, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	active := m.activeAccountID() == accountID
	if active {
		exclusive, err := m.acquireGlobalMutation(ctx)
		if err != nil {
			return err
		}
		if exclusive != nil {
			defer exclusive.Release()
		}
	}
	release, err := m.acquireAccountMutation(ctx)
	if err != nil {
		return err
	}
	defer release()

	record, ok := m.catalog.record(accountID)
	if !ok || (record.Snapshot.Status != domain.AgyAccountStatusValid && record.Snapshot.Status != domain.AgyAccountStatusSignedOut) {
		return apierr.NotFound("AGY_ACCOUNT_NOT_FOUND", "Antigravity account not found")
	}
	if record.Snapshot.Status == domain.AgyAccountStatusSignedOut {
		return nil
	}
	active = m.activeAccountID() == accountID
	credentialPath := filepath.Join(record.Home, agyCredentialFilename)
	logoutCredentialPath := credentialPath
	if active {
		globalCredential, admitted, globalErr := readCodexFileState(m.globalCredentialPath(), true)
		if globalErr != nil {
			return apierr.Conflict("AGY_ACCOUNT_LOGOUT_UNCONFIRMED", "Antigravity could not safely log out this account", nil)
		}
		if admitted.exists {
			identity, identityErr := parseAgyCredentialIdentity(globalCredential)
			matched, match := m.matchGlobalCredentialForReconciliation(globalCredential, identity, identityErr)
			latest, latestState, latestErr := readCodexFileState(m.globalCredentialPath(), false)
			if match != agyCredentialMatchManaged || matched.Snapshot.ID != accountID || latestErr != nil ||
				!sameCodexFileState(admitted, latestState) || !bytes.Equal(globalCredential, latest) {
				return apierr.Conflict("AGY_GLOBAL_ACCOUNT_CHANGED", "The device Antigravity account changed", nil)
			}
			logoutCredentialPath = m.globalCredentialPath()
		}
	}

	_, credentialState, credentialErr := readCodexFileState(logoutCredentialPath, true)
	if credentialErr != nil {
		return apierr.Conflict("AGY_ACCOUNT_LOGOUT_UNCONFIRMED", "Antigravity could not safely log out this account", nil)
	}
	if credentialState.exists {
		// The Antigravity CLI has no sign-out verb, and AO never deletes a
		// credential the CLI still considers live; the row offers delete instead.
		return apierr.NotImplemented("AGY_ACCOUNT_LOGOUT_UNSUPPORTED", "Antigravity cannot sign this account out; delete the account instead")
	}

	if _, err := m.catalog.markSignedOut(accountID); err != nil {
		return apierr.Conflict("AGY_ACCOUNT_LOGOUT_UNCONFIRMED", "Antigravity logged out, but AO could not update the account. Try again.", nil)
	}
	if active {
		m.mu.Lock()
		m.deviceAccountID = ""
		m.deviceCredentialPresent = false
		m.markDeviceReconciledLocked(false, m.now())
		m.mu.Unlock()
	}
	m.clearReauthenticationRequired(accountID)
	m.capacity.replace(accountID, staticAgyCapacity(domain.AgyCapacityUnknown, domain.AgyCapacityReasonSkippedSignedOut, "Sign in to Antigravity to see subscription capacity."), "signed_out")
	m.publish()
	return nil
}

func (m *agyAccountManager) deleteAccount(ctx context.Context, accountID string) error {
	accountID = strings.TrimSpace(accountID)
	record, ok := m.catalog.record(accountID)
	if !ok || (record.Snapshot.Status != domain.AgyAccountStatusValid && record.Snapshot.Status != domain.AgyAccountStatusSignedOut) {
		return apierr.NotFound("AGY_ACCOUNT_NOT_FOUND", "Antigravity account not found")
	}
	if record.Snapshot.Status == domain.AgyAccountStatusValid {
		if err := m.logout(ctx, accountID); err != nil {
			return err
		}
	}
	release, err := m.acquireAccountMutation(ctx)
	if err != nil {
		return err
	}
	defer release()
	record, ok = m.catalog.record(accountID)
	if !ok || (record.Snapshot.Status != domain.AgyAccountStatusValid && record.Snapshot.Status != domain.AgyAccountStatusSignedOut) {
		return apierr.NotFound("AGY_ACCOUNT_NOT_FOUND", "Antigravity account not found")
	}
	if record.Snapshot.Status != domain.AgyAccountStatusSignedOut {
		return apierr.Conflict("AGY_ACCOUNT_DELETE_REQUIRES_LOGOUT", "Log out of this Antigravity account before deleting it", nil)
	}
	if m.activeAccountID() == accountID {
		return apierr.Conflict("AGY_ACCOUNT_DELETE_ACTIVE", "The active Antigravity account cannot be deleted", nil)
	}
	if err := m.catalog.deleteSignedOut(accountID); err != nil {
		return apierr.Conflict("AGY_ACCOUNT_DELETE_UNCONFIRMED", "Antigravity could not safely delete this account", nil)
	}
	m.mu.Lock()
	delete(m.auth, accountID)
	m.mu.Unlock()
	return nil
}

func (m *agyAccountManager) activeAccountID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.reconciliation.ActiveAccountVerified {
		return ""
	}
	return m.deviceAccountID
}

func (m *agyAccountManager) accountMayOwnDeviceCredential(accountID string) bool {
	accountID = strings.TrimSpace(accountID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reconciliation.ActiveAccountVerified {
		return accountID != "" && accountID == m.deviceAccountID
	}
	lastKnownDeviceAccountID := m.deferredAccountID
	if lastKnownDeviceAccountID == "" {
		lastKnownDeviceAccountID = m.deviceAccountID
	}
	if lastKnownDeviceAccountID == "" {
		// Without any local ownership result, every saved account is potentially
		// the device owner. Reconcile before choosing a logout home.
		return accountID != ""
	}
	return accountID != "" && accountID == lastKnownDeviceAccountID
}

func (m *agyAccountManager) activateFromCredentialLocked(ctx context.Context, accountID, sourceCredential string, expectedGlobal []byte) error { //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).
	notCommitted := func(err error) error {
		return errors.Join(ports.ErrAgyAccountSwitchNotCommitted, err)
	}
	if err := ctx.Err(); err != nil {
		return notCommitted(err)
	}
	record, ok := m.catalog.record(accountID)
	if !ok || record.Snapshot.Status != domain.AgyAccountStatusValid {
		return notCommitted(apierr.NotFound("AGY_ACCOUNT_NOT_FOUND", "Antigravity account not found"))
	}
	targetCredential, err := readOpaqueCredential(sourceCredential)
	if err != nil {
		return notCommitted(err)
	}
	if !agyLocalCredentialIdentifiesRecord(record, targetCredential) {
		return notCommitted(apierr.Conflict("AGY_ACCOUNT_IDENTITY_CHANGED", "The selected Antigravity credential does not match this account", nil))
	}
	if err := ctx.Err(); err != nil {
		return notCommitted(err)
	}
	globalPath := m.globalCredentialPath()
	previousCredential, previousErr := readOpaqueCredential(globalPath)
	if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
		return notCommitted(ports.ErrAgyGlobalCredentialStoreUnsupported)
	}
	if expectedGlobal != nil {
		expectsMissing := len(expectedGlobal) == 0
		if (expectsMissing && !errors.Is(previousErr, os.ErrNotExist)) || (!expectsMissing && (previousErr != nil || !bytes.Equal(previousCredential, expectedGlobal))) {
			return notCommitted(ports.ErrAgyGlobalAccountChanged)
		}
	}
	if err := ctx.Err(); err != nil {
		return notCommitted(err)
	}
	if err := agyWriteGlobalCredentialSettled(globalPath, targetCredential); err != nil {
		return err
	}
	currentCredential, currentState, currentErr := readCodexFileState(globalPath, false)
	latestCredential, latestState, latestErr := readCodexFileState(globalPath, false)
	if currentErr != nil || latestErr != nil || !sameCodexFileState(currentState, latestState) ||
		!bytes.Equal(currentCredential, latestCredential) || !bytes.Equal(currentCredential, targetCredential) {
		return ports.ErrAgyGlobalAccountChanged
	}
	now := m.now()
	m.mu.Lock()
	m.deviceAccountID = accountID
	m.deferredAccountID = ""
	m.deviceCredentialPresent = true
	m.markDeviceReconciledLocked(true, now)
	m.mu.Unlock()
	if refreshed, readErr := readOpaqueCredential(globalPath); readErr == nil {
		_ = writePrivateFileAtomic(filepath.Join(record.Home, agyCredentialFilename), refreshed)
	}
	m.invalidate(accountID)
	m.publish()
	return nil
}

func agyWriteGlobalCredentialSettled(path string, data []byte) error {
	err := agyWriteGlobalCredentialAtomic(path, data)
	if err == nil {
		return nil
	}
	current, readErr := readOpaqueCredential(path)
	if readErr == nil && bytes.Equal(current, data) {
		return nil
	}
	return errors.Join(err, readErr)
}

func agyWriteGlobalCredentialAtomic(path string, data []byte) error {
	if len(data) == 0 || len(data) > 8<<20 {
		return errors.New("global Antigravity credential is empty or too large")
	}
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		// The token lives two levels under the home (.gemini/antigravity-cli);
		// a home that has never run the CLI has neither, so create the chain
		// private before the usual validation.
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return err
		}
		if err := ensurePrivateDirectory(parent); err != nil {
			return err
		}
		info, err = os.Lstat(parent)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || validateCodexDirectory(parent, false) != nil {
		return ports.ErrAgyGlobalCredentialStoreUnsupported
	}
	replacement, err := prepareCodexFileReplacementInDirectory(path, data, false)
	if err != nil {
		return err
	}
	defer replacement.Abort()
	return replacement.Commit()
}

func (m *agyAccountManager) globalCredentialPath() string {
	return filepath.Join(m.globalHome, agyCredentialFilename)
}

func (m *agyAccountManager) validateGlobalCredentialStore() error {
	if m.globalHome == "" {
		return ports.ErrAgyGlobalCredentialStoreUnsupported
	}
	// A missing auth.json means that no device account is active; it does not
	// mean that Agy is using a non-file-backed credential store. Validate the
	// path and any credential that is present, while allowing activation to
	// create the file (and, when needed, its private parent directory).
	_, _, err := readCodexFileState(m.globalCredentialPath(), true)
	if err != nil {
		return ports.ErrAgyGlobalCredentialStoreUnsupported
	}
	return nil
}
