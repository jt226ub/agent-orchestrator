package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

const (
	agyAccountDisplayTTL       = 5 * time.Minute
	agyAccountLaunchTTL        = 30 * time.Second
	agyAccountAuthTimeout      = 10 * time.Second
	agyAccountReconcileTimeout = 45 * time.Second
	// The CLI's browser sign-in is short-lived; the daemon verifies the
	// staged token on its own every poll interval within the lifetime.
	agyAccountLoginLifetime     = 5 * time.Minute
	agyAccountLoginPollInterval = 2 * time.Second
	agyAccountProcessLimit      = 2
)

// AgyAccounts is the display-safe account-management view. Credentials and
// filesystem locations remain daemon-private.
type AgyAccounts struct {
	ActiveAccountID      string                         `json:"activeAccountId,omitempty"`
	AccountRevision      int64                          `json:"accountRevision"`
	Accounts             []domain.AgyAccountSnapshot    `json:"accounts"`
	Capabilities         domain.AgyAccountCapabilities  `json:"capabilities"`
	DeviceReconciliation domain.AgyDeviceReconciliation `json:"deviceReconciliation"`
	ActiveLogin          *AgyActiveLogin                `json:"activeLogin,omitempty"`
	CurrentSwitch        *domain.AgyAccountSwitch       `json:"currentSwitch,omitempty"`
}

// AgyActiveLogin is the safe in-memory login state needed to reattach the
// Settings terminal after a renderer remount.
type AgyActiveLogin struct {
	OperationID   string                       `json:"operationId"`
	AccountID     string                       `json:"accountId,omitempty"`
	Status        domain.AgyAccountLoginStatus `json:"status"`
	ReasonCode    string                       `json:"reasonCode"`
	Reason        string                       `json:"reason"`
	ExpiresAt     time.Time                    `json:"expiresAt"`
	ShellTerminal AgyLoginTerminalDisplay      `json:"shellTerminal"`
}

