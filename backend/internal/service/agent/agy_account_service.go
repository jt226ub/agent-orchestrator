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

// Service integration.
func (s *Service) structuredAgyAuthentication(ctx context.Context, agentID string, purpose domain.AgentReadinessPurpose) (domain.AgentAuthenticationObservation, bool) {
	if agentID != string(domain.HarnessAgy) || s.agyAccounts == nil || s.agyAccounts.factory == nil {
		return domain.AgentAuthenticationObservation{}, false
	}
	if err := s.WaitAgyAccountStoreReady(ctx); err != nil {
		// Account management is optional for ordinary Agy use. When AO's
		// local account store cannot answer safely, fall back to the native
		// readiness path instead of treating that as proof Agy is signed out.
		return domain.AgentAuthenticationObservation{}, false
	}
	if purpose == domain.AgentReadinessPurposeLaunch {
		s.agyAccounts.mu.Lock()
		verified := s.agyAccounts.reconciliation.Status == domain.AgyDeviceReconciliationVerified
		s.agyAccounts.mu.Unlock()
		if !verified {
			return domain.AgentAuthenticationObservation{}, false
		}
	}
	id := s.agyAccounts.activeAccountID()
	if id == "" {
		s.agyAccounts.mu.Lock()
		reconciled := s.agyAccounts.reconciliation.Status == domain.AgyDeviceReconciliationVerified
		credentialPresent := s.agyAccounts.deviceCredentialPresent
		s.agyAccounts.mu.Unlock()
		if !reconciled || credentialPresent {
			return uncheckedAuthentication(), true
		}
		return successfulAuthentication(s.agyAccounts.now(), domain.AgentAuthenticationUnauthorized, domain.AgentReadinessReasonUnauthorized, "Sign in to Agy or add an account in Settings."), true
	}
	record, ok := s.agyAccounts.catalog.record(id)
	if !ok {
		return failedAuthentication(s.agyAccounts.now(), domain.AgentReadinessReasonAuthCheckInconclusive, "The active Agy account is unavailable."), true
	}
	result, err := s.agyAccounts.ensureAuthentication(ctx, record, purpose)
	if err != nil {
		return failedAuthentication(s.agyAccounts.now(), domain.AgentReadinessReasonAuthCheckFailed, "Authentication check failed."), true
	}
	record, ok = s.agyAccounts.catalog.record(id)
	if !ok {
		return domain.AgentAuthenticationObservation{}, false
	}
	if purpose == domain.AgentReadinessPurposeLaunch && result.State == domain.AgentAuthenticationAuthorized && record.Snapshot.AuthMethod == domain.AgyAuthMethodGoogle {
		capabilities := s.agyAccounts.detectCapabilities(ctx)
		if capabilities.CapacityRead.State != domain.AgyCapabilitySupported {
			return domain.AgentAuthenticationObservation{}, false
		}
		// Launch readiness uses a protected provider call. account/read is only
		// local metadata discovery and cannot prove that the server accepts the
		// stored tokens.
		s.agyAccounts.capacity.invalidate(record.Snapshot.ID, false)
		_, capacityErr := s.agyAccounts.capacity.ensureOne(ctx, record, capabilities, true)
		if capacityErr != nil {
			return domain.AgentAuthenticationObservation{}, false
		}
		latest, ok := s.agyAccounts.catalog.record(record.Snapshot.ID)
		if !ok {
			return domain.AgentAuthenticationObservation{}, false
		}
		verified, reauthenticationRequired := s.agyAccounts.authenticationVerification(record.Snapshot.ID)
		if reauthenticationRequired {
			return latest.Snapshot.Authentication, true
		}
		if !verified {
			// Offline, timeout, and provider failures are not evidence that the
			// account is signed out. Let native launch readiness remain advisory.
			return domain.AgentAuthenticationObservation{}, false
		}
		return latest.Snapshot.Authentication, true
	}
	return result, true
}

