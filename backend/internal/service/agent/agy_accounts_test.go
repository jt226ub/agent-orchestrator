package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

type fakeAgyAccountFactory struct {
	mu               sync.Mutex
	opens            int
	capabilityChecks int
	capabilities     domain.AgyAccountCapabilities
	open             func(ports.AgyAccountContext) (ports.AgyAccountClient, error)
}

func (f *fakeAgyAccountFactory) Open(_ context.Context, account ports.AgyAccountContext) (ports.AgyAccountClient, error) {
	f.mu.Lock()
	f.opens++
	open := f.open
	f.mu.Unlock()
	if open == nil {
		return nil, errors.New("unexpected account client open")
	}
	return open(account)
}

func (f *fakeAgyAccountFactory) Capabilities(context.Context) domain.AgyAccountCapabilities {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capabilityChecks++
	return f.capabilities
}

type fakeAgyAccountClient struct {
	read            ports.AgyAccountObservation
	readErr         error
	readFn          func(context.Context) (ports.AgyAccountObservation, error)
	readStarted     chan struct{}
	readRelease     chan struct{}
	capacity        ports.AgyCapacityObservation
	capacityErr     error
	capacityFn      func(context.Context) (ports.AgyCapacityObservation, error)
	capacityStarted chan struct{}
	capacityRelease chan struct{}
}

func (c *fakeAgyAccountClient) Read(ctx context.Context) (ports.AgyAccountObservation, error) {
	if c.readFn != nil {
		return c.readFn(ctx)
	}
	if c.readStarted != nil {
		select {
		case c.readStarted <- struct{}{}:
		default:
		}
	}
	if c.readRelease != nil {
		select {
		case <-c.readRelease:
		case <-ctx.Done():
			return ports.AgyAccountObservation{}, ctx.Err()
		}
	}
	return c.read, c.readErr
}

func (c *fakeAgyAccountClient) ReadCapacity(ctx context.Context) (ports.AgyCapacityObservation, error) {
	if c.capacityStarted != nil {
		select {
		case c.capacityStarted <- struct{}{}:
		default:
		}
	}
	if c.capacityRelease != nil {
		select {
		case <-c.capacityRelease:
		case <-ctx.Done():
			return ports.AgyCapacityObservation{}, ctx.Err()
		}
	}
	if c.capacityFn != nil {
		return c.capacityFn(ctx)
	}
	return c.capacity, c.capacityErr
}

func (c *fakeAgyAccountClient) Close() error { return nil }

type testAgyDeviceAccount struct {
	AccountID   string
	Revision    int64
	ActivatedAt time.Time
	UpdatedAt   time.Time
}

func agySetTestDeviceAccount(manager *agyAccountManager, active testAgyDeviceAccount) {
	manager.deviceAccountID = active.AccountID
	manager.deviceCredentialPresent = active.AccountID != ""
	manager.snapshotRevision = active.Revision
	manager.reconciliation = domain.AgyDeviceReconciliation{
		Status: domain.AgyDeviceReconciliationVerified, ActiveAccountVerified: active.AccountID != "", ReasonCode: "verified",
	}
}

type testAgyDeviceStateSeed interface {
	testDeviceAccount() (testAgyDeviceAccount, bool)
}

type testAgyDeviceSeed struct {
	active testAgyDeviceAccount
	found  bool
}

func (s *testAgyDeviceSeed) testDeviceAccount() (testAgyDeviceAccount, bool) {
	return s.active, s.found
}

type fakeAgyLoginTerminal struct {
	mu              sync.Mutex
	opened          []shellterm.OpenCommandTerminalInput
	closed          []string
	result          shellterm.ShellTerminal
	closeErr        error
	writeCredential bool
	credential      []byte
}

func (f *fakeAgyLoginTerminal) OpenCommandTerminal(_ context.Context, in shellterm.OpenCommandTerminalInput) (shellterm.ShellTerminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, in)
	if f.writeCredential {
		credential := f.credential
		if credential == nil {
			credential = agyTestOAuthCredential("", "opaque-login-credential")
		}
		if err := writePrivateFileAtomic(filepath.Join(in.Env["HOME"], agyCredentialFilename), credential); err != nil {
			return shellterm.ShellTerminal{}, err
		}
	}
	return f.result, nil
}

func (f *fakeAgyLoginTerminal) CloseShellTerminal(_ context.Context, handle string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = append(f.closed, handle)
	return nil
}

func supportedAgyAccountCapabilities() domain.AgyAccountCapabilities {
	supported := domain.AgyCapabilityObservation{State: domain.AgyCapabilitySupported, ReasonCode: domain.AgyCapabilityReasonSupported, Reason: "supported"}
	return domain.AgyAccountCapabilities{
		AccountRead: supported, NativeLogin: supported, CapacityRead: supported, GlobalSwitch: supported,
	}
}

func newTestAgyAccountManager(t *testing.T, factory ports.AgyAccountClientFactory, state testAgyDeviceStateSeed) *agyAccountManager {
	t.Helper()
	root := t.TempDir()
	manager := newAgyAccountManager(context.Background(),
		filepath.Join(root, "accounts"), filepath.Join(root, "pending-accounts"),
		filepath.Join(root, "switch-staging"), filepath.Join(root, "device-home"),
		factory, nil)
	manager.snapshotRevision = 0
	if state != nil {
		if active, ok := state.testDeviceAccount(); ok && active.AccountID != "" {
			manager.deviceAccountID = active.AccountID
			manager.deviceCredentialPresent = true
			manager.snapshotRevision = active.Revision
			manager.reconciliation = domain.AgyDeviceReconciliation{
				Status: domain.AgyDeviceReconciliationVerified, ActiveAccountVerified: true, ReasonCode: "verified",
			}
		}
	}
	return manager
}

func TestCachedAgyAccountsPerformsNoFilesystemOrNativeWork(t *testing.T) {
	factory := &fakeAgyAccountFactory{open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
		t.Fatal("cached account read opened Agy")
		return nil, nil
	}}
	manager := newTestAgyAccountManager(t, factory, nil)
	result := manager.cached()
	if len(result.Accounts) != 0 || result.AccountRevision != 0 {
		t.Fatalf("cached accounts = %#v", result)
	}
	if factory.opens != 0 || factory.capabilityChecks != 0 {
		t.Fatalf("native work: opens=%d capability=%d", factory.opens, factory.capabilityChecks)
	}
}

func TestAgyUnverifiedDeviceAssociationUsesSavedHomeDuringTemporaryReconciliationFailure(t *testing.T) {
	manager := newTestAgyAccountManager(t, nil, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
	})
	manager.mu.Lock()
	agySetTestDeviceAccount(manager, testAgyDeviceAccount{AccountID: record.Snapshot.ID, Revision: 2})
	manager.reconciliation = domain.AgyDeviceReconciliation{
		Status:                domain.AgyDeviceReconciliationTemporarilyUnavailable,
		ActiveAccountVerified: false,
		ReasonCode:            "account_read_inconclusive",
		Retryable:             true,
	}
	manager.mu.Unlock()

	context := manager.accountContext(record)
	if context.Home != record.Home || !context.Managed {
		t.Fatalf("stale active account context = %#v, want isolated saved home", context)
	}
}

func TestAgyCheckingReconciliationKeepsLastMatchedAccountVisibleWithoutUsingGlobalHome(t *testing.T) {
	manager := newTestAgyAccountManager(t, nil, nil)
	inactiveID := "11111111-1111-4111-8111-111111111111"
	ids := []string{inactiveID, agyTestAccountID}
	nextID := 0
	manager.catalog.newID = func() string {
		id := ids[nextID]
		nextID++
		return id
	}
	inactiveEmail := "inactive@example.com"
	agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", []byte("inactive-credential"), ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &inactiveEmail,
	})
	activeEmail := "active@example.com"
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", []byte("active-credential"), ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &activeEmail,
	})
	manager.mu.Lock()
	agySetTestDeviceAccount(manager, testAgyDeviceAccount{AccountID: record.Snapshot.ID, Revision: 2})
	manager.deviceAccountID = record.Snapshot.ID
	manager.deferredAccountID = record.Snapshot.ID
	manager.reconciliation = domain.AgyDeviceReconciliation{
		Status:                domain.AgyDeviceReconciliationChecking,
		ActiveAccountVerified: false,
		ReasonCode:            "checking",
	}
	manager.mu.Unlock()

	view := manager.cached()
	if view.ActiveAccountID != "" || len(view.Accounts) != 2 || view.Accounts[0].Active || view.Accounts[1].Active {
		t.Fatalf("checking reconciliation exposed an unverified active account: %#v", view)
	}
	accountContext := manager.accountContext(record)
	if accountContext.Home != record.Home || !accountContext.Managed {
		t.Fatalf("checking reconciliation used the unverified global home: %#v", accountContext)
	}
}

