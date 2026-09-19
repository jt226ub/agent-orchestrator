package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/shellterm"
)

type fakeAgyAccounts struct {
	result              agentsvc.AgyAccounts
	ensureIDs           []string
	forceAuthentication bool
	forceReconciliation bool
	events              chan agentsvc.AgyAccounts
	loginStart          agentsvc.AgyAccountLoginTerminalStart
	verifiedOperation   string
	cancelledOperation  string
	reauthenticatedID   string
	loggedOutID         string
	deletedID           string
	switchConfig        ports.AgyAccountSwitchConfig
	switchResult        domain.AgyAccountSwitch
	switchErr           error
	switchReadID        string
	switchReadResult    domain.AgyAccountSwitch
	switchReadErr       error
}

func (f *fakeAgyAccounts) CachedAgyAccounts(context.Context) (agentsvc.AgyAccounts, error) {
	return f.result, nil
}
func (f *fakeAgyAccounts) EnsureAgyAccounts(_ context.Context, ids []string, options agentsvc.AgyAccountEnsureOptions) (agentsvc.AgyAccounts, error) {
	f.ensureIDs, f.forceAuthentication, f.forceReconciliation = ids, options.ForceAuthentication, options.ForceDeviceReconciliation
	return f.result, nil
}
func (f *fakeAgyAccounts) SubscribeAgyAccounts(ctx context.Context) (<-chan agentsvc.AgyAccounts, error) {
	if f.events != nil {
		return f.events, nil
	}
	ch := make(chan agentsvc.AgyAccounts)
	go func() { <-ctx.Done(); close(ch) }()
	return ch, nil
}
func (f *fakeAgyAccounts) OpenAgyAccountLoginTerminal(context.Context) (agentsvc.AgyAccountLoginTerminalStart, error) {
	return f.loginStart, nil
}
func (f *fakeAgyAccounts) OpenAgyAccountReauthenticationTerminal(_ context.Context, id string) (agentsvc.AgyAccountLoginTerminalStart, error) {
	f.reauthenticatedID = id
	return f.loginStart, nil
}
func (f *fakeAgyAccounts) LogoutAgyAccount(_ context.Context, id string) (agentsvc.AgyAccounts, error) {
	f.loggedOutID = id
	return f.result, nil
}
func (f *fakeAgyAccounts) DeleteAgyAccount(_ context.Context, id string) (agentsvc.AgyAccounts, error) {
	f.deletedID = id
	return f.result, nil
}
func (f *fakeAgyAccounts) VerifyAgyAccountLogin(_ context.Context, id string) (domain.AgyAccountLoginOperation, error) {
	f.verifiedOperation = id
	return domain.AgyAccountLoginOperation{OperationID: id, Status: domain.AgyAccountLoginRetryable, ReasonCode: domain.AgyAccountLoginReasonFailed, Reason: "try again"}, nil
}
func (f *fakeAgyAccounts) CancelAgyAccountLogin(_ context.Context, id string) (domain.AgyAccountLoginOperation, error) {
	f.cancelledOperation = id
	return domain.AgyAccountLoginOperation{OperationID: id, Status: domain.AgyAccountLoginCancelled, ReasonCode: domain.AgyAccountLoginReasonCancelled, Reason: "cancelled"}, nil
}
func (f *fakeAgyAccounts) StartAgyAccountSwitch(_ context.Context, cfg ports.AgyAccountSwitchConfig) (domain.AgyAccountSwitch, error) {
	f.switchConfig = cfg
	return f.switchResult, f.switchErr
}
func (f *fakeAgyAccounts) GetAgyAccountSwitch(_ context.Context, id string) (domain.AgyAccountSwitch, error) {
	f.switchReadID = id
	return f.switchReadResult, f.switchReadErr
}
func agyAccountsFixture() agentsvc.AgyAccounts {
	supported := domain.AgyCapabilityObservation{State: domain.AgyCapabilitySupported, ReasonCode: "supported", Reason: "available"}
	remaining := 95.0
	used := 5.0
	email := "person@example.com"
	bucketName := "Code review"
	windowMinutes := int64(300)
	resetsAt := time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC)
	return agentsvc.AgyAccounts{
		ActiveAccountID: "72d4db6e-da2c-414c-a6a9-fdbd09a006b6",
		AccountRevision: 3,
		DeviceReconciliation: domain.AgyDeviceReconciliation{
			Status: domain.AgyDeviceReconciliationVerified, ActiveAccountVerified: true,
			ReasonCode: "verified",
		},
		Accounts: []domain.AgyAccountSnapshot{{
			ID: "72d4db6e-da2c-414c-a6a9-fdbd09a006b6", Label: "person@example.com", Source: domain.AgyAccountSourceManaged,
			Status: domain.AgyAccountStatusValid, ReasonCode: domain.AgyAccountReasonValid, Reason: "available", Active: true,
			Authentication: domain.AgentAuthenticationObservation{State: domain.AgentAuthenticationAuthorized, Freshness: domain.AgentReadinessFresh, ReasonCode: domain.AgentReadinessReasonAuthorized, Reason: "signed in"},
			AuthMethod:     domain.AgyAuthMethodGoogle,
			AccountEmail:   &email,
			Capacity: domain.AgyCapacitySnapshot{
				State: domain.AgyCapacityAvailable, Freshness: domain.AgentReadinessFresh,
				UsedPercent: &used, RemainingPercent: &remaining, ResetsAt: &resetsAt,
				ReasonCode: domain.AgyCapacityReasonAvailable, Reason: "available",
				AdditionalBuckets: []domain.AgyCapacityBucket{{
					LimitID: "provider-limit-secret", DisplayName: &bucketName,
					Primary: &domain.AgyCapacityWindow{UsedPercent: 12, WindowDurationMinutes: &windowMinutes, ResetsAt: &resetsAt},
				}},
			},
		}},
		Capabilities: domain.AgyAccountCapabilities{AccountRead: supported, NativeLogin: supported, CapacityRead: supported, GlobalSwitch: supported},
	}
}