// AgyLoginTerminalDisplay excludes the terminal command and filesystem
// context while retaining the mux identity and display metadata.
type AgyLoginTerminalDisplay struct {
	HandleID  string    `json:"handleId"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
}

// AgyAccountLoginTerminalStart combines safe login state with its trusted terminal.
type AgyAccountLoginTerminalStart struct {
	Operation     domain.AgyAccountLoginOperation `json:"operation"`
	ShellTerminal shellterm.ShellTerminal         `json:"shellTerminal"`
}

type agyAccountLoginTerminalService interface {
	OpenCommandTerminal(context.Context, shellterm.OpenCommandTerminalInput) (shellterm.ShellTerminal, error)
	CloseShellTerminal(context.Context, string) error
}

type agyAccountAuthCall struct {
	done     chan struct{}
	previous domain.AgentAuthenticationObservation
	retry    bool
}
type agyAccountAuthState struct {
	invalidated bool
	// launchVerified reports that a protected Agy account call succeeded.
	// account/read only discovers local account metadata and never sets it.
	launchVerified           bool
	reauthenticationRequired bool
	failures                 int
	nextRetryAt              time.Time
	call                     *agyAccountAuthCall
}
type agyAccountReconcileCall struct {
	done chan struct{}
	err  error
}
type agyAccountLoginOperation struct {
	snapshot                 domain.AgyAccountLoginOperation
	targetAccountID          string
	deviceState              codexFileState
	startingGlobalCredential []byte
	targetWasActive          bool
	pendingDir               string
	home                     string
	terminalHandle           string
	terminalTitle            string
	terminalCreated          time.Time
	closing                  bool
	committing               bool
	commitDone               chan struct{}
}

type agyAccountManager struct {
	ctx               context.Context
	catalog           *agyAccountCatalog
	factory           ports.AgyAccountClientFactory
	operationGate     ports.AgyOperationGate
	logger            *slog.Logger
	now               func() time.Time
	after             func(time.Duration) <-chan time.Time
	newID             func() string
	processes         chan struct{}
	mutations         chan struct{}
	executable        func() (string, error)
	terminal          agyAccountLoginTerminalService
	globalHome        string
	pendingRoot       string
	switchStagingRoot string

	mu                      sync.Mutex
	accountStoreCall        *agyAccountReconcileCall
	accountStoreErr         error
	accountStoreNextRetry   time.Time
	accountStoreFailures    int
	auth                    map[string]*agyAccountAuthState
	capabilities            domain.AgyAccountCapabilities
	snapshotRevision        int64
	deviceAccountID         string
	deferredAccountID       string
	deviceCredentialPresent bool
	login                   *agyAccountLoginOperation
	reconcile               *agyAccountReconcileCall
	reconcileRequested      bool
	reconciliation          domain.AgyDeviceReconciliation
	reconcileFailures       int
	reconcileScheduled      bool
	accountStoreReady       bool
	capacity                *agyAccountCapacityCoordinator
	onAuthenticationChanged func()
	subscribers             map[chan AgyAccounts]struct{}
}

func newAgyAccountManager(ctx context.Context, accountRoot, pendingRoot, switchStagingRoot, globalHome string, factory ports.AgyAccountClientFactory, logger *slog.Logger, operationGates ...ports.AgyOperationGate) *agyAccountManager {
	if ctx == nil {
		ctx = context.Background()
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	var operationGate ports.AgyOperationGate
	if len(operationGates) > 0 {
		operationGate = operationGates[0]
	}
	m := &agyAccountManager{
		ctx: ctx, catalog: newAgyAccountCatalog(accountRoot, logger), factory: factory, operationGate: operationGate,
		logger: logger, now: func() time.Time { return time.Now().UTC() }, newID: uuid.NewString,
		after:     time.After,
		processes: make(chan struct{}, agyAccountProcessLimit), mutations: make(chan struct{}, 1), executable: os.Executable,
		globalHome: canonicalPath(globalHome), pendingRoot: canonicalPath(pendingRoot), switchStagingRoot: canonicalPath(switchStagingRoot),
		auth:         map[string]*agyAccountAuthState{},
		capabilities: unavailableAgyCapabilities(), subscribers: map[chan AgyAccounts]struct{}{},
		snapshotRevision: time.Now().UTC().UnixNano(),
		reconciliation: domain.AgyDeviceReconciliation{
			Status: domain.AgyDeviceReconciliationNotChecked, ReasonCode: "not_checked",
		},
	}
	m.mutations <- struct{}{}
	m.capacity = newAgyAccountCapacityCoordinator(m)
	m.catalog.setOnRemoved(func(ids []string) { m.capacity.removeAccounts(ids); m.publish() })
	return m
}

func (m *agyAccountManager) acquireAccountMutation(ctx context.Context) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.mutations:
		var once sync.Once
		return func() { once.Do(func() { m.mutations <- struct{}{} }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *agyAccountManager) acquireGlobalRead(ctx context.Context, account ports.AgyAccountContext) (func(), error) {
	if m.operationGate == nil || canonicalPath(account.Home) != m.globalHome {
		return func() {}, nil
	}
	return m.operationGate.AcquireShared(ctx)
}

func (m *agyAccountManager) acquireGlobalMutation(ctx context.Context) (ports.AgyOperationLease, error) {
	if m.operationGate == nil {
		return nil, nil
	}
	return m.operationGate.AcquireExclusive(ctx)
}

func unavailableAgyCapabilities() domain.AgyAccountCapabilities {
	unknown := domain.AgyCapabilityObservation{State: domain.AgyCapabilityUnknown, ReasonCode: domain.AgyCapabilityReasonUnknown, Reason: "Agy capability detection has not completed."}
	return domain.AgyAccountCapabilities{AccountRead: unknown, NativeLogin: unknown, CapacityRead: unknown, GlobalSwitch: unknown}
}

func (m *agyAccountManager) detectCapabilities(ctx context.Context) domain.AgyAccountCapabilities {
	if m.factory == nil {
		return unavailableAgyCapabilities()
	}
	capabilities := m.factory.Capabilities(ctx)
	if key := agyDaemonTokenOverride(); key != "" {
		capabilities.GlobalSwitch = domain.AgyCapabilityObservation{
			State: domain.AgyCapabilityUnsupported, ReasonCode: "daemon_token_override",
			Reason: "Account switching is disabled while " + key + " is set for the daemon.",
		}
	} else if err := m.validateGlobalCredentialStore(); err != nil {
		capabilities.GlobalSwitch = domain.AgyCapabilityObservation{
			State: domain.AgyCapabilityUnsupported, ReasonCode: "global_credential_store_unsupported",
			Reason: "Device-global account switching requires a file-backed Agy sign-in.",
		}
	} else {
		capabilities.GlobalSwitch = domain.AgyCapabilityObservation{
			State: domain.AgyCapabilitySupported, ReasonCode: domain.AgyCapabilityReasonSupported,
			Reason: "AO can switch file-backed Agy credentials on this device.",
		}
	}
	m.mu.Lock()
	m.capabilities = capabilities
	m.mu.Unlock()
	return capabilities
}

func (m *agyAccountManager) view(ids []string) (AgyAccounts, error) {
	records, err := m.catalog.recordsFor(ids)
	if err != nil {
		return AgyAccounts{}, mapUnknownAgyAccount(err)
	}
	m.mu.Lock()
	snapshotRevision, capabilities, reconciliation := m.snapshotRevision, m.capabilities, m.reconciliation
	deviceAccountID := ""
	if reconciliation.ActiveAccountVerified {
		deviceAccountID = m.deviceAccountID
	}
	var activeLogin *AgyActiveLogin
	if m.login != nil && m.login.terminalHandle != "" && !agyTerminalLoginStatus(m.login.snapshot.Status) {
		activeLogin = &AgyActiveLogin{
			OperationID: m.login.snapshot.OperationID, AccountID: m.login.snapshot.AccountID,
			Status: m.login.snapshot.Status, ReasonCode: m.login.snapshot.ReasonCode, Reason: m.login.snapshot.Reason,
			ExpiresAt: m.login.snapshot.ExpiresAt,
			ShellTerminal: AgyLoginTerminalDisplay{
				HandleID: m.login.terminalHandle, Title: m.login.terminalTitle, CreatedAt: m.login.terminalCreated,
			},
		}
	}
	m.mu.Unlock()
	accounts := make([]domain.AgyAccountSnapshot, 0, len(records))
	for _, record := range records {
		snapshot := record.Snapshot
		snapshot.Active = deviceAccountID != "" && snapshot.ID == deviceAccountID
		snapshot.Capacity = m.capacity.snapshot(snapshot.ID)
		accounts = append(accounts, snapshot)
	}
	if deviceAccountID != "" {
		for i := range accounts {
			if accounts[i].ID == deviceAccountID && i > 0 {
				item := accounts[i]
				copy(accounts[1:i+1], accounts[0:i])
				accounts[0] = item
				break
			}
		}
	}
	return AgyAccounts{ActiveAccountID: deviceAccountID, AccountRevision: snapshotRevision, Accounts: accounts, Capabilities: capabilities, DeviceReconciliation: reconciliation, ActiveLogin: activeLogin}, nil
}

func (m *agyAccountManager) cached() AgyAccounts { result, _ := m.view(nil); return result }

func (m *agyAccountManager) accountContext(record agyAccountRecord) ports.AgyAccountContext {
	if record.useSavedHome {
		return ports.AgyAccountContext{Home: record.Home, Managed: true}
	}
	home := record.Home
	m.mu.Lock()
	active, associated := m.deviceAccountID, m.reconciliation.ActiveAccountVerified
	m.mu.Unlock()
	// The global home is safe for a saved slot only after the current device
	// credential has been positively associated with that exact slot.
	if associated && record.Snapshot.ID == active {
		return ports.AgyAccountContext{Home: m.globalHome, Managed: false}
	}
	return ports.AgyAccountContext{Home: home, Managed: true}
}

func (m *agyAccountManager) deferAccountRead(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	associated := m.reconciliation.ActiveAccountVerified && id == m.deviceAccountID
	return id != "" && id == m.deferredAccountID && !associated
}

func (m *agyAccountManager) ensure(ctx context.Context, ids []string, forceAuthentication bool, installation domain.AgentInstallationState) (AgyAccounts, error) {
	if err := m.catalog.refresh(); err != nil {
		return AgyAccounts{}, apierr.Unavailable("AGY_ACCOUNT_MANAGEMENT_UNAVAILABLE", "Agy account discovery is unavailable")
	}
	records, err := m.catalog.recordsFor(ids)
	if err != nil {
		return AgyAccounts{}, mapUnknownAgyAccount(err)
	}
	eligible := make([]agyAccountRecord, 0, len(records))
	for _, record := range records {
		// A targeted metadata refresh belongs to the saved account, not the
		// device-global credential. Keeping it isolated avoids turning a row
		// expansion into a device-account state transition.
		record.useSavedHome = len(ids) > 0
		if !m.deferAccountRead(record.Snapshot.ID) {
			eligible = append(eligible, record)
		}
	}
	if installation == domain.AgentInstallationNotInstalled {
		m.recordAuthenticationUnavailable(eligible, domain.AgentReadinessReasonAuthSkippedNotInstalled, "Authentication was not checked because Agy is not installed.")
		return m.view(ids)
	}
	capabilities := m.detectCapabilities(ctx)
	if capabilities.AccountRead.State != domain.AgyCapabilitySupported {
		code, reason := domain.AgentReadinessReasonAuthCheckInconclusive, "Authentication could not be checked."
		if capabilities.AccountRead.State == domain.AgyCapabilityUnsupported {
			code, reason = domain.AgentReadinessReasonAuthCheckUnsupported, "This Agy version cannot check authentication."
		}
		m.recordAuthenticationUnavailable(eligible, code, reason)
		return m.view(ids)
	}
	if forceAuthentication {
		m.forceAuthenticationRetry(eligible)
	}
	for _, record := range eligible {
		if record.Snapshot.Status == domain.AgyAccountStatusValid {
			if _, err := m.ensureAuthentication(ctx, record, domain.AgentReadinessPurposeDisplay); err != nil {
				return AgyAccounts{}, err
			}
		}
	}
	records, err = m.catalog.recordsFor(ids)
	if err != nil {
		return AgyAccounts{}, mapUnknownAgyAccount(err)
	}
	// Authentication checks above update the catalog snapshots. Capacity must
	// consume those refreshed observations, while still excluding the one stale
	// pointer whose device ownership has not been established.
	eligible = eligible[:0]
	for _, record := range records {
		record.useSavedHome = len(ids) > 0
		if !m.deferAccountRead(record.Snapshot.ID) {
			eligible = append(eligible, record)
		}
	}
	if err := m.capacity.ensure(ctx, eligible, capabilities, forceAuthentication); err != nil && !errors.Is(err, context.Canceled) {
		m.logger.Debug("Agy account capacity ensure degraded", "failure_category", "capacity_read")
	}
	result, err := m.view(ids)
	m.publish()
	return result, err
}

func (m *agyAccountManager) recordAuthenticationUnavailable(records []agyAccountRecord, code, reason string) {
	attempted := m.now()
	observation := failedAuthentication(attempted, code, reason)
	for _, record := range records {
		if record.Snapshot.Status != domain.AgyAccountStatusValid {
			continue
		}
		m.catalog.updateSnapshot(record.Snapshot.ID, func(snapshot *domain.AgyAccountSnapshot) {
			preserveAuthenticationFailure(&snapshot.Authentication, observation)
		})
	}
	m.publish()
}

func (m *agyAccountManager) forceAuthenticationRetry(records []agyAccountRecord) {
	m.mu.Lock()
	for _, record := range records {
		if record.Snapshot.Status != domain.AgyAccountStatusValid {
			continue
		}
		state := m.auth[record.Snapshot.ID]
		if state == nil {
			state = &agyAccountAuthState{}
			m.auth[record.Snapshot.ID] = state
		}
		state.invalidated = true
		state.nextRetryAt = time.Time{}
	}
	m.mu.Unlock()
	for _, record := range records {
		if record.Snapshot.Status == domain.AgyAccountStatusValid {
			m.capacity.invalidate(record.Snapshot.ID, false)
		}
	}
}

func (m *agyAccountManager) ensureAuthentication(ctx context.Context, record agyAccountRecord, purpose domain.AgentReadinessPurpose) (domain.AgentAuthenticationObservation, error) {
	for {
		if err := ctx.Err(); err != nil {
			return domain.AgentAuthenticationObservation{}, err
		}
		m.mu.Lock()
		state := m.auth[record.Snapshot.ID]
		if state == nil {
			state = &agyAccountAuthState{invalidated: true}
			m.auth[record.Snapshot.ID] = state
		}
		current, _ := m.catalog.record(record.Snapshot.ID)
		if state.reauthenticationRequired {
			out := current.Snapshot.Authentication
			m.mu.Unlock()
			return out, nil
		}
		ttl := agyAccountDisplayTTL
		if purpose == domain.AgentReadinessPurposeLaunch {
			ttl = agyAccountLaunchTTL
		}
		fresh := current.Snapshot.Authentication.CheckedAt != nil && m.now().Sub(*current.Snapshot.Authentication.CheckedAt) < ttl
		if !state.invalidated && fresh {
			out := current.Snapshot.Authentication
			m.mu.Unlock()
			return out, nil
		}
		if purpose == domain.AgentReadinessPurposeDisplay && !state.nextRetryAt.IsZero() && m.now().Before(state.nextRetryAt) {
			out := current.Snapshot.Authentication
			m.mu.Unlock()
			return out, nil
		}
		if state.call != nil {
			call := state.call
			m.mu.Unlock()
			select {
			case <-call.done:
				if call.retry {
					continue
				}
				latest, _ := m.catalog.record(record.Snapshot.ID)
				return latest.Snapshot.Authentication, nil
			case <-ctx.Done():
				return domain.AgentAuthenticationObservation{}, ctx.Err()
			}
		}
		call := &agyAccountAuthCall{done: make(chan struct{}), previous: current.Snapshot.Authentication}
		state.call = call
		m.catalog.updateSnapshot(record.Snapshot.ID, func(s *domain.AgyAccountSnapshot) { s.Authentication.Freshness = domain.AgentReadinessChecking })
		m.mu.Unlock()
		go m.runAuthentication(record, call)
		select {
		case <-call.done:
			if call.retry {
				continue
			}
			latest, _ := m.catalog.record(record.Snapshot.ID)
			return latest.Snapshot.Authentication, nil
		case <-ctx.Done():
			return domain.AgentAuthenticationObservation{}, ctx.Err()
		}
	}
}

func (m *agyAccountManager) runAuthentication(record agyAccountRecord, call *agyAccountAuthCall) {
	attempted := m.now()
	ctx, cancel := context.WithTimeout(m.ctx, agyAccountAuthTimeout)
	defer cancel()
	select {
	case m.processes <- struct{}{}:
		defer func() { <-m.processes }()
	case <-ctx.Done():
		m.finishAuthentication(ctx, record.Snapshot.ID, failedAuthentication(attempted, domain.AgentReadinessReasonAuthCheckFailed, "Authentication check stopped."), domain.AgyAuthMethodUnknown, nil, true, call)
		return
	}
	account := m.accountContext(record)
	releaseGlobal, err := m.acquireGlobalRead(ctx, account)
	if err != nil {
		m.finishAuthentication(ctx, record.Snapshot.ID, failedAuthentication(attempted, domain.AgentReadinessReasonAuthCheckFailed, "Authentication check stopped."), domain.AgyAuthMethodUnknown, nil, true, call)
		return
	}
	releasedGlobal := false
	releaseGlobalOnce := func() {
		if !releasedGlobal {
			releaseGlobal()
			releasedGlobal = true
		}
	}
	defer releaseGlobalOnce()
	if m.globalCredentialMissingFor(account) {
		releaseGlobalOnce()
		_ = m.reconcileGlobalWithPolicy(m.ctx, true)
		m.retryAuthenticationAfterDeviceChange(record.Snapshot.ID, call)
		return
	}
	client, err := m.factory.Open(ctx, account)
	if err != nil {
		if m.globalCredentialMissingFor(account) {
			releaseGlobalOnce()
			_ = m.reconcileGlobalWithPolicy(m.ctx, true)
			m.retryAuthenticationAfterDeviceChange(record.Snapshot.ID, call)
			return
		}
		m.finishAuthentication(ctx, record.Snapshot.ID, failedAuthentication(attempted, domain.AgentReadinessReasonAuthCheckFailed, "Authentication check failed."), domain.AgyAuthMethodUnknown, nil, true, call)
		return
	}
	clientClosed := false
	closeClientOnce := func() {
		if !clientClosed {
			_ = client.Close()
			clientClosed = true
		}
	}
	defer closeClientOnce()
	// account/read is metadata discovery only. Remote authentication is proved
	// separately by a protected account call.
	observation, err := client.Read(ctx)
	if m.globalCredentialMissingFor(account) {
		closeClientOnce()
		releaseGlobalOnce()
		_ = m.reconcileGlobalWithPolicy(m.ctx, true)
		m.retryAuthenticationAfterDeviceChange(record.Snapshot.ID, call)
		return
	}
	if err != nil {
		code, reason := domain.AgentReadinessReasonAuthCheckFailed, "Authentication check failed."
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code, reason = domain.AgentReadinessReasonAuthCheckTimeout, "Authentication check timed out."
		}
		m.finishAuthentication(ctx, record.Snapshot.ID, failedAuthentication(attempted, code, reason), domain.AgyAuthMethodUnknown, nil, true, call)
		return
	}
	result := agyAccountAuthenticationObservation(m.now(), observation.Authentication)
	m.finishAuthentication(ctx, record.Snapshot.ID, result, observation.Method, observation.Email, observation.Authentication == domain.AgentAuthenticationUnknown, call)
}

func (m *agyAccountManager) globalCredentialMissingFor(account ports.AgyAccountContext) bool {
	if canonicalPath(account.Home) != m.globalHome {
		return false
	}
	_, state, err := readCodexFileState(m.globalCredentialPath(), true)
	return err == nil && !state.exists
}

func (m *agyAccountManager) retryAuthenticationAfterDeviceChange(id string, call *agyAccountAuthCall) {
	m.mu.Lock()
	state := m.auth[id]
	if state != nil && state.call == call {
		m.catalog.updateSnapshot(id, func(snapshot *domain.AgyAccountSnapshot) {
			snapshot.Authentication = call.previous
		})
		state.invalidated = true
		state.nextRetryAt = time.Time{}
		state.call = nil
		call.retry = true
		close(call.done)
	}
	m.mu.Unlock()
	m.publish()
}

func agyAccountAuthenticationObservation(at time.Time, state domain.AgentAuthenticationState) domain.AgentAuthenticationObservation {
	switch state {
	case domain.AgentAuthenticationAuthorized:
		return successfulAuthentication(at, state, domain.AgentReadinessReasonAuthorized, "Agy appears signed in.")
	case domain.AgentAuthenticationUnauthorized:
		return successfulAuthentication(at, state, domain.AgentReadinessReasonUnauthorized, "Agy needs authentication.")
	case domain.AgentAuthenticationNotApplicable:
		return successfulAuthentication(at, state, domain.AgentReadinessReasonAuthNotApplicable, "Agy authentication is not required.")
	default:
		return failedAuthentication(at, domain.AgentReadinessReasonAuthCheckInconclusive, "Authentication check was inconclusive.")
	}
}

func (m *agyAccountManager) finishAuthentication(ctx context.Context, id string, observation domain.AgentAuthenticationObservation, method domain.AgyAuthMethod, email *string, failed bool, call *agyAccountAuthCall) {
	m.mu.Lock()
	state := m.auth[id]
	if !state.reauthenticationRequired {
		if failed {
			m.catalog.updateSnapshot(id, func(s *domain.AgyAccountSnapshot) { preserveAuthenticationFailure(&s.Authentication, observation) })
			state.invalidated = true
			state.failures++
			if state.failures <= len(defaultReadinessRetryDelays) {
				state.nextRetryAt = m.now().Add(defaultReadinessRetryDelays[state.failures-1])
			}
		} else {
			// An unauthorized read reports no account, so it carries no identity. The
			// saved slot still belongs to the same account, and discarding its label
			// would both hide who must sign in again and break global reconciliation's
			// identity match.
			identified := observation.State == domain.AgentAuthenticationAuthorized || observation.State == domain.AgentAuthenticationNotApplicable
			m.catalog.updateSnapshot(id, func(s *domain.AgyAccountSnapshot) {
				s.Authentication = observation
				if identified {
					s.AuthMethod = method
					s.AccountEmail = email
					s.Label = agyAccountLabel(email)
				}
			})
			if identified {
				// Reconciliation may create the account from local auth.json before
				// Agy supplies display metadata. Persist the first successful
				// account/read result so a later catalog refresh or daemon restart
				// cannot regress the label to the internal account-id fallback.
				if err := m.catalog.updateVerifiedDescriptor(ctx, id, ports.AgyAccountObservation{Method: method, Email: email}); err != nil {
					m.logger.Warn("Agy account display metadata could not be persisted", "accountID", id)
				}
			}
			state.invalidated = false
			state.launchVerified = false
			state.failures = 0
			state.nextRetryAt = time.Time{}
		}
	}
	state.call = nil
	close(call.done)
	m.mu.Unlock()
	m.publish()
}

type agyAuthenticationEvidence uint8

const agyAuthenticationCredentialRejected agyAuthenticationEvidence = 1

// recordProtectedAuthenticationEvidence is the single owner for authentication
// conclusions learned by other protected Agy calls. Capacity only reports the
// typed rejection; it never refreshes credentials or mutates authentication.
func (m *agyAccountManager) recordProtectedAuthenticationEvidence(id string, evidence agyAuthenticationEvidence) {
	if evidence == agyAuthenticationCredentialRejected {
		m.requireReauthentication(id)
	}
}

func (m *agyAccountManager) authenticationVerification(id string) (verified, reauthenticationRequired bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.auth[id]
	if state == nil {
		return false, false
	}
	return state.launchVerified, state.reauthenticationRequired
}

func (m *agyAccountManager) authenticationChanged() {
	if m.onAuthenticationChanged != nil {
		m.onAuthenticationChanged()
	}
	m.publish()
}

func (m *agyAccountManager) invalidate(id string) {
	m.mu.Lock()
	state := m.auth[id]
	if state == nil {
		state = &agyAccountAuthState{}
		m.auth[id] = state
	}
	state.invalidated = true
	state.nextRetryAt = time.Time{}
	m.mu.Unlock()
	m.catalog.updateSnapshot(id, func(s *domain.AgyAccountSnapshot) { s.Authentication.Freshness = domain.AgentReadinessStale })
	m.capacity.invalidate(id, true)
	m.publish()
}

// invalidateCredentialEvidence clears conclusions made about credentials that
// reconciliation has just replaced. In particular, an expired-token result for
// the previous auth.json must not suppress verification of a newly rotated or
// externally refreshed credential for the same account.
func (m *agyAccountManager) invalidateCredentialEvidence(id string) {
	m.mu.Lock()
	state := m.auth[id]
	if state == nil {
		state = &agyAccountAuthState{}
		m.auth[id] = state
	}
	state.reauthenticationRequired = false
	state.launchVerified = false
	state.invalidated = true
	state.failures = 0
	state.nextRetryAt = time.Time{}
	m.mu.Unlock()
	m.catalog.updateSnapshot(id, func(snapshot *domain.AgyAccountSnapshot) {
		snapshot.Authentication = uncheckedAuthentication()
	})
	m.capacity.invalidate(id, true)
	m.publish()
}

func (m *agyAccountManager) requireReauthentication(id string) {
	m.mu.Lock()
	state := m.auth[id]
	if state == nil {
		state = &agyAccountAuthState{}
		m.auth[id] = state
	}
	state.reauthenticationRequired = true
	state.invalidated = false
	state.launchVerified = true
	state.failures = 0
	state.nextRetryAt = time.Time{}
	m.mu.Unlock()
	m.catalog.updateSnapshot(id, func(snapshot *domain.AgyAccountSnapshot) {
		snapshot.Authentication = signedOutAuthentication(m.now(), "Agy could not refresh this account. Sign in again to continue.")
		snapshot.Capacity = unavailableAgyCapacity()
	})
	m.capacity.replace(id, staticAgyCapacity(domain.AgyCapacityUnknown, domain.AgyCapacityReasonSkippedSignedOut, "Sign in to Agy to see subscription capacity."), "reauthentication_required")
	m.authenticationChanged()
}

func (m *agyAccountManager) clearReauthenticationRequired(id string) {
	m.mu.Lock()
	state := m.auth[id]
	if state == nil {
		state = &agyAccountAuthState{}
		m.auth[id] = state
	}
	state.reauthenticationRequired = false
	state.invalidated = false
	state.launchVerified = false
	state.failures = 0
	state.nextRetryAt = time.Time{}
	m.mu.Unlock()
}

func (m *agyAccountManager) subscribe(ctx context.Context) <-chan AgyAccounts {
	ch := make(chan AgyAccounts, 1)
	m.mu.Lock()
	m.subscribers[ch] = struct{}{}
	m.mu.Unlock()
	ch <- m.cached()
	go func() { <-ctx.Done(); m.mu.Lock(); delete(m.subscribers, ch); close(ch); m.mu.Unlock() }()
	return ch
}
func (m *agyAccountManager) publish() {
	m.mu.Lock()
	next := m.now().UnixNano()
	if next <= m.snapshotRevision {
		next = m.snapshotRevision + 1
	}
	m.snapshotRevision = next
	m.mu.Unlock()
	snapshot := m.cached()
	m.mu.Lock()
	defer m.mu.Unlock()
	for ch := range m.subscribers {
		select {
		case ch <- snapshot:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- snapshot:
			default:
			}
		}
	}
}

// agyDaemonTokenOverride names the first daemon environment variable that
// makes the CLI bill a key or token instead of the signed-in plan; while one
// is set, the saved accounts are not what the CLI would use.
func agyDaemonTokenOverride() string {
	for _, key := range []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "JETSKI_OAUTH_TOKEN"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return key
		}
	}
	return ""
}