func TestAgyNativeLoginTerminalUsesOnePrivatePendingHomeAndNoName(t *testing.T) {
	manager := newTestAgyAccountManager(t, nil, nil)
	manager.newID = func() string { return "b60a377d-da68-4a61-86f2-f31f04c571f2" }
	manager.executable = func() (string, error) { return "/Applications/AO.app/Contents/MacOS/ao", nil }
	terminal := &fakeAgyLoginTerminal{result: shellterm.ShellTerminal{HandleID: "shellterm-login-1", Title: "Add Agy account"}}
	manager.terminal = terminal
	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if started.Operation.Status != domain.AgyAccountLoginPending || started.Operation.OperationID == "" {
		t.Fatalf("login start = %#v", started)
	}
	if len(terminal.opened) != 1 {
		t.Fatalf("terminal opens = %d", len(terminal.opened))
	}
	opened := terminal.opened[0]
	if !slices.Equal(opened.Argv, []string{"/Applications/AO.app/Contents/MacOS/ao", "agy-login"}) {
		t.Fatalf("argv = %#v", opened.Argv)
	}
	home := opened.Env["HOME"]
	if home == "" || home != opened.WorkingDir || !pathWithin(manager.pendingRoot, home) {
		t.Fatalf("pending login home = %q, workdir = %q", home, opened.WorkingDir)
	}
}

func TestCachedAgyAccountsProjectsOnlySafeActiveLoginMetadata(t *testing.T) {
	manager := newTestAgyAccountManager(t, nil, nil)
	manager.newID = func() string { return "b60a377d-da68-4a61-86f2-f31f04c571f2" }
	manager.executable = func() (string, error) { return "/Applications/AO.app/Contents/MacOS/ao-private", nil }
	createdAt := time.Date(2026, time.September, 2, 10, 30, 0, 0, time.UTC)
	terminal := &fakeAgyLoginTerminal{result: shellterm.ShellTerminal{
		HandleID: "shellterm-login-safe", WorkingDir: "/private/login-home", Title: "Add Agy account", CreatedAt: createdAt,
	}}
	manager.terminal = terminal
	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(manager.cached())
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, want := range []string{`"activeLogin"`, `"operationId":"` + started.Operation.OperationID + `"`, `"handleId":"shellterm-login-safe"`, `"title":"Add Agy account"`, `"createdAt":"2026-09-02T10:30:00Z"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("cached account login missing %s: %s", want, payload)
		}
	}
	for _, forbidden := range []string{"pending-accounts", "/private/login-home", "ao-private", "HOME", "workingDir", "argv", "env"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("cached account login leaked %q: %s", forbidden, payload)
		}
	}

	if _, err := manager.cancelLogin(context.Background(), started.Operation.OperationID); err != nil {
		t.Fatal(err)
	}
	payload, err = json.Marshal(manager.cached())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"activeLogin"`) {
		t.Fatalf("terminal login remained active: %s", payload)
	}
}

func TestAgyNativeLoginCreatesAndActivatesFirstAccountWithoutProviderCheck(t *testing.T) {
	email := "person@example.com"
	client := &fakeAgyAccountClient{read: ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email}}
	factory := &fakeAgyAccountFactory{capabilities: supportedAgyAccountCapabilities(), open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) { return client, nil }}
	manager := newTestAgyAccountManager(t, factory, nil)
	ids := []string{"b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestAccountID}
	index := 0
	manager.newID = func() string { id := ids[index]; index++; return id }
	manager.catalog.newID = func() string { return agyTestAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	terminal := &fakeAgyLoginTerminal{writeCredential: true, credential: agyTestOAuthCredential("provider-account", "opaque-login-credential"), result: shellterm.ShellTerminal{HandleID: "shellterm-login-1", Title: "Add Agy account"}}
	manager.terminal = terminal
	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.AgyAccountLoginCompleted || completed.Account == nil || !completed.Account.Active || completed.Account.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("completed login = %#v", completed)
	}
	if active := manager.activeAccountID(); active != agyTestAccountID {
		t.Fatalf("active account = %q", active)
	}
	if len(terminal.closed) != 1 || terminal.closed[0] != "shellterm-login-1" {
		t.Fatalf("closed terminals = %#v", terminal.closed)
	}
	credential := filepath.Join(manager.catalog.root, agyTestAccountID, agyCredentialHomeDirectory, agyCredentialFilename)
	data, err := os.ReadFile(credential)
	if err != nil || !bytes.Equal(data, agyTestOAuthCredential("provider-account", "opaque-login-credential")) {
		t.Fatalf("opaque credential = %q, err=%v", data, err)
	}
	if factory.opens != 0 {
		t.Fatalf("login transaction opened %d Agy clients", factory.opens)
	}
}