func newAgyAccountServer(t *testing.T, fake *fakeAgyAccounts) *httptest.Server {
	t.Helper()
	return httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.DiscardHandler), nil, httpd.APIDeps{AgyAccounts: fake}, httpd.ControlDeps{}))
}

func TestAgyAccountRoutesExposeSafeCachedAndEnsureShapes(t *testing.T) {
	fixture := agyAccountsFixture()
	fixture.CurrentSwitch = &domain.AgyAccountSwitch{
		ID: "switch-1", SourceAccountID: "source-account", TargetAccountID: "target-account",
		Phase: domain.AgyAccountSwitchRecoveryRequired,
	}
	fake := &fakeAgyAccounts{result: fixture}
	srv := newAgyAccountServer(t, fake)
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/agy/accounts", "")
	text := string(body)
	if status != http.StatusOK || !strings.Contains(text, `"activeAccountId"`) || !strings.Contains(text, `"accountEmail":"person@example.com"`) || !strings.Contains(text, `"remainingPercent":95`) || !strings.Contains(text, `"displayName":"Code review"`) {
		t.Fatalf("GET status=%d body=%s", status, body)
	}
	for _, forbidden := range []string{"provider-limit-secret", "private-generation-id", "limitId", "antigravity-oauth-token", "credential-home", "pending-accounts", "HOME=", "/Users/", "/home/", "nativeSessionId", "generationId", "idempotencyKey"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("GET leaked %q: %s", forbidden, body)
		}
	}
	for _, unusedCapability := range []string{"accountRead", "capacityRead", "usageRead"} {
		if strings.Contains(text, `"`+unusedCapability+`"`) {
			t.Fatalf("GET exposed unused capability %q: %s", unusedCapability, body)
		}
	}
	for _, usedCapability := range []string{"nativeLogin", "globalSwitch"} {
		if !strings.Contains(text, `"`+usedCapability+`"`) {
			t.Fatalf("GET omitted UI capability %q: %s", usedCapability, body)
		}
	}
	var response controllers.AgyAccountsResponse
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode redacted response: %v", err)
	}
	if len(response.Accounts) != 1 {
		t.Fatalf("decoded accounts = %#v", response.Accounts)
	}
	if response.DeviceReconciliation.Status != string(domain.AgyDeviceReconciliationVerified) || !response.DeviceReconciliation.ActiveAccountVerified {
		t.Fatalf("decoded device reconciliation = %#v", response.DeviceReconciliation)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/accounts/ensure", `{"accountIds":["a","a"],"forceAuthentication":true,"forceDeviceReconciliation":true}`)
	if status != http.StatusOK || len(fake.ensureIDs) != 2 || !fake.forceAuthentication || !fake.forceReconciliation {
		t.Fatalf("ensure status=%d ids=%#v forceAuthentication=%v forceReconciliation=%v body=%s", status, fake.ensureIDs, fake.forceAuthentication, fake.forceReconciliation, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/accounts/ensure", `{"accountIds":[],"unknown":true}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"INVALID_JSON"`) {
		t.Fatalf("strict ensure status=%d body=%s", status, body)
	}
}

func TestAgyAccountReadRoutesStayAvailableDuringDeviceReconciliationFailure(t *testing.T) {
	fixture := agyAccountsFixture()
	fixture.DeviceReconciliation = domain.AgyDeviceReconciliation{
		Status:     domain.AgyDeviceReconciliationTemporarilyUnavailable,
		ReasonCode: "account_read_inconclusive", Retryable: true,
	}
	fixture.Accounts[0].Active = false
	fake := &fakeAgyAccounts{result: fixture}
	srv := newAgyAccountServer(t, fake)
	defer srv.Close()

	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodGet, path: "/api/v1/agents/agy/accounts"},
		{method: http.MethodPost, path: "/api/v1/agents/agy/accounts/ensure", body: `{"accountIds":[]}`},
	} {
		body, status, _ := doRequest(t, srv, request.method, request.path, request.body)
		if status != http.StatusOK || !strings.Contains(string(body), `"status":"temporarily_unavailable"`) ||
			!strings.Contains(string(body), `"reasonCode":"account_read_inconclusive"`) || strings.Contains(string(body), `"active":true`) {
			t.Fatalf("%s %s status=%d body=%s", request.method, request.path, status, body)
		}
	}
}