// CachedAgyAccounts returns the current in-memory view without native work.
func (s *Service) CachedAgyAccounts(ctx context.Context) (AgyAccounts, error) {
	if err := ctx.Err(); err != nil {
		return AgyAccounts{}, err
	}
	if s.agyAccounts == nil {
		return AgyAccounts{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	if err := s.WaitAgyAccountStoreReady(ctx); err != nil {
		return AgyAccounts{}, err
	}
	result := s.agyAccounts.cached()
	if s.agySwitches != nil {
		if sw, ok, err := s.agySwitches.GetActiveAgyAccountSwitch(ctx); err == nil && ok {
			result.CurrentSwitch = &sw
		}
	}
	return result, nil
}

// AgyAccountEnsureOptions selects the account observations refreshed by EnsureAgyAccounts.
type AgyAccountEnsureOptions struct {
	ForceAuthentication       bool
	ForceDeviceReconciliation bool
}

// EnsureAgyAccounts rediscovers requested accounts and refreshes eligible observations.
func (s *Service) EnsureAgyAccounts(ctx context.Context, ids []string, options AgyAccountEnsureOptions) (AgyAccounts, error) {
	if s.agyAccounts == nil {
		return AgyAccounts{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	if s.agySwitches != nil && s.agySwitches.AgyAccountSwitchInProgress() {
		if sw, ok, err := s.agySwitches.GetActiveAgyAccountSwitch(ctx); err == nil && ok {
			result := s.agyAccounts.cached()
			result.CurrentSwitch = &sw
			return result, nil
		}
		return s.agyAccounts.cached(), nil
	}
	if err := s.WaitAgyAccountStoreReady(ctx); err != nil {
		return AgyAccounts{}, err
	}
	// Full Settings refreshes establish the current device account before any
	// global-home checks. A targeted row refresh stays in that saved account's
	// isolated home, so it must not publish a transient device reconciliation.
	if len(ids) == 0 || options.ForceDeviceReconciliation {
		_ = s.agyAccounts.reconcileGlobalWithPolicy(ctx, options.ForceDeviceReconciliation)
	}
	installation, err := s.readiness.EnsureInstallation(ctx, []string{string(domain.HarnessAgy)}, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		return AgyAccounts{}, err
	}
	result, err := s.agyAccounts.ensure(ctx, ids, options.ForceAuthentication, installation[0].Installation.State)
	if err == nil && s.agySwitches != nil {
		if sw, ok, switchErr := s.agySwitches.GetActiveAgyAccountSwitch(ctx); switchErr == nil && ok {
			result.CurrentSwitch = &sw
		}
	}
	return result, err
}

// SubscribeAgyAccounts returns cached state followed by latest-wins updates.
func (s *Service) SubscribeAgyAccounts(ctx context.Context) (<-chan AgyAccounts, error) {
	if s.agyAccounts == nil {
		return nil, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	if err := s.WaitAgyAccountStoreReady(ctx); err != nil {
		return nil, err
	}
	source := s.agyAccounts.subscribe(ctx)
	out := make(chan AgyAccounts, 1)
	go func() {
		defer close(out)
		for snapshot := range source {
			if s.agySwitches != nil {
				if sw, ok, err := s.agySwitches.GetActiveAgyAccountSwitch(ctx); err == nil && ok {
					snapshot.CurrentSwitch = &sw
				}
			}
			select {
			case out <- snapshot:
			default:
				select {
				case <-out:
				default:
				}
				select {
				case out <- snapshot:
				default:
				}
			}
		}
	}()
	return out, nil
}

// PublishAgyAccounts notifies subscribers after externally owned switch changes.
func (s *Service) PublishAgyAccounts() {
	if s.agyAccounts != nil {
		s.agyAccounts.publish()
	}
}

// SetAgyAccountLoginTerminalOpener wires the trusted shell-terminal boundary.
func (s *Service) SetAgyAccountLoginTerminalOpener(opener agyAccountLoginTerminalService) {
	if s.agyAccounts != nil {
		s.agyAccounts.terminal = opener
	}
}

func (s *Service) prepareAgyAccountLogin(ctx context.Context) error {
	if s.agyAccounts == nil {
		return apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	if s.agySwitches != nil && s.agySwitches.AgyAccountSwitchInProgress() {
		return apierr.Conflict("AGY_ACCOUNT_SWITCH_IN_PROGRESS", "A Agy account switch is already in progress", nil)
	}
	if err := s.WaitAgyAccountStoreReady(ctx); err != nil {
		return err
	}
	if err := s.requireAgyAccountInstallation(ctx); err != nil {
		return err
	}
	capabilities := s.agyAccounts.detectCapabilities(ctx)
	switch capabilities.NativeLogin.State {
	case domain.AgyCapabilityUnsupported:
		return apierr.NotImplemented("AGY_ACCOUNT_MANAGEMENT_UNSUPPORTED", "This Agy version does not support account management")
	case domain.AgyCapabilityUnknown:
		return apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management capability could not be verified")
	default:
		return nil
	}
}

// OpenAgyAccountLoginTerminal starts one private native-login operation.
func (s *Service) OpenAgyAccountLoginTerminal(ctx context.Context) (AgyAccountLoginTerminalStart, error) {
	if err := s.prepareAgyAccountLogin(ctx); err != nil {
		return AgyAccountLoginTerminalStart{}, err
	}
	return s.agyAccounts.openLoginTerminal(ctx, "")
}

// OpenAgyAccountReauthenticationTerminal starts native sign-in for one
// retained account slot. A locally validated credential replaces that slot
// instead of creating a duplicate account.
func (s *Service) OpenAgyAccountReauthenticationTerminal(ctx context.Context, accountID string) (AgyAccountLoginTerminalStart, error) {
	if err := s.prepareAgyAccountLogin(ctx); err != nil {
		return AgyAccountLoginTerminalStart{}, err
	}
	accountID = strings.TrimSpace(accountID)
	if err := s.EnsureAgyDeviceAccountReconciled(ctx); err != nil {
		return AgyAccountLoginTerminalStart{}, err
	}
	return s.agyAccounts.openLoginTerminal(ctx, accountID)
}

// LogoutAgyAccount removes one AO-saved credential while retaining the
// account card. Active-account logout also clears the device-global file-backed
// credential after exact structured identity confirmation.
func (s *Service) LogoutAgyAccount(ctx context.Context, accountID string) (AgyAccounts, error) {
	if s.agyAccounts == nil || s.agyAccounts.factory == nil {
		return AgyAccounts{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	if s.agySwitches != nil && s.agySwitches.AgyAccountSwitchInProgress() {
		return AgyAccounts{}, apierr.Conflict("AGY_ACCOUNT_SWITCH_IN_PROGRESS", "A Agy account switch is already in progress", nil)
	}
	if s.AgyAccountLoginInProgress() {
		return AgyAccounts{}, apierr.Conflict("AGY_ACCOUNT_LOGIN_IN_PROGRESS", "Finish or close the Agy account login before logging out", nil)
	}
	if err := s.WaitAgyAccountStoreReady(ctx); err != nil {
		return AgyAccounts{}, err
	}
	accountID = strings.TrimSpace(accountID)
	// Only the verified or last-known device owner needs global reconciliation.
	// An inactive account logs out through its isolated AO home even when the
	// device-global credential cannot currently be inspected.
	if s.agyAccounts.accountMayOwnDeviceCredential(accountID) {
		if err := s.EnsureAgyDeviceAccountReconciled(ctx); err != nil {
			return AgyAccounts{}, err
		}
	}
	if err := s.agyAccounts.logout(ctx, accountID); err != nil {
		return AgyAccounts{}, err
	}
	s.readiness.Invalidate(string(domain.HarnessAgy), readinessInvalidateAuthentication)
	return s.CachedAgyAccounts(ctx)
}

// DeleteAgyAccount permanently removes one inactive signed-out account slot.
func (s *Service) DeleteAgyAccount(ctx context.Context, accountID string) (AgyAccounts, error) {
	if s.agyAccounts == nil {
		return AgyAccounts{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	if s.agySwitches != nil && s.agySwitches.AgyAccountSwitchInProgress() {
		return AgyAccounts{}, apierr.Conflict("AGY_ACCOUNT_SWITCH_IN_PROGRESS", "A Agy account switch is already in progress", nil)
	}
	if s.AgyAccountLoginInProgress() {
		return AgyAccounts{}, apierr.Conflict("AGY_ACCOUNT_LOGIN_IN_PROGRESS", "Finish or close the Agy account login before deleting an account", nil)
	}
	if err := s.WaitAgyAccountStoreReady(ctx); err != nil {
		return AgyAccounts{}, err
	}
	accountID = strings.TrimSpace(accountID)
	if s.agyAccounts.accountMayOwnDeviceCredential(accountID) {
		if err := s.EnsureAgyDeviceAccountReconciled(ctx); err != nil {
			return AgyAccounts{}, err
		}
	}
	if err := s.agyAccounts.deleteAccount(ctx, accountID); err != nil {
		return AgyAccounts{}, err
	}
	return s.CachedAgyAccounts(ctx)
}

// VerifyAgyAccountLogin completes a pending login once its local credential
// has been safely validated and committed.
func (s *Service) VerifyAgyAccountLogin(ctx context.Context, operationID string) (domain.AgyAccountLoginOperation, error) {
	if s.agyAccounts == nil {
		return domain.AgyAccountLoginOperation{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	result, err := s.agyAccounts.verifyLogin(ctx, strings.TrimSpace(operationID))
	if err == nil && result.Status == domain.AgyAccountLoginCompleted && result.Account != nil {
		if result.Account.Active && s.readiness != nil {
			s.readiness.Invalidate(string(domain.HarnessAgy), readinessInvalidateAuthentication)
		}
		// Credential commit is complete. Provider-backed authentication, capacity,
		// and usage warming happens independently and cannot roll the login back.
		accountID := result.Account.ID
		go func() {
			_, _ = s.EnsureAgyAccounts(s.agyAccounts.ctx, []string{accountID}, AgyAccountEnsureOptions{
				ForceAuthentication: true, ForceDeviceReconciliation: true,
			})
		}()
	}
	return result, err
}

// CancelAgyAccountLogin destroys a pending login and its credential staging.
func (s *Service) CancelAgyAccountLogin(ctx context.Context, operationID string) (domain.AgyAccountLoginOperation, error) {
	if s.agyAccounts == nil {
		return domain.AgyAccountLoginOperation{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	return s.agyAccounts.cancelLogin(ctx, strings.TrimSpace(operationID))
}
func (s *Service) requireAgyAccountInstallation(ctx context.Context) error {
	observations, err := s.readiness.EnsureInstallation(ctx, []string{string(domain.HarnessAgy)}, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		return err
	}
	if observations[0].Installation.State == domain.AgentInstallationNotInstalled && observations[0].Installation.Freshness == domain.AgentReadinessFresh {
		return apierr.NotImplemented("AGY_ACCOUNT_MANAGEMENT_UNSUPPORTED", "Agy is not installed")
	}
	return nil
}

// InvalidateAgyAccountAuthentication invalidates the globally active account.
func (s *Service) InvalidateAgyAccountAuthentication() {
	if s.agyAccounts == nil {
		return
	}
	id := s.agyAccounts.activeAccountID()
	if id != "" {
		s.agyAccounts.invalidate(id)
	}
	s.readiness.Invalidate(string(domain.HarnessAgy), readinessInvalidateAuthentication)
	go func() { _ = s.agyAccounts.reconcileGlobal(s.agyAccounts.ctx) }()
}

// WarmAgyAccounts starts asynchronous local-store initialization, local
// device reconciliation, and then saved-account observation warming.
func (s *Service) WarmAgyAccounts() {
	if s.agyAccounts == nil {
		return
	}
	go func() {
		if err := s.agyAccounts.waitAccountStore(s.agyAccounts.ctx); err != nil {
			return
		}
		// Reconciliation is local-only and establishes the safe home for the device
		// account before any Agy process is opened for authentication or capacity.
		// A local reconciliation failure still leaves inactive saved accounts
		// eligible for their isolated checks below.
		_ = s.agyAccounts.reconcileGlobal(s.agyAccounts.ctx)
		capabilities := s.agyAccounts.detectCapabilities(s.agyAccounts.ctx)
		records, err := s.agyAccounts.catalog.recordsFor(nil)
		if err != nil {
			return
		}
		if capabilities.AccountRead.State == domain.AgyCapabilitySupported {
			for _, record := range records {
				if record.Snapshot.Status == domain.AgyAccountStatusValid {
					_, _ = s.agyAccounts.ensureAuthentication(s.agyAccounts.ctx, record, domain.AgentReadinessPurposeDisplay)
				}
			}
		}
		_ = s.agyAccounts.capacity.ensure(s.agyAccounts.ctx, records, capabilities, false)
	}()
}

// WaitAgyAccountStoreReady waits only for AO-owned local account state. It
// never starts Agy or inspects the device-global credential.
func (s *Service) WaitAgyAccountStoreReady(ctx context.Context) error {
	if s.agyAccounts == nil {
		return apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	err := s.agyAccounts.waitAccountStore(ctx)
	if err == nil {
		return nil
	}
	var failure *agyAccountLocalFailure
	if !errors.As(err, &failure) {
		return err
	}
	return apierr.New(apierr.KindUnavailable, "AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account setup did not complete", map[string]any{
		"reasonCode": failure.reason, "retryable": failure.retryable,
	})
}

// EnsureAgyDeviceAccountReconciled conclusively identifies the canonical
// device account before an operation is allowed to mutate it.
func (s *Service) EnsureAgyDeviceAccountReconciled(ctx context.Context) error {
	if err := s.WaitAgyAccountStoreReady(ctx); err != nil {
		return err
	}
	err := s.agyAccounts.reconcileGlobal(ctx)
	s.agyAccounts.mu.Lock()
	state := s.agyAccounts.reconciliation
	s.agyAccounts.mu.Unlock()
	if err == nil && state.Status == domain.AgyDeviceReconciliationVerified {
		return nil
	}
	reason, retryable := state.ReasonCode, state.Retryable
	if err != nil {
		failure := agyClassifyDeviceReconciliationFailure(err)
		reason, retryable = failure.reason, failure.retryable
	}
	if strings.TrimSpace(reason) == "" {
		reason = "account_reconciliation_unavailable"
	}
	return apierr.New(apierr.KindUnavailable, "AGY_DEVICE_ACCOUNT_UNVERIFIED", "The device Agy account could not be verified", map[string]any{
		"reasonCode": reason, "retryable": retryable,
	})
}

// BeginAgyAccountMutation gives the local switch worker exclusive ownership
// of the credential mutation path for the complete transaction.
func (s *Service) BeginAgyAccountMutation(ctx context.Context) error {
	if s.agyAccounts == nil {
		return apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	_, err := s.agyAccounts.acquireAccountMutation(ctx)
	return err
}

// EndAgyAccountMutation releases the switch-owned credential mutation path.
func (s *Service) EndAgyAccountMutation() {
	if s.agyAccounts == nil {
		return
	}
	select {
	case s.agyAccounts.mutations <- struct{}{}:
	default:
	}
}

// AgyAccountLoginInProgress reports whether a native login is still open.
func (s *Service) AgyAccountLoginInProgress() bool {
	if s.agyAccounts == nil {
		return false
	}
	s.agyAccounts.mu.Lock()
	defer s.agyAccounts.mu.Unlock()
	return s.agyAccounts.login != nil && !agyTerminalLoginStatus(s.agyAccounts.login.snapshot.Status)
}

// PrepareAgyAccountForSwitch snapshots the stable source and target
// credentials before the durable journal is created. Provider state is not
// consulted; the staged files are the complete local recovery evidence.
func (s *Service) PrepareAgyAccountForSwitch(ctx context.Context, switchID, accountID string) (domain.AgyAccountSwitchSource, error) {
	notPrepared := func(err error) (domain.AgyAccountSwitchSource, error) {
		return domain.AgyAccountSwitchSource{}, errors.Join(ports.ErrAgyAccountSwitchNotCommitted, err)
	}
	if s.agyAccounts == nil {
		return notPrepared(apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable"))
	}
	if s.AgyAccountLoginInProgress() {
		return notPrepared(apierr.Conflict("AGY_ACCOUNT_LOGIN_IN_PROGRESS", "Finish or close the Agy account login before switching accounts", nil))
	}
	if !isCanonicalUUIDv4(strings.TrimSpace(switchID)) {
		return notPrepared(apierr.Invalid("INVALID_AGY_ACCOUNT_ID", "Invalid Agy account switch identifier", nil))
	}
	record, ok := s.agyAccounts.catalog.record(strings.TrimSpace(accountID))
	if !ok || record.Snapshot.Status != domain.AgyAccountStatusValid {
		return notPrepared(apierr.NotFound("AGY_ACCOUNT_NOT_FOUND", "Agy account not found"))
	}
	if err := ctx.Err(); err != nil {
		return notPrepared(err)
	}
	credentialPath := filepath.Join(record.Home, agyCredentialFilename)
	credential, admitted, credentialErr := readCodexFileState(credentialPath, false)
	if credentialErr != nil {
		s.agyAccounts.requireReauthentication(record.Snapshot.ID)
		return notPrepared(apierr.Conflict("AGY_ACCOUNT_REAUTHENTICATION_REQUIRED", "Sign in again before switching to this Agy account", nil))
	}
	latestCredential, latest, latestErr := readCodexFileState(credentialPath, false)
	if latestErr != nil || !sameCodexFileState(admitted, latest) || !bytes.Equal(credential, latestCredential) {
		return notPrepared(apierr.Conflict("AGY_ACCOUNT_IDENTITY_CHANGED", "The Agy account changed while preparing the switch. Try again", nil))
	}
	if !agyLocalCredentialIdentifiesRecord(record, latestCredential) {
		return notPrepared(apierr.Conflict("AGY_ACCOUNT_IDENTITY_CHANGED", "The saved Agy account no longer matches its credential. Sign in again", nil))
	}
	_ = s.agyAccounts.catalog.updateCredentialIdentity(ctx, record.Snapshot.ID, latestCredential)

	stagingDir := filepath.Join(s.agyAccounts.switchStagingRoot, switchID)
	if err := ensurePrivateDirectory(stagingDir); err != nil {
		return notPrepared(apierr.Unavailable("AGY_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The Agy credential switch could not be staged"))
	}
	if err := writePrivateFileAtomic(filepath.Join(stagingDir, "target-auth.json"), latestCredential); err != nil {
		_ = os.RemoveAll(stagingDir)
		return notPrepared(apierr.Unavailable("AGY_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The selected Agy credential could not be staged"))
	}

	globalCredential, globalState, globalErr := readCodexFileState(s.agyAccounts.globalCredentialPath(), true)
	if globalErr != nil {
		_ = os.RemoveAll(stagingDir)
		return notPrepared(apierr.NotImplemented("AGY_GLOBAL_CREDENTIAL_STORE_UNSUPPORTED", "Device-global Agy account switching requires file-backed credentials"))
	}
	source := domain.AgyAccountSwitchSource{Kind: domain.AgyAccountSwitchSourceNone}
	if globalState.exists {
		identity, identityErr := parseAgyCredentialIdentity(globalCredential)
		matched, match := s.agyAccounts.matchGlobalCredentialForReconciliation(globalCredential, identity, identityErr)
		source.Kind = domain.AgyAccountSwitchSourceDevice
		if match == agyCredentialMatchManaged {
			source.Kind = domain.AgyAccountSwitchSourceManaged
			source.AccountID = matched.Snapshot.ID
		}
		if err := writePrivateFileAtomic(filepath.Join(stagingDir, "source-auth.json"), globalCredential); err != nil {
			_ = os.RemoveAll(stagingDir)
			return notPrepared(apierr.Unavailable("AGY_ACCOUNT_SWITCH_ACTIVATION_UNCONFIRMED", "The current Agy credential could not be checkpointed"))
		}
	}
	finalGlobal, finalState, finalErr := readCodexFileState(s.agyAccounts.globalCredentialPath(), true)
	if finalErr != nil || !sameCodexFileState(globalState, finalState) || !bytes.Equal(globalCredential, finalGlobal) {
		_ = os.RemoveAll(stagingDir)
		return notPrepared(ports.ErrAgyGlobalAccountChanged)
	}
	return source, nil
}

// InspectAgyAccountSwitch classifies the stable device credential against
// the staged target and source. An error means local inspection was
// inconclusive and the durable switch fence must be retained.
func (s *Service) InspectAgyAccountSwitch(ctx context.Context, switchID string, sourceKind domain.AgyAccountSwitchSourceKind, sourceAccountID, accountID string) (domain.AgyAccountSwitchInstallationState, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.agyAccounts == nil {
		return "", apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable")
	}
	switchID = strings.TrimSpace(switchID)
	if !isCanonicalUUIDv4(switchID) {
		return "", apierr.Invalid("INVALID_AGY_ACCOUNT_ID", "Invalid Agy account switch identifier", nil)
	}
	accountID = strings.TrimSpace(accountID)
	if err := s.agyAccounts.validateGlobalCredentialStore(); err != nil {
		return "", apierr.NotImplemented("AGY_GLOBAL_CREDENTIAL_STORE_UNSUPPORTED", "Device-global Agy account switching requires file-backed credentials")
	}
	globalPath := s.agyAccounts.globalCredentialPath()
	globalCredential, admitted, credentialErr := readCodexFileState(globalPath, true)
	if credentialErr != nil {
		return "", credentialErr
	}
	latestCredential, latest, latestErr := readCodexFileState(globalPath, true)
	if latestErr != nil || !sameCodexFileState(admitted, latest) || !bytes.Equal(globalCredential, latestCredential) {
		return "", errors.Join(ports.ErrAgyGlobalAccountChanged, latestErr)
	}
	if !admitted.exists {
		return domain.AgyAccountSwitchCredentialMissing, nil
	}
	targetCredential, targetErr := readOpaqueCredential(filepath.Join(s.agyAccounts.switchStagingRoot, switchID, "target-auth.json"))
	targetFromStaging := targetErr == nil
	var targetRecord agyAccountRecord
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return "", targetErr
	}
	if !targetFromStaging {
		if record, ok := s.agyAccounts.catalog.record(accountID); ok && record.Snapshot.Status == domain.AgyAccountStatusValid {
			targetRecord = record
			targetCredential, targetErr = readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
			if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
				return "", targetErr
			}
		}
	}
	if targetErr == nil && bytes.Equal(globalCredential, targetCredential) && (targetFromStaging || agyLocalCredentialIdentifiesRecord(targetRecord, globalCredential)) {
		return domain.AgyAccountSwitchTargetInstalled, nil
	}
	if sourceKind != domain.AgyAccountSwitchSourceNone {
		sourceCredential, sourceErr := readOpaqueCredential(filepath.Join(s.agyAccounts.switchStagingRoot, switchID, "source-auth.json"))
		if sourceErr == nil && bytes.Equal(globalCredential, sourceCredential) {
			return domain.AgyAccountSwitchSourceInstalled, nil
		}
		if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
			return "", sourceErr
		}
		if sourceAccountID = strings.TrimSpace(sourceAccountID); sourceAccountID != "" {
			if source, found := s.agyAccounts.catalog.record(sourceAccountID); found && agyLocalCredentialIdentifiesRecord(source, globalCredential) {
				return domain.AgyAccountSwitchSourceInstalled, nil
			}
		}
	}
	return domain.AgyAccountSwitchExternalCredential, nil
}

// ActivatePreparedAgyAccountSwitch performs the single compare-and-swap
// from the staged source to the staged target.
func (s *Service) ActivatePreparedAgyAccountSwitch(ctx context.Context, sourceKind domain.AgyAccountSwitchSourceKind, switchID, targetID string) error {
	notCommitted := func(err error) error {
		return errors.Join(ports.ErrAgyAccountSwitchNotCommitted, err)
	}
	if s.agyAccounts == nil {
		return notCommitted(apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account management is unavailable"))
	}
	if !isCanonicalUUIDv4(strings.TrimSpace(switchID)) {
		return notCommitted(apierr.Invalid("INVALID_AGY_ACCOUNT_ID", "Invalid Agy account switch identifier", nil))
	}
	stagingDir := filepath.Join(s.agyAccounts.switchStagingRoot, switchID)
	if sourceKind != domain.AgyAccountSwitchSourceManaged && sourceKind != domain.AgyAccountSwitchSourceDevice && sourceKind != domain.AgyAccountSwitchSourceNone {
		return notCommitted(apierr.Invalid("INVALID_AGY_ACCOUNT_SWITCH_SOURCE", "Invalid Agy account switch source", nil))
	}
	target, ok := s.agyAccounts.catalog.record(strings.TrimSpace(targetID))
	if !ok || target.Snapshot.Status != domain.AgyAccountStatusValid {
		return notCommitted(apierr.NotFound("AGY_ACCOUNT_NOT_FOUND", "Agy account not found"))
	}
	targetCredential, targetState, targetErr := readCodexFileState(filepath.Join(stagingDir, "target-auth.json"), false)
	latestTarget, latestTargetState, latestTargetErr := readCodexFileState(filepath.Join(stagingDir, "target-auth.json"), false)
	if targetErr != nil || latestTargetErr != nil || !sameCodexFileState(targetState, latestTargetState) || !bytes.Equal(targetCredential, latestTarget) || !agyLocalCredentialIdentifiesRecord(target, latestTarget) {
		s.agyAccounts.requireReauthentication(target.Snapshot.ID)
		return notCommitted(apierr.Conflict("AGY_ACCOUNT_REAUTHENTICATION_REQUIRED", "Sign in again before switching to this Agy account", nil))
	}
	var expectedGlobal []byte
	if sourceKind == domain.AgyAccountSwitchSourceNone {
		expectedGlobal = []byte{}
	} else {
		var sourceErr error
		expectedGlobal, sourceErr = readOpaqueCredential(filepath.Join(stagingDir, "source-auth.json"))
		if sourceErr != nil {
			return notCommitted(apierr.Conflict("AGY_GLOBAL_ACCOUNT_CHANGED", "The device Agy account changed before switching", nil))
		}
	}
	if err := ctx.Err(); err != nil {
		return notCommitted(err)
	}
	err := s.agyAccounts.activateFromCredentialLocked(ctx, strings.TrimSpace(targetID), filepath.Join(stagingDir, "target-auth.json"), expectedGlobal)
	if err == nil {
		s.readiness.Invalidate(string(domain.HarnessAgy), readinessInvalidateAuthentication)
	}
	return err
}

// CleanupAgyAccountSwitch removes private switch staging after the durable
// switch has reached a locally confirmed terminal phase.
func (s *Service) CleanupAgyAccountSwitch(_ context.Context, switchID string) error {
	if s.agyAccounts == nil || !isCanonicalUUIDv4(strings.TrimSpace(switchID)) {
		return nil
	}
	return os.RemoveAll(filepath.Join(s.agyAccounts.switchStagingRoot, switchID))
}

// CleanupInactiveAgyAccountSwitches removes credential staging that has no
// live durable journal. The active switch directory, when supplied, is kept.
func (s *Service) CleanupInactiveAgyAccountSwitches(ctx context.Context, activeSwitchID string) error {
	if s.agyAccounts == nil {
		return nil
	}
	entries, err := os.ReadDir(s.agyAccounts.switchStagingRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	activeSwitchID = strings.TrimSpace(activeSwitchID)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if !entry.IsDir() || !isCanonicalUUIDv4(name) || name == activeSwitchID {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.agyAccounts.switchStagingRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

var _ ports.AgyAccountCredentialManager = (*Service)(nil)

// AgyAccountSwitchInProgress reports whether the credential coordinator owns
// the device-global mutation gate.
func (s *Service) AgyAccountSwitchInProgress() bool {
	return s.agySwitches != nil && s.agySwitches.AgyAccountSwitchInProgress()
}

// StartAgyAccountSwitch starts a durable device credential switch.
func (s *Service) StartAgyAccountSwitch(ctx context.Context, cfg ports.AgyAccountSwitchConfig) (domain.AgyAccountSwitch, error) {
	if s.agySwitches == nil {
		return domain.AgyAccountSwitch{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account switching is unavailable")
	}
	return s.agySwitches.StartAgyAccountSwitch(ctx, cfg)
}

// GetAgyAccountSwitch returns the durable result for one switch. The UI uses
// this journal state instead of inferring success from a reconciliation snapshot.
func (s *Service) GetAgyAccountSwitch(ctx context.Context, id string) (domain.AgyAccountSwitch, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domain.AgyAccountSwitch{}, apierr.Invalid("AGY_ACCOUNT_SWITCH_ID_REQUIRED", "Agy account switch ID is required", nil)
	}
	if s.agySwitches == nil {
		return domain.AgyAccountSwitch{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account switching is unavailable")
	}
	sw, ok, err := s.agySwitches.GetAgyAccountSwitch(ctx, id)
	if err != nil {
		return domain.AgyAccountSwitch{}, err
	}
	if !ok {
		return domain.AgyAccountSwitch{}, apierr.NotFound("AGY_ACCOUNT_SWITCH_NOT_FOUND", "Agy account switch not found")
	}
	return sw, nil
}

// ReconcileAgyAccountSwitches starts best-effort local settlement for any
// durable switch journal. Recovery failure never blocks unrelated daemon work.
func (s *Service) ReconcileAgyAccountSwitches(ctx context.Context) error {
	if s.agySwitches == nil {
		return nil
	}
	return s.agySwitches.ReconcileAgyAccountSwitches(ctx)
}

// WaitAgyAccountSwitchWorkers drains credential workers during shutdown.
func (s *Service) WaitAgyAccountSwitchWorkers(ctx context.Context) error {
	if s.agySwitches == nil {
		return nil
	}
	return s.agySwitches.Wait(ctx)
}