func TestAgyNativeLoginVerificationDoesNotReplaceExistingDeviceAccount(t *testing.T) {
	email := "saved@example.com"
	client := &fakeAgyAccountClient{read: ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email}}
	factory := &fakeAgyAccountFactory{capabilities: supportedAgyAccountCapabilities(), open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) { return client, nil }}
	manager := newTestAgyAccountManager(t, factory, nil)
	if err := ensurePrivateDirectory(manager.globalHome); err != nil {
		t.Fatal(err)
	}
	original := []byte("existing-device-credential")
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), original); err != nil {
		t.Fatal(err)
	}
	ids := []string{"b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestAccountID}
	index := 0
	manager.newID = func() string { id := ids[index]; index++; return id }
	manager.catalog.newID = func() string { return agyTestAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeAgyLoginTerminal{writeCredential: true, result: shellterm.ShellTerminal{HandleID: "shellterm-login-1", Title: "Add Agy account"}}

	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.AgyAccountLoginCompleted || completed.Account == nil || completed.Account.Active {
		t.Fatalf("completed login = %#v", completed)
	}
	current, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || !bytes.Equal(current, original) {
		t.Fatalf("existing device credential changed: %q, %v", current, err)
	}
	if manager.activeAccountID() != "" {
		t.Fatalf("saved account became active: %q", manager.activeAccountID())
	}
}

func TestAgyNativeLoginVerificationSavesAccountWhileDeviceReconciliationRetries(t *testing.T) {
	email := "person@example.com"
	client := &fakeAgyAccountClient{read: ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email,
	}}
	factory := &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open:         func(ports.AgyAccountContext) (ports.AgyAccountClient, error) { return client, nil },
	}
	manager := newTestAgyAccountManager(t, factory, nil)
	manager.mu.Lock()
	manager.reconciliation = domain.AgyDeviceReconciliation{
		Status:     domain.AgyDeviceReconciliationTemporarilyUnavailable,
		ReasonCode: "account_read_inconclusive", Retryable: true,
	}
	manager.mu.Unlock()
	ids := []string{"b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestAccountID}
	index := 0
	manager.newID = func() string { id := ids[index]; index++; return id }
	manager.catalog.newID = func() string { return agyTestAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeAgyLoginTerminal{
		writeCredential: true,
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-1", Title: "Add Agy account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.AgyAccountLoginCompleted || completed.Account == nil || !completed.Account.Active || completed.AccountID != agyTestAccountID {
		t.Fatalf("completed login = %#v", completed)
	}
	if active := manager.activeAccountID(); active != agyTestAccountID {
		t.Fatalf("first saved account was not activated: active=%q", active)
	}
	if _, err := readOpaqueCredential(filepath.Join(manager.catalog.root, agyTestAccountID, agyCredentialHomeDirectory, agyCredentialFilename)); err != nil {
		t.Fatalf("saved account credential: %v", err)
	}
	if credential, err := readOpaqueCredential(manager.globalCredentialPath()); err != nil || !bytes.Equal(credential, agyTestOAuthCredential("", "opaque-login-credential")) {
		t.Fatalf("first saved account was not installed on the device: %q, %v", credential, err)
	}
}

func TestAgyNativeReauthenticationReplacesTheExistingAccountSlot(t *testing.T) {
	email := "person@example.com"
	observation := ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email}
	client := &fakeAgyAccountClient{
		read: observation,
		capacity: ports.AgyCapacityObservation{Overall: &domain.AgyCapacityBucket{
			LimitID: "agy",
			Primary: &domain.AgyCapacityWindow{UsedPercent: 58},
		}},
	}
	factory := &fakeAgyAccountFactory{capabilities: supportedAgyAccountCapabilities(), open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) { return client, nil }}
	manager := newTestAgyAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestOAuthCredential("provider-account", "old-access"), observation)
	manager.requireReauthentication(record.Snapshot.ID)
	if before := manager.cached().Accounts[0].Capacity; before.ReasonCode != domain.AgyCapacityReasonSkippedSignedOut {
		t.Fatalf("capacity before reauthentication = %#v", before)
	}
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeAgyLoginTerminal{
		writeCredential: true,
		credential:      agyTestOAuthCredential("provider-account", "new-access"),
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Agy account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Operation.AccountID != record.Snapshot.ID {
		t.Fatalf("reauthentication target = %q", started.Operation.AccountID)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.AgyAccountLoginCompleted || completed.Account == nil || completed.Account.ID != record.Snapshot.ID {
		t.Fatalf("completed reauthentication = %#v", completed)
	}
	if completed.Account.Authentication.State != domain.AgentAuthenticationAuthorized || completed.Account.Capacity.State != domain.AgyCapacityUnknown {
		t.Fatalf("completed reauthentication state = %#v", completed.Account)
	}
	if completed.Account.Capacity.ReasonCode == domain.AgyCapacityReasonSkippedSignedOut {
		t.Fatalf("completed reauthentication retained signed-out capacity: %#v", completed.Account.Capacity)
	}
	cached := manager.cached().Accounts[0]
	if cached.Authentication.State != domain.AgentAuthenticationAuthorized || cached.Capacity.State != domain.AgyCapacityUnknown {
		t.Fatalf("cached reauthentication state = %#v", cached)
	}
	if snapshots := manager.catalog.snapshots(); len(snapshots) != 1 || snapshots[0].ID != record.Snapshot.ID {
		t.Fatalf("reauthentication changed account identity: %#v", snapshots)
	}
	credential, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	if err != nil || !bytes.Equal(credential, agyTestOAuthCredential("provider-account", "new-access")) {
		t.Fatalf("replacement credential = %q, err=%v", credential, err)
	}
}

func TestAgyActiveReauthenticationRevalidatesAndReplacesTheDeviceCredential(t *testing.T) {
	email := "person@example.com"
	observation := ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email,
	}
	factory := &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			return &fakeAgyAccountClient{read: observation}, nil
		},
	}
	state := &testAgyDeviceSeed{
		active: testAgyDeviceAccount{AccountID: agyTestAccountID, Revision: 1}, found: true,
	}
	manager := newTestAgyAccountManager(t, factory, state)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestOAuthCredential("provider-account", "old-access"), observation)
	oldCredential, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), oldCredential); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	agySetTestDeviceAccount(manager, state.active)
	manager.markDeviceReconciledLocked(true, time.Now().UTC())
	manager.mu.Unlock()
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeAgyLoginTerminal{
		writeCredential: true,
		credential:      agyTestOAuthCredential("provider-account", "new-access"),
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Agy account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.AgyAccountLoginCompleted || completed.Account == nil || !completed.Account.Active {
		t.Fatalf("completed reauthentication = %#v", completed)
	}
	globalCredential, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || !bytes.Equal(globalCredential, agyTestOAuthCredential("provider-account", "new-access")) {
		t.Fatalf("device credential = %q, err=%v", globalCredential, err)
	}
	if manager.activeAccountID() != agyTestAccountID {
		t.Fatalf("active account = %q", manager.activeAccountID())
	}
}

func TestAgyOpenReauthenticationReconcilesBeforeCapturingActiveOwnership(t *testing.T) {
	observation := ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle}
	factory := &fakeAgyAccountFactory{capabilities: supportedAgyAccountCapabilities()}
	manager := newTestAgyAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	credential := agyTestOAuthCredential("provider-account", "old-access")
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, observation)
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.deviceAccountID = ""
	manager.reconciliation = domain.AgyDeviceReconciliation{Status: domain.AgyDeviceReconciliationNotChecked, ActiveAccountVerified: false}
	manager.mu.Unlock()
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeAgyLoginTerminal{result: shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Agy account"}}
	installed := &readinessTestAgent{resolve: func(context.Context) (string, error) { return "/ao", nil }}
	service := &Service{agyAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{readinessHarness("agy", "Agy", installed)},
	})}

	started, err := service.OpenAgyAccountReauthenticationTerminal(context.Background(), record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	targetWasActive := manager.login != nil && manager.login.targetWasActive
	manager.mu.Unlock()
	if !targetWasActive {
		t.Fatalf("operation did not capture reconciled device ownership: %#v", started.Operation)
	}
}

func TestAgyActiveReauthenticationPreservesCredentialsWhenDeviceChangesAfterTerminalOpens(t *testing.T) {
	observation := ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
	}
	manager := newTestAgyAccountManager(t, nil, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	oldCredential := agyTestOAuthCredential("provider-account", "old-access")
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", oldCredential, observation)
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), oldCredential); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	agySetTestDeviceAccount(manager, testAgyDeviceAccount{AccountID: record.Snapshot.ID, Revision: 1})
	manager.markDeviceReconciledLocked(true, time.Now().UTC())
	manager.mu.Unlock()
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	newCredential := agyTestOAuthCredential("provider-account", "new-access")
	manager.terminal = &fakeAgyLoginTerminal{
		writeCredential: true,
		credential:      newCredential,
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Agy account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	externalCredential := agyTestOAuthCredential("external-account", "external-access")
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), externalCredential); err != nil {
		t.Fatal(err)
	}

	result, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != domain.AgyAccountLoginRetryable {
		t.Fatalf("reauthentication result = %#v", result)
	}
	globalCredential, err := readOpaqueCredential(manager.globalCredentialPath())
	if err != nil || !bytes.Equal(globalCredential, externalCredential) {
		t.Fatalf("external device credential changed: credential=%q err=%v", globalCredential, err)
	}
	savedCredential, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	if err != nil || !bytes.Equal(savedCredential, oldCredential) {
		t.Fatalf("saved credential changed: credential=%q err=%v", savedCredential, err)
	}
	pendingCredential, err := readOpaqueCredential(filepath.Join(manager.login.home, agyCredentialFilename))
	if err != nil || !bytes.Equal(pendingCredential, newCredential) {
		t.Fatalf("pending credential was not preserved: credential=%q err=%v", pendingCredential, err)
	}
}

func TestAgyActiveReauthenticationCommitsWhenProviderIsUnavailable(t *testing.T) {
	email := "person@example.com"
	observation := ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email,
	}
	factory := &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(account ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			if !account.Managed {
				return nil, errors.New("temporary device verification failure")
			}
			return &fakeAgyAccountClient{read: observation}, nil
		},
	}
	state := &testAgyDeviceSeed{
		active: testAgyDeviceAccount{AccountID: agyTestAccountID, Revision: 1}, found: true,
	}
	manager := newTestAgyAccountManager(t, factory, state)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestOAuthCredential("provider-account", "old-access"), observation)
	oldCredential, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), oldCredential); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	agySetTestDeviceAccount(manager, state.active)
	manager.markDeviceReconciledLocked(true, time.Now().UTC())
	manager.mu.Unlock()
	manager.newID = func() string { return "1c5de3ab-82d0-4a68-a06b-8495cdeab909" }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeAgyLoginTerminal{
		writeCredential: true,
		credential:      agyTestOAuthCredential("provider-account", "new-access"),
		result:          shellterm.ShellTerminal{HandleID: "shellterm-login-reauth", Title: "Sign in to Agy account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), record.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.AgyAccountLoginCompleted || completed.Account == nil || !completed.Account.Active {
		t.Fatalf("completed reauthentication = %#v", completed)
	}
	if factory.opens != 0 {
		t.Fatalf("reauthentication opened %d Agy clients", factory.opens)
	}
}