func TestAgyAccountLoginTerminalAndVerificationRoutesExposeNoCommandOrPath(t *testing.T) {
	fake := &fakeAgyAccounts{result: agyAccountsFixture(), loginStart: agentsvc.AgyAccountLoginTerminalStart{
		Operation:     domain.AgyAccountLoginOperation{OperationID: "op-1", Status: domain.AgyAccountLoginPending, ReasonCode: domain.AgyAccountLoginReasonPending, Reason: "waiting", ExpiresAt: time.Now().Add(time.Minute)},
		ShellTerminal: shellterm.ShellTerminal{HandleID: "shellterm-login-1", WorkingDir: "/private/secret", Title: "Add Agy account", CreatedAt: time.Now()},
	}}
	srv := newAgyAccountServer(t, fake)
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/accounts/login-terminal", "")
	text := string(body)
	if status != http.StatusAccepted || !strings.Contains(text, `"operationId":"op-1"`) || !strings.Contains(text, `"handleId":"shellterm-login-1"`) {
		t.Fatalf("login status=%d body=%s", status, body)
	}
	for _, forbidden := range []string{"workingDir", "/private/secret", "argv", "AGY_HOME"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("login response leaked %q: %s", forbidden, body)
		}
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/accounts/login-terminal", `{}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"INVALID_REQUEST_BODY"`) {
		t.Fatalf("body rejection status=%d body=%s", status, body)
	}
	_, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/accounts/login-operations/op-1/verify", "")
	if status != http.StatusOK || fake.verifiedOperation != "op-1" {
		t.Fatalf("verify status=%d id=%q", status, fake.verifiedOperation)
	}
}