func TestAgyRequiredReauthenticationStaysSignedOutUntilLogin(t *testing.T) {
	factory := &fakeAgyAccountFactory{open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
		t.Fatal("account requiring sign-in was read again")
		return nil, nil
	}}
	manager := newTestAgyAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle})
	manager.requireReauthentication(record.Snapshot.ID)

	authentication, err := manager.ensureAuthentication(context.Background(), record, domain.AgentReadinessPurposeDisplay)
	if err != nil {
		t.Fatal(err)
	}
	if authentication.State != domain.AgentAuthenticationUnauthorized || authentication.Freshness != domain.AgentReadinessFresh {
		t.Fatalf("authentication = %#v", authentication)
	}
	view := manager.cached()
	if len(view.Accounts) != 1 || view.Accounts[0].Authentication.State != domain.AgentAuthenticationUnauthorized {
		t.Fatalf("account awaiting sign-in = %#v", view.Accounts)
	}
}

func TestAgyLoginCloseFailureRetainsPendingOperation(t *testing.T) {
	manager := newTestAgyAccountManager(t, nil, nil)
	manager.newID = func() string { return "b60a377d-da68-4a61-86f2-f31f04c571f2" }
	manager.executable = func() (string, error) { return "/ao", nil }
	terminal := &fakeAgyLoginTerminal{result: shellterm.ShellTerminal{HandleID: "shellterm-login-1"}, closeErr: errors.New("pty busy")}
	manager.terminal = terminal
	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.cancelLogin(context.Background(), started.Operation.OperationID); err == nil {
		t.Fatal("cancel unexpectedly succeeded")
	}
	manager.mu.Lock()
	operation := manager.login.snapshot
	manager.mu.Unlock()
	if operation.Status != domain.AgyAccountLoginRetryable || operation.ReasonCode != domain.AgyAccountLoginReasonFailed {
		t.Fatalf("operation after close failure = %#v", operation)
	}
}

func TestAgyBootstrapImportsUnknownDeviceCredentialOnlyOnce(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "device")
	if err := ensurePrivateDirectory(device); err != nil {
		t.Fatal(err)
	}
	deviceCredential := filepath.Join(device, agyCredentialFilename)
	original := agyTestOAuthCredential("device-account", "access-one")
	if err := writePrivateFileAtomic(deviceCredential, original); err != nil {
		t.Fatal(err)
	}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), device, nil, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	if err := manager.waitAccountStore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != agyTestAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("imported account = %#v", view)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snapshots := manager.catalog.snapshots(); len(snapshots) != 1 || snapshots[0].ID != agyTestAccountID {
		t.Fatalf("repeated reconciliation duplicated import = %#v", snapshots)
	}
	after, err := os.ReadFile(deviceCredential)
	if err != nil || !slices.Equal(after, original) {
		t.Fatalf("device credential changed: %q err=%v", after, err)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap created an obsolete private runtime: %v", err)
	}
}

func TestAgyGlobalReconciliationKeepsMatchingDeviceAccountActiveWithoutProactiveRefresh(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalCredential := filepath.Join(globalHome, agyCredentialFilename)
	credential := []byte("opaque-agy-credential\x00\xff")
	if err := writePrivateFileAtomic(globalCredential, credential); err != nil {
		t.Fatal(err)
	}
	email := "device@example.com"
	observation := ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &email,
	}
	manager := newAgyAccountManager(
		context.Background(),
		filepath.Join(root, "accounts"),
		filepath.Join(root, "pending"),
		filepath.Join(root, "staging"),
		globalHome,
		nil,
		nil,
	)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", observation)
	if err := writePrivateFileAtomic(filepath.Join(record.Home, agyCredentialFilename), credential); err != nil {
		t.Fatal(err)
	}
	var refreshRequests []bool
	manager.factory = &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			return &fakeAgyAccountClient{readFn: func(_ context.Context) (ports.AgyAccountObservation, error) {
				// The Antigravity client has no proactive refresh; every read is the protected quota call.
				refresh := false
				refreshRequests = append(refreshRequests, refresh)
				if refresh {
					return ports.AgyAccountObservation{}, errors.New("proactive refresh rejected for copied credential")
				}
				return observation, nil
			}}, nil
		},
	}

	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != agyTestAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("reconciled device account = %#v, refresh requests = %#v", view, refreshRequests)
	}
	if slices.Contains(refreshRequests, true) {
		t.Fatalf("reconciliation requested proactive refresh: %#v", refreshRequests)
	}
}

func TestAgyGlobalReconciliationMatchesRotatedOAuthByAccountID(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalCredential := agyTestOAuthCredential("provider-account-a", "rotated-access")
	if err := writePrivateFileAtomic(filepath.Join(globalHome, agyCredentialFilename), globalCredential); err != nil {
		t.Fatal(err)
	}
	email := "known@example.com"
	state := &testAgyDeviceSeed{
		active: testAgyDeviceAccount{AccountID: agyTestAccountID, Revision: 7},
	}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestOAuthCredential("provider-account-a", "old-access"), ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &email,
	})
	agySetTestDeviceAccount(manager, state.active)
	manager.catalog.updateSnapshot(record.Snapshot.ID, func(snapshot *domain.AgyAccountSnapshot) {
		snapshot.Authentication = accountAuthenticationObservation(time.Now().UTC(), domain.AgentAuthenticationAuthorized)
	})
	manager.requireReauthentication(record.Snapshot.ID)
	manager.factory = &fakeAgyAccountFactory{open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
		t.Fatal("local reconciliation opened Agy")
		return nil, nil
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != agyTestAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("rotated OAuth account was not matched = %#v", view)
	}
	if view.Accounts[0].Authentication.State != domain.AgentAuthenticationUnknown || view.Accounts[0].Authentication.ReasonCode != domain.AgentReadinessReasonNotChecked {
		t.Fatalf("old expired-token result survived credential rotation = %#v", view.Accounts[0].Authentication)
	}
	if _, reauthenticationRequired := manager.authenticationVerification(record.Snapshot.ID); reauthenticationRequired {
		t.Fatal("rotated credential remained blocked by the previous login-expired result")
	}
	saved, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	if err != nil || !slices.Equal(saved, globalCredential) {
		t.Fatalf("rotated credential was not checkpointed: %v", err)
	}
}

func TestAgyGlobalReconciliationExactCredentialMatchRemainsActiveOffline(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	state := &testAgyDeviceSeed{active: testAgyDeviceAccount{AccountID: agyTestAccountID, Revision: 7}}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	email := "known@example.com"
	record := agyCommitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email,
	})
	saved, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), saved); err != nil {
		t.Fatal(err)
	}
	agySetTestDeviceAccount(manager, state.active)
	manager.factory = &fakeAgyAccountFactory{open: func(account ports.AgyAccountContext) (ports.AgyAccountClient, error) {
		if account.Managed || account.Home != globalHome {
			t.Fatalf("matched device verification context = %#v", account)
		}
		return nil, errors.New("offline")
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != agyTestAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("byte-matched account lost device ownership while offline: %#v", view)
	}
	if view.Accounts[0].AccountEmail == nil || *view.Accounts[0].AccountEmail != email {
		t.Fatalf("cached identity changed during failed verification: %#v", view.Accounts[0])
	}
}

func TestAgyExternalDeviceSwitchImportsBWithoutWritingIntoA(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credentialB := agyTestOAuthCredential("provider-b", "access-b")
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, agyCredentialFilename), credentialB); err != nil {
		t.Fatal(err)
	}
	state := &testAgyDeviceSeed{active: testAgyDeviceAccount{AccountID: agyTestAccountID, Revision: 3}}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	accountBID := "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"
	ids := []string{agyTestAccountID, accountBID}
	manager.catalog.newID = func() string { id := ids[0]; ids = ids[1:]; return id }
	aEmail := "account-a@example.com"
	credentialA := agyTestOAuthCredential("provider-a", "access-a")
	recordA := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credentialA, ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &aEmail,
	})
	agySetTestDeviceAccount(manager, state.active)
	manager.factory = &fakeAgyAccountFactory{open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
		t.Fatal("local reconciliation opened Agy")
		return nil, nil
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != accountBID || len(view.Accounts) != 2 {
		t.Fatalf("external B contaminated saved A: %#v", view)
	}
	savedA, err := readOpaqueCredential(filepath.Join(recordA.Home, agyCredentialFilename))
	if err != nil || !slices.Equal(savedA, credentialA) {
		t.Fatalf("external B overwrote A: %v", err)
	}
	// The imported account's label comes from its own credential's ID token,
	// not from a provider call, so it is known before any authentication warming.
	activeB, ok := manager.catalog.record(accountBID)
	if !ok || activeB.Snapshot.AccountEmail == nil || *activeB.Snapshot.AccountEmail != "provider-b@example.com" || activeB.Snapshot.Authentication.State == domain.AgentAuthenticationAuthorized {
		t.Fatalf("local import should carry the ID token email and await authentication warming: %#v", activeB)
	}
}

func TestAgyGlobalReconciliationMissingGlobalClearsActiveProjectionWithoutRestoringSavedAccount(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	state := &testAgyDeviceSeed{
		active: testAgyDeviceAccount{AccountID: agyTestAccountID, Revision: 4},
	}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
	})
	savedCredential, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	agySetTestDeviceAccount(manager, state.active)
	manager.factory = &fakeAgyAccountFactory{open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
		t.Fatal("missing-global reconciliation opened Agy")
		return nil, nil
	}}

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if manager.activeAccountID() != "" || view.ActiveAccountID != "" || len(view.Accounts) != 1 || view.Accounts[0].Active {
		t.Fatalf("saved account remained active without a device credential: %#v", view)
	}
	storedCredential, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	if err != nil || !slices.Equal(storedCredential, savedCredential) {
		t.Fatalf("saved account credential changed: %v", err)
	}
	if _, err := os.Stat(manager.globalCredentialPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("saved account was silently restored globally: %v", err)
	}
}

func TestEnsureAgyAccountsReconcilesRecentlyRemovedGlobalCredentialBeforeAccountChecks(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := agyTestOAuthCredential("provider-account", "access-token")
	state := &testAgyDeviceSeed{
		active: testAgyDeviceAccount{AccountID: agyTestAccountID, Revision: 1},
	}
	var opened []ports.AgyAccountContext
	email := "saved@example.com"
	factory := &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(account ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			opened = append(opened, account)
			return &fakeAgyAccountClient{
				read: ports.AgyAccountObservation{
					Authentication: domain.AgentAuthenticationAuthorized,
					Method:         domain.AgyAuthMethodGoogle,
					Email:          &email,
				},
				capacity: ports.AgyCapacityObservation{},
			}, nil
		},
	}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &email,
	})
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}
	agySetTestDeviceAccount(manager, state.active)
	manager.accountStoreReady = true
	now := time.Now().UTC()
	manager.reconciliation = domain.AgyDeviceReconciliation{
		Status:                domain.AgyDeviceReconciliationVerified,
		ActiveAccountVerified: true,
		ReasonCode:            "verified",
		AttemptedAt:           &now,
		VerifiedAt:            &now,
	}
	manager.deviceAccountID = record.Snapshot.ID
	manager.deviceCredentialPresent = true

	// Reproduce an external `agy logout` while AO still holds a fresh device
	// association. The old five-minute cache skipped reconciliation here.
	if err := os.Remove(manager.globalCredentialPath()); err != nil {
		t.Fatal(err)
	}
	readiness := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{harnessAgent(string(domain.HarnessAgy), "Agy", nil)},
	})
	service := &Service{agyAccounts: manager, readiness: readiness}

	view, err := service.EnsureAgyAccounts(context.Background(), nil, AgyAccountEnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if view.ActiveAccountID != "" || len(view.Accounts) != 1 || view.Accounts[0].Active {
		t.Fatalf("removed global credential remained active: %#v", view)
	}
	if view.Accounts[0].Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("saved credential was not checked independently: %#v", view.Accounts[0].Authentication)
	}
	if manager.activeAccountID() != "" {
		t.Fatalf("active account was not cleared: %q", manager.activeAccountID())
	}
	if len(opened) == 0 {
		t.Fatal("saved account was not checked")
	}
	for _, account := range opened {
		if !account.Managed || canonicalPath(account.Home) != canonicalPath(record.Home) {
			t.Fatalf("account check used stale global home: %#v", account)
		}
	}
}

func TestEnsureAgyAccountsTargetedRefreshDoesNotReconcileDeviceCredential(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := agyTestOAuthCredential("provider-account", "access-token")
	var opened []ports.AgyAccountContext
	factory := &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(account ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			opened = append(opened, account)
			return &fakeAgyAccountClient{
				read:     ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle},
				capacity: ports.AgyCapacityObservation{},
			}, nil
		},
	}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
	})
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}
	manager.accountStoreReady = true
	attemptedAt := time.Now().UTC().Add(-time.Minute)
	manager.reconciliation = domain.AgyDeviceReconciliation{
		Status:                domain.AgyDeviceReconciliationVerified,
		ActiveAccountVerified: true,
		ReasonCode:            "verified",
		AttemptedAt:           &attemptedAt,
		VerifiedAt:            &attemptedAt,
	}
	manager.deviceAccountID = record.Snapshot.ID
	manager.deviceCredentialPresent = true
	readiness := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{harnessAgent(string(domain.HarnessAgy), "Agy", nil)},
	})
	service := &Service{agyAccounts: manager, readiness: readiness}

	view, err := service.EnsureAgyAccounts(context.Background(), []string{record.Snapshot.ID}, AgyAccountEnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if view.DeviceReconciliation.AttemptedAt == nil || !view.DeviceReconciliation.AttemptedAt.Equal(attemptedAt) {
		t.Fatalf("targeted metadata refresh reconciled device credential: before=%v after=%v", attemptedAt, view.DeviceReconciliation.AttemptedAt)
	}
	if view.ActiveAccountID != record.Snapshot.ID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("targeted metadata refresh changed active presentation: %#v", view)
	}
	if len(opened) == 0 {
		t.Fatal("targeted metadata refresh did not check the saved account")
	}
	for _, account := range opened {
		if !account.Managed || canonicalPath(account.Home) != canonicalPath(record.Home) {
			t.Fatalf("targeted metadata refresh used device-global home: %#v", account)
		}
	}
}

func TestEnsureAgyAccountsRetriesSavedAccountWhenGlobalCredentialDisappearsDuringCheck(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := agyTestOAuthCredential("provider-account", "access-token")
	state := &testAgyDeviceSeed{
		active: testAgyDeviceAccount{AccountID: agyTestAccountID, Revision: 1},
	}
	email := "saved@example.com"
	removedGlobal := false
	var opened []ports.AgyAccountContext
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", credential, ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &email,
	})
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), credential); err != nil {
		t.Fatal(err)
	}
	manager.factory = &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(account ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			opened = append(opened, account)
			if !account.Managed && !removedGlobal {
				removedGlobal = true
				if err := os.Remove(manager.globalCredentialPath()); err != nil {
					t.Fatal(err)
				}
				return nil, errors.New("device credential disappeared")
			}
			return &fakeAgyAccountClient{
				read: ports.AgyAccountObservation{
					Authentication: domain.AgentAuthenticationAuthorized,
					Method:         domain.AgyAuthMethodGoogle,
					Email:          &email,
				},
				capacity: ports.AgyCapacityObservation{},
			}, nil
		},
	}
	agySetTestDeviceAccount(manager, state.active)
	manager.accountStoreReady = true
	readiness := newReadinessCoordinator(readinessCoordinatorConfig{
		Agents: []agentregistry.HarnessAgent{harnessAgent(string(domain.HarnessAgy), "Agy", nil)},
	})
	service := &Service{agyAccounts: manager, readiness: readiness}

	view, err := service.EnsureAgyAccounts(context.Background(), nil, AgyAccountEnsureOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !removedGlobal {
		t.Fatal("test did not remove the global credential during the account check")
	}
	if view.ActiveAccountID != "" || view.Accounts[0].Active {
		t.Fatalf("removed global credential remained active: %#v", view)
	}
	if view.Accounts[0].Authentication.State != domain.AgentAuthenticationAuthorized || view.Accounts[0].Authentication.Freshness != domain.AgentReadinessFresh {
		t.Fatalf("global disappearance became a false authentication failure: %#v", view.Accounts[0].Authentication)
	}
	if len(opened) < 2 || opened[0].Managed || !opened[1].Managed || canonicalPath(opened[1].Home) != canonicalPath(record.Home) {
		t.Fatalf("account checks did not move from global to saved home: %#v", opened)
	}
}