func TestAgyAccountCachedAndEventResponsesExposeSafeActiveLogin(t *testing.T) {
	result := agyAccountsFixture()
	result.ActiveLogin = &agentsvc.AgyActiveLogin{
		OperationID: "login-op-safe", AccountID: result.ActiveAccountID,
		Status: domain.AgyAccountLoginPending, ReasonCode: domain.AgyAccountLoginReasonPending,
		Reason: "waiting", ExpiresAt: time.Date(2026, time.September, 2, 12, 15, 0, 0, time.UTC),
		ShellTerminal: agentsvc.AgyLoginTerminalDisplay{
			HandleID: "shellterm-login-safe", Title: "Sign in to Agy account",
			CreatedAt: time.Date(2026, time.September, 2, 12, 0, 0, 0, time.UTC),
		},
	}
	events := make(chan agentsvc.AgyAccounts, 1)
	events <- result
	close(events)
	fake := &fakeAgyAccounts{result: result, events: events}
	srv := newAgyAccountServer(t, fake)
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/agy/accounts", "")
	if status != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", status, body)
	}
	assertSafeActiveLogin := func(t *testing.T, text string) {
		t.Helper()
		for _, want := range []string{`"activeLogin"`, `"operationId":"login-op-safe"`, `"accountId":"` + result.ActiveAccountID + `"`, `"handleId":"shellterm-login-safe"`, `"title":"Sign in to Agy account"`} {
			if !strings.Contains(text, want) {
				t.Fatalf("active login missing %s: %s", want, text)
			}
		}
		for _, forbidden := range []string{"pendingDir", "workingDir", "AGY_HOME", "argv", "env", "credential"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("active login leaked %q: %s", forbidden, text)
			}
		}
	}
	assertSafeActiveLogin(t, string(body))

	response, err := http.Get(srv.URL + "/api/v1/agents/agy/accounts/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	eventBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	assertSafeActiveLogin(t, string(eventBody))
}