func TestAgyGlobalAccountMatchingUsesUniqueOpaqueCredentialIdentity(t *testing.T) {
	manager := newTestAgyAccountManager(t, nil, nil)
	accountIDs := []string{agyTestAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.catalog.newID = func() string {
		id := accountIDs[0]
		accountIDs = accountIDs[1:]
		return id
	}
	first := agyCommitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle})
	second := agyCommitTestAccount(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle})
	if err := writePrivateFileAtomic(filepath.Join(first.Home, agyCredentialFilename), []byte("credential-a")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFileAtomic(filepath.Join(second.Home, agyCredentialFilename), []byte("credential-b")); err != nil {
		t.Fatal(err)
	}

	matched, match := manager.matchGlobalCredentialForReconciliation([]byte("credential-b"), agyCredentialIdentity{}, errors.New("opaque credential"))
	if match != agyCredentialMatchManaged || matched.Snapshot.ID != second.Snapshot.ID {
		t.Fatalf("unique opaque credential match = (%q, %v), want second account", matched.Snapshot.ID, match)
	}
	if err := writePrivateFileAtomic(filepath.Join(first.Home, agyCredentialFilename), []byte("credential-b")); err != nil {
		t.Fatal(err)
	}
	if matched, match := manager.matchGlobalCredentialForReconciliation([]byte("credential-b"), agyCredentialIdentity{}, errors.New("opaque credential")); match != agyCredentialMatchAmbiguous {
		t.Fatalf("ambiguous opaque credential matched account %q", matched.Snapshot.ID)
	}
}

func TestAgyGlobalReconciliationRefusesAmbiguousProviderAccountID(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalCredential := agyTestOAuthCredential("shared-provider-account", "global-access")
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, agyCredentialFilename), globalCredential); err != nil {
		t.Fatal(err)
	}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil, nil)
	ids := []string{agyTestAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.catalog.newID = func() string { id := ids[0]; ids = ids[1:]; return id }
	first := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestOAuthCredential("shared-provider-account", "first-access"), ports.AgyAccountObservation{Method: domain.AgyAuthMethodGoogle})
	second := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", agyTestOAuthCredential("shared-provider-account", "second-access"), ports.AgyAccountObservation{Method: domain.AgyAuthMethodGoogle})

	err := manager.reconcileGlobal(context.Background())
	var failure *agyAccountLocalFailure
	if !errors.As(err, &failure) || failure.reason != "global_account_ambiguous" || failure.retryable {
		t.Fatalf("ambiguous reconciliation = %#v", err)
	}
	for _, record := range []agyAccountRecord{first, second} {
		saved, readErr := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
		if readErr != nil || slices.Equal(saved, globalCredential) {
			t.Fatalf("ambiguous credential overwrote %s: %v", record.Snapshot.ID, readErr)
		}
	}
}

func TestAgyGlobalReconciliationReusesImportedAccountAcrossExternalTokenRotation(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	globalCredential := filepath.Join(globalHome, agyCredentialFilename)
	credentialA := agyTestOAuthCredential("provider-account", "access-a")
	if err := writePrivateFileAtomic(globalCredential, credentialA); err != nil {
		t.Fatal(err)
	}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }

	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := manager.cached()
	if first.ActiveAccountID != agyTestAccountID || len(first.Accounts) != 1 || !first.Accounts[0].Active {
		t.Fatalf("first imported account = %#v", first)
	}
	credentialB := agyTestOAuthCredential("provider-account", "access-b")
	if err := writeGlobalCredentialAtomic(globalCredential, credentialB); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := manager.cached()
	if second.ActiveAccountID != agyTestAccountID || len(second.Accounts) != 1 || !second.Accounts[0].Active {
		t.Fatalf("rotated account was not reused: %#v", second)
	}
	record, ok := manager.catalog.record(agyTestAccountID)
	if !ok {
		t.Fatal("imported account disappeared")
	}
	saved, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	if err != nil || !slices.Equal(saved, credentialB) {
		t.Fatalf("rotated credential was not saved: %v", err)
	}
}

func TestAgyGlobalReconciliationReactivatesSignedOutIdentityFromDeviceCredential(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	credential := agyTestOAuthCredential("returning-account", "restored-access")
	if err := writePrivateFileAtomic(filepath.Join(globalHome, agyCredentialFilename), credential); err != nil {
		t.Fatal(err)
	}
	email := "returning@example.com"
	observation := ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email}
	factory := &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			return &fakeAgyAccountClient{read: observation}, nil
		},
	}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestOAuthCredential("returning-account", "old-access"), observation)
	if _, err := manager.catalog.markSignedOut(record.Snapshot.ID); err != nil {
		t.Fatal(err)
	}

	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != agyTestAccountID || len(view.Accounts) != 1 || view.Accounts[0].Status != domain.AgyAccountStatusValid || !view.Accounts[0].Active {
		t.Fatalf("device credential did not reactivate its saved account = %#v", view)
	}
	saved, err := readOpaqueCredential(filepath.Join(record.Home, agyCredentialFilename))
	if err != nil || !slices.Equal(saved, credential) {
		t.Fatalf("restored credential was not copied to its account: %v", err)
	}
}

func TestAgyImportedGlobalCredentialDoesNotBlockNormalAuthentication(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, agyCredentialFilename), agyTestOAuthCredential("imported-provider-account", "access")); err != nil {
		t.Fatal(err)
	}
	email := "keyring@example.com"
	factory := &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			return &fakeAgyAccountClient{read: ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &email}}, nil
		},
	}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, factory, nil, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	if err := manager.initializeAccountStore(); err != nil {
		t.Fatal(err)
	}
	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if view.ActiveAccountID != agyTestAccountID || len(view.Accounts) != 1 || !view.Accounts[0].Active {
		t.Fatalf("imported global account = %#v", view)
	}
	if got := manager.detectCapabilities(context.Background()).GlobalSwitch.State; got != domain.AgyCapabilitySupported {
		t.Fatalf("global switch capability = %q, want supported", got)
	}
	manager.mu.Lock()
	manager.accountStoreReady = true
	manager.mu.Unlock()
	service := &Service{agyAccounts: manager}
	auth, handled := service.structuredAgyAuthentication(context.Background(), string(domain.HarnessAgy), domain.AgentReadinessPurposeDisplay)
	if !handled || auth.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("normal Agy authentication = (%#v, %v), want authorized", auth, handled)
	}
	if err := manager.catalog.refresh(); err != nil {
		t.Fatal(err)
	}
	persisted, ok := manager.catalog.record(agyTestAccountID)
	if !ok || persisted.Snapshot.AccountEmail == nil || *persisted.Snapshot.AccountEmail != email || persisted.Snapshot.Label != email {
		t.Fatalf("imported account metadata was not persisted: %#v", persisted.Snapshot)
	}
}

func TestAgyGlobalSwitchCapabilityAllowsMissingCredentialButRejectsUnsafeCredential(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	factory := &fakeAgyAccountFactory{capabilities: supportedAgyAccountCapabilities()}
	manager := newAgyAccountManager(
		context.Background(),
		filepath.Join(root, "accounts"),
		filepath.Join(root, "pending"),
		filepath.Join(root, "staging"),
		globalHome,
		factory,
		nil,
		nil,
	)

	if got := manager.detectCapabilities(context.Background()).GlobalSwitch.State; got != domain.AgyCapabilitySupported {
		t.Fatalf("missing credential global switch capability = %q, want supported", got)
	}
	// A directory where the token file should be is an unsafe credential.
	if err := os.MkdirAll(manager.globalCredentialPath(), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := manager.detectCapabilities(context.Background()).GlobalSwitch.State; got != domain.AgyCapabilityUnsupported {
		t.Fatalf("unsafe credential global switch capability = %q, want unsupported", got)
	}
}

func TestAgyLocalReconciliationPreservesActiveSlotProjection(t *testing.T) {
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	if err := writeGlobalCredentialAtomic(filepath.Join(globalHome, agyCredentialFilename), []byte("opaque-agy-credential\x00\xff")); err != nil {
		t.Fatal(err)
	}
	slotEmail := "saved@example.com"
	state := &testAgyDeviceSeed{active: testAgyDeviceAccount{AccountID: agyTestAccountID, Revision: 3}}
	var opened []ports.AgyAccountContext
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &slotEmail,
	})
	agySetTestDeviceAccount(manager, state.active)
	manager.factory = &fakeAgyAccountFactory{capabilities: supportedAgyAccountCapabilities(), open: func(account ports.AgyAccountContext) (ports.AgyAccountClient, error) {
		opened = append(opened, account)
		if account.Managed {
			return &fakeAgyAccountClient{read: ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &slotEmail}}, nil
		}
		return &fakeAgyAccountClient{read: ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle, Email: &slotEmail}}, nil
	}}
	checked := time.Now().UTC()
	manager.catalog.updateSnapshot(record.Snapshot.ID, func(snapshot *domain.AgyAccountSnapshot) {
		snapshot.Authentication = accountAuthenticationObservation(checked, domain.AgentAuthenticationAuthorized)
	})
	manager.auth[record.Snapshot.ID] = &agyAccountAuthState{invalidated: true}
	manager.capacity.replace(record.Snapshot.ID, domain.AgyCapacitySnapshot{State: domain.AgyCapacityAvailable, Freshness: domain.AgentReadinessFresh, ReasonCode: domain.AgyCapacityReasonAvailable, Reason: "available", CheckedAt: &checked, AdditionalBuckets: []domain.AgyCapacityBucket{}}, "test")

	if err := manager.reconcileGlobal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ensureAuthentication(context.Background(), record, domain.AgentReadinessPurposeDisplay); err != nil {
		t.Fatal(err)
	}
	view := manager.cached()
	if len(opened) < 1 || opened[len(opened)-1].Managed || opened[len(opened)-1].Home != globalHome {
		t.Fatalf("active slot authentication contexts = %#v", opened)
	}
	if len(view.Accounts) != 1 || view.Accounts[0].AccountEmail == nil || *view.Accounts[0].AccountEmail != slotEmail || view.Accounts[0].Capacity.State != domain.AgyCapacityAvailable {
		t.Fatalf("active slot projection changed under unmanaged global state = %#v", view)
	}
}

func TestAgyAuthenticationRequestCancellationDoesNotCancelSharedRead(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	client := &fakeAgyAccountClient{read: ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized}, readStarted: started, readRelease: release}
	factory := &fakeAgyAccountFactory{open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) { return client, nil }}
	manager := newTestAgyAccountManager(t, factory, nil)
	manager.catalog.newID = func() string { return agyTestAccountID }
	record := agyCommitTestAccount(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle})
	waitCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := manager.ensureAuthentication(waitCtx, record, domain.AgentReadinessPurposeDisplay)
		done <- err
	}()
	<-started
	manager.mu.Lock()
	shared := manager.auth[record.Snapshot.ID].call
	manager.mu.Unlock()
	if shared == nil {
		t.Fatal("shared authentication read was not in flight")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v", err)
	}
	close(release)
	// Wait on the shared call itself, not on the snapshot. The snapshot flips to
	// authorized before the verified descriptor is persisted under the account
	// home, so polling the snapshot lets TempDir cleanup race that write.
	select {
	case <-shared.done:
	case <-time.After(5 * time.Second):
		t.Fatal("shared authentication read did not finish")
	}
	latest, _ := manager.catalog.record(record.Snapshot.ID)
	if latest.Snapshot.Authentication.State != domain.AgentAuthenticationAuthorized {
		t.Fatalf("shared authentication state = %v", latest.Snapshot.Authentication.State)
	}
}

func TestAgyLoginDeduplicatesExistingAccountByProviderID(t *testing.T) {
	email := "duplicate@example.com"
	observation := ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &email,
	}
	client := &fakeAgyAccountClient{read: observation}
	factory := &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			return client, nil
		},
	}
	state := &testAgyDeviceSeed{}
	manager := newTestAgyAccountManager(t, factory, state)

	existingID := "a1111111-1111-4111-8111-111111111111"
	manager.catalog.newID = func() string { return existingID }
	existing := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "c1111111-1111-4111-8111-111111111111", agyTestOAuthCredential("provider-account", "old-access"), observation)
	if existing.Snapshot.ID != existingID {
		t.Fatalf("existing account id = %q", existing.Snapshot.ID)
	}

	loginID := "d2222222-2222-4222-8222-222222222222"
	newAccountID := "b2222222-2222-4222-8222-222222222222"
	manager.newID = func() string { return loginID }
	manager.catalog.newID = func() string { return newAccountID }
	manager.executable = func() (string, error) { return "/ao", nil }
	manager.terminal = &fakeAgyLoginTerminal{
		writeCredential: true,
		credential:      agyTestOAuthCredential("provider-account", "new-access"),
		result:          shellterm.ShellTerminal{HandleID: "shellterm-dedup", Title: "Add Agy account"},
	}

	started, err := manager.openLoginTerminal(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := manager.verifyLogin(context.Background(), started.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != domain.AgyAccountLoginCompleted {
		t.Fatalf("login status = %q", completed.Status)
	}
	snapshots := manager.catalog.snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 account after dedup login, got %d", len(snapshots))
	}
	if snapshots[0].ID != existingID {
		t.Fatalf("expected existing account %q to be reused, got %q", existingID, snapshots[0].ID)
	}
	credential, err := readOpaqueCredential(filepath.Join(existing.Home, agyCredentialFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(credential, agyTestOAuthCredential("provider-account", "new-access")) {
		t.Fatalf("credential was not replaced: %q", credential)
	}
}

func agyNewSwitchAdmissionFixture(t *testing.T, factory *fakeAgyAccountFactory, observation ports.AgyAccountObservation) (*agyAccountManager, *Service, agyAccountRecord) {
	t.Helper()
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	sourceID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	state := &testAgyDeviceSeed{
		active: testAgyDeviceAccount{AccountID: sourceID, Revision: 1},
	}
	manager := newAgyAccountManager(context.Background(),
		filepath.Join(root, "accounts"), filepath.Join(root, "pending"),
		filepath.Join(root, "staging"), globalHome, factory, nil)
	ids := []string{sourceID, agyTestAccountID}
	idx := 0
	manager.catalog.newID = func() string { id := ids[idx]; idx++; return id }
	sourceObs := ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle}
	sourceCredential := agyTestOAuthCredential("switch-source", "source-access")
	targetCredential := agyTestOAuthCredential("switch-target", "target-access")
	agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", sourceCredential, sourceObs)
	record := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", targetCredential, observation)
	if err := writeGlobalCredentialAtomic(manager.globalCredentialPath(), sourceCredential); err != nil {
		t.Fatal(err)
	}
	agySetTestDeviceAccount(manager, state.active)
	manager.accountStoreReady = true
	manager.reconciliation = domain.AgyDeviceReconciliation{
		Status: domain.AgyDeviceReconciliationVerified, ActiveAccountVerified: true,
	}
	svc := &Service{agyAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{})}
	return manager, svc, record
}

func TestSwitchAdmissionDoesNotOpenAgyWhenProviderIsUnavailable(t *testing.T) {
	email := "switch-test@example.com"
	observation := ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &email,
	}
	factory := &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			return nil, errors.New("provider unavailable")
		},
	}
	_, svc, record := agyNewSwitchAdmissionFixture(t, factory, observation)

	_, err := svc.PrepareAgyAccountForSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", record.Snapshot.ID)
	if err != nil {
		t.Fatalf("local switch admission was blocked by provider state: %v", err)
	}
	if factory.opens != 0 {
		t.Fatalf("switch admission opened %d Agy clients", factory.opens)
	}
}

func TestAgySwitchAdmissionIgnoresStaleAuthenticationFailure(t *testing.T) {
	email := "read-err@example.com"
	observation := ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &email,
	}
	factory := &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			return &fakeAgyAccountClient{readErr: errors.New("timeout reading account")}, nil
		},
	}
	_, svc, record := agyNewSwitchAdmissionFixture(t, factory, observation)

	_, err := svc.PrepareAgyAccountForSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", record.Snapshot.ID)
	if err != nil {
		t.Fatalf("stale authentication failure blocked local switch admission: %v", err)
	}
}