func TestAgyAccountReauthenticationAndLogoutRoutesTargetAnExistingAccount(t *testing.T) {
	accountID := "72d4db6e-da2c-414c-a6a9-fdbd09a006b6"
	fake := &fakeAgyAccounts{result: agyAccountsFixture(), loginStart: agentsvc.AgyAccountLoginTerminalStart{
		Operation: domain.AgyAccountLoginOperation{
			OperationID: "op-reauth", AccountID: accountID, Status: domain.AgyAccountLoginPending,
			ReasonCode: domain.AgyAccountLoginReasonPending, Reason: "waiting", ExpiresAt: time.Now().Add(time.Minute),
		},
		ShellTerminal: shellterm.ShellTerminal{HandleID: "shellterm-reauth", WorkingDir: "/private/secret", Title: "Sign in to Agy account", CreatedAt: time.Now()},
	}}
	srv := newAgyAccountServer(t, fake)
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/accounts/"+accountID+"/login-terminal", "")
	if status != http.StatusAccepted || fake.reauthenticatedID != accountID || !strings.Contains(string(body), `"accountId":"`+accountID+`"`) || strings.Contains(string(body), "/private/secret") {
		t.Fatalf("reauthentication status=%d id=%q body=%s", status, fake.reauthenticatedID, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/accounts/"+accountID+"/logout", "")
	if status != http.StatusOK || fake.loggedOutID != accountID || !strings.Contains(string(body), `"accountRevision":3`) {
		t.Fatalf("logout status=%d id=%q body=%s", status, fake.loggedOutID, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/accounts/"+accountID+"/logout", `{}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"INVALID_REQUEST_BODY"`) {
		t.Fatalf("logout body rejection status=%d body=%s", status, body)
	}
}

func TestAgyAccountDeleteRouteTargetsSignedOutAccount(t *testing.T) {
	accountID := "72d4db6e-da2c-414c-a6a9-fdbd09a006b6"
	fake := &fakeAgyAccounts{result: agyAccountsFixture()}
	srv := newAgyAccountServer(t, fake)
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodDelete, "/api/v1/agents/agy/accounts/"+accountID, "")
	if status != http.StatusOK || fake.deletedID != accountID || !strings.Contains(string(body), `"accountRevision":3`) {
		t.Fatalf("delete status=%d id=%q body=%s", status, fake.deletedID, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodDelete, "/api/v1/agents/agy/accounts/"+accountID, `{}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"INVALID_REQUEST_BODY"`) {
		t.Fatalf("delete body rejection status=%d body=%s", status, body)
	}
}

func TestAgyAccountSwitchRequiresIdempotencyAndRedactsPrivateIdentity(t *testing.T) {
	fake := &fakeAgyAccounts{result: agyAccountsFixture(), switchResult: domain.AgyAccountSwitch{
		ID: "switch-1", SourceAccountID: "source", TargetAccountID: "target", Phase: domain.AgyAccountSwitchRequested,
		IdempotencyKey: "private-key",
	}}
	srv := newAgyAccountServer(t, fake)
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/account-switches", `{"targetAccountId":"target","expectedAccountRevision":3,"idempotencyKey":""}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"code":"IDEMPOTENCY_KEY_REQUIRED"`) {
		t.Fatalf("missing key status=%d body=%s", status, body)
	}
	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/account-switches", `{"targetAccountId":"target","expectedAccountRevision":3,"idempotencyKey":"request-key"}`)
	text := string(body)
	if status != http.StatusAccepted || fake.switchConfig.IdempotencyKey != "request-key" || strings.Contains(text, `"sessions"`) {
		t.Fatalf("switch status=%d config=%#v body=%s", status, fake.switchConfig, body)
	}
	for _, forbidden := range []string{"private-key"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("switch leaked %q: %s", forbidden, body)
		}
	}
}

func TestAgyAccountSwitchReadReturnsDurableStateAndRedactsPrivateIdentity(t *testing.T) {
	fake := &fakeAgyAccounts{result: agyAccountsFixture(), switchReadResult: domain.AgyAccountSwitch{
		ID: "switch-1", SourceAccountID: "source", TargetAccountID: "target", Phase: domain.AgyAccountSwitchCompleted,
		IdempotencyKey: "private-key",
	}}
	srv := newAgyAccountServer(t, fake)
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/agy/account-switches/switch-1", "")
	text := string(body)
	if status != http.StatusOK || fake.switchReadID != "switch-1" || !strings.Contains(text, `"phase":"completed"`) {
		t.Fatalf("switch read status=%d id=%q body=%s", status, fake.switchReadID, body)
	}
	for _, forbidden := range []string{"private-key"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("switch read leaked %q: %s", forbidden, body)
		}
	}
}

func TestAgyAccountSwitchCancelRouteIsNotRegistered(t *testing.T) {
	fake := &fakeAgyAccounts{result: agyAccountsFixture()}
	srv := newAgyAccountServer(t, fake)
	defer srv.Close()
	_, status, _ := doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/account-switches/switch-1/cancel", "")
	if status != http.StatusNotFound {
		t.Errorf("cancel status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestAgyAccountEventStreamSendsNamedCachedState(t *testing.T) {
	events := make(chan agentsvc.AgyAccounts, 1)
	events <- agyAccountsFixture()
	close(events)
	fake := &fakeAgyAccounts{result: agyAccountsFixture(), events: events}
	srv := newAgyAccountServer(t, fake)
	defer srv.Close()
	response, err := http.Get(srv.URL + "/api/v1/agents/agy/accounts/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if response.StatusCode != http.StatusOK || !strings.Contains(text, "event: agy_account") || !strings.Contains(text, `"accountRevision":3`) || !strings.Contains(text, `"displayName":"Code review"`) {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	for _, forbidden := range []string{"provider-limit-secret", "limitId"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("event leaked %q: %s", forbidden, body)
		}
	}
}