func TestAgySwitchAdmissionIgnoresCachedUnauthorizedState(t *testing.T) {
	email := "unauth@example.com"
	manager, svc, record := agyNewSwitchAdmissionFixture(t, &fakeAgyAccountFactory{}, ports.AgyAccountObservation{
		Authentication: domain.AgentAuthenticationAuthorized,
		Method:         domain.AgyAuthMethodGoogle,
		Email:          &email,
	})
	manager.requireReauthentication(record.Snapshot.ID)

	if _, err := svc.PrepareAgyAccountForSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", record.Snapshot.ID); err != nil {
		t.Fatalf("cached provider rejection blocked a locally valid credential: %v", err)
	}
}

type agySwitchFixture struct {
	manager *agyAccountManager
	service *Service
	state   *testAgyDeviceSeed
	source  agyAccountRecord
	target  agyAccountRecord
}

// agyNewSwitchFixture seeds two saved Google accounts, the source installed as
// the device credential, and a verified device projection.
func agyNewSwitchFixture(t *testing.T) agySwitchFixture {
	t.Helper()
	root := t.TempDir()
	globalHome := filepath.Join(root, "global-agy")
	if err := ensurePrivateDirectory(globalHome); err != nil {
		t.Fatal(err)
	}
	state := &testAgyDeviceSeed{active: testAgyDeviceAccount{AccountID: agyTestAccountID, Revision: 1}, found: true}
	manager := newAgyAccountManager(context.Background(), filepath.Join(root, "accounts"), filepath.Join(root, "pending"), filepath.Join(root, "staging"), globalHome, nil, nil)
	ids := []string{agyTestAccountID, "bb1e9a5d-37ad-43f8-83bd-13de8168f8af"}
	manager.catalog.newID = func() string { id := ids[0]; ids = ids[1:]; return id }
	observation := ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle}
	source := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "b60a377d-da68-4a61-86f2-f31f04c571f2", agyTestOAuthCredential("source-account", "source-access"), observation)
	target := agyCommitTestAccountWithCredential(t, manager.catalog, manager.pendingRoot, "1c5de3ab-82d0-4a68-a06b-8495cdeab909", agyTestOAuthCredential("target-account", "target-access"), observation)
	if err := agyWriteGlobalCredentialAtomic(manager.globalCredentialPath(), agyTestOAuthCredential("source-account", "source-access")); err != nil {
		t.Fatal(err)
	}
	agySetTestDeviceAccount(manager, state.active)
	manager.accountStoreReady = true
	manager.mu.Lock()
	manager.markDeviceReconciledLocked(true, time.Now().UTC())
	manager.mu.Unlock()
	return agySwitchFixture{manager: manager, service: &Service{agyAccounts: manager, readiness: newReadinessCoordinator(readinessCoordinatorConfig{})}, state: state, source: source, target: target}
}

func TestAgyVerifySwitchTargetDoesNotRequireManagedDeviceSource(t *testing.T) {
	fixture := agyNewSwitchFixture(t)
	fixture.manager.mu.Lock()
	fixture.manager.reconciliation = domain.AgyDeviceReconciliation{Status: domain.AgyDeviceReconciliationNotChecked, ReasonCode: "not_checked"}
	fixture.manager.mu.Unlock()
	fixture.manager.factory = &fakeAgyAccountFactory{
		capabilities: supportedAgyAccountCapabilities(),
		open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
			return &fakeAgyAccountClient{read: ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle}}, nil
		},
	}

	if _, err := fixture.service.PrepareAgyAccountForSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", fixture.target.Snapshot.ID); err != nil {
		t.Fatalf("PrepareAgyAccountForSwitch: %v", err)
	}
	fixture.manager.mu.Lock()
	reconciliation := fixture.manager.reconciliation
	fixture.manager.mu.Unlock()
	if reconciliation.Status != domain.AgyDeviceReconciliationNotChecked || reconciliation.ActiveAccountVerified {
		t.Fatalf("target verification unexpectedly changed device reconciliation = %#v", reconciliation)
	}
}

func TestAgyRecentVerifiedReconciliationDoesNotScheduleAnotherRead(t *testing.T) {
	fixture := agyNewSwitchFixture(t)
	factory := &fakeAgyAccountFactory{open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
		return nil, errors.New("fresh device reconciliation unexpectedly opened Antigravity")
	}}
	fixture.manager.factory = factory
	fixture.manager.requestGlobalReconciliationIfNeeded()

	fixture.manager.mu.Lock()
	requested := fixture.manager.reconcileRequested
	fixture.manager.mu.Unlock()
	factory.mu.Lock()
	opens := factory.opens
	factory.mu.Unlock()
	if requested || opens != 0 {
		t.Fatalf("fresh reconciliation scheduled work: requested=%v opens=%d", requested, opens)
	}
}

func TestAgyRepeatedReconciliationRequestsJoinOneBackgroundRead(t *testing.T) {
	fixture := agyNewSwitchFixture(t)
	release, err := fixture.manager.acquireAccountMutation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fixture.manager.mu.Lock()
	fixture.manager.reconciliation = domain.AgyDeviceReconciliation{Status: domain.AgyDeviceReconciliationNotChecked, ReasonCode: "not_checked"}
	fixture.manager.mu.Unlock()

	for range 10 {
		fixture.manager.requestGlobalReconciliationIfNeeded()
	}
	deadline := time.Now().Add(time.Second)
	for {
		fixture.manager.mu.Lock()
		started := fixture.manager.reconcile != nil
		fixture.manager.mu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background reconciliation did not start")
		}
		time.Sleep(time.Millisecond)
	}
	for range 10 {
		fixture.manager.requestGlobalReconciliationIfNeeded()
	}
	fixture.manager.mu.Lock()
	call := fixture.manager.reconcile
	fixture.manager.mu.Unlock()
	if call == nil {
		t.Fatal("concurrent requests did not join the in-flight reconciliation")
	}
	release()

	deadline = time.Now().Add(time.Second)
	for {
		fixture.manager.mu.Lock()
		finished := fixture.manager.reconciliation.Status == domain.AgyDeviceReconciliationVerified && !fixture.manager.reconcileRequested
		fixture.manager.mu.Unlock()
		if finished {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background reconciliation did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestInspectAgyAccountSwitchClassifiesTargetWithoutChangingProjection(t *testing.T) {
	fixture := agyNewSwitchFixture(t)
	targetCredential := agyTestOAuthCredential("target-account", "target-access")
	if err := writePrivateFileAtomic(filepath.Join(fixture.target.Home, agyCredentialFilename), targetCredential); err != nil {
		t.Fatal(err)
	}
	if err := agyWriteGlobalCredentialAtomic(fixture.manager.globalCredentialPath(), targetCredential); err != nil {
		t.Fatal(err)
	}
	state, err := fixture.service.InspectAgyAccountSwitch(context.Background(), "6f8dfc76-8db4-4621-8974-c480093e0d55", domain.AgyAccountSwitchSourceManaged, fixture.source.Snapshot.ID, fixture.target.Snapshot.ID)
	if err != nil || state != domain.AgyAccountSwitchTargetInstalled {
		t.Fatalf("inspection = %q, %v", state, err)
	}
	if fixture.manager.deviceAccountID != fixture.source.Snapshot.ID {
		t.Fatalf("inspection changed device projection = %q", fixture.manager.deviceAccountID)
	}
}

func TestAgyCredentialActivationDoesNotOpenAgy(t *testing.T) {
	fixture := agyNewSwitchFixture(t)
	fixture.manager.factory = &fakeAgyAccountFactory{open: func(ports.AgyAccountContext) (ports.AgyAccountClient, error) {
		t.Fatal("credential activation opened Antigravity")
		return nil, nil
	}}
	err := fixture.manager.activateFromCredentialLocked(context.Background(), fixture.target.Snapshot.ID, filepath.Join(fixture.target.Home, agyCredentialFilename), agyTestOAuthCredential("source-account", "source-access"))
	if err != nil {
		t.Fatal(err)
	}
	if fixture.manager.activeAccountID() != fixture.target.Snapshot.ID {
		t.Fatalf("active account = %q", fixture.manager.activeAccountID())
	}
	if fixture.manager.factory.(*fakeAgyAccountFactory).opens != 0 {
		t.Fatal("credential activation opened Antigravity")
	}
}
