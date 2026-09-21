package controllers_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
)

type fakeClaudeCodeUsage struct {
	result   domain.ClaudeCodePlanUsageSnapshot
	ensured  bool
	forced   bool
	cachedOK bool
}

func (f *fakeClaudeCodeUsage) CachedClaudeCodeUsage(context.Context) (domain.ClaudeCodePlanUsageSnapshot, error) {
	f.cachedOK = true
	return f.result, nil
}

func (f *fakeClaudeCodeUsage) EnsureClaudeCodeUsage(_ context.Context, force bool) (domain.ClaudeCodePlanUsageSnapshot, error) {
	f.ensured, f.forced = true, force
	return f.result, nil
}

func claudeCodeUsageFixture() domain.ClaudeCodePlanUsageSnapshot {
	plan := "max"
	remaining := 58.0
	resetsAt := time.Date(2026, time.September, 21, 17, 0, 0, 0, time.UTC)
	checkedAt := time.Date(2026, time.September, 21, 15, 45, 0, 0, time.UTC)
	return domain.ClaudeCodePlanUsageSnapshot{
		State: domain.ClaudeCodePlanUsageAvailable, Freshness: domain.AgentReadinessFresh, Plan: &plan,
		Identity:         &domain.ClaudeCodePlanUsageIdentity{EmailAddress: "dev@example.com", DisplayName: "Dev", OrganizationName: "Dev's Organization"},
		Promotion:        &domain.ClaudeCodePlanPromotion{PercentIncrease: 50, EndsOn: "2026-09-30"},
		RemainingPercent: &remaining,
		Windows: []domain.ClaudeCodePlanUsageWindow{
			{ID: "five_hour", DisplayName: "5-hour limit", UsedPercent: 42, ResetsAt: &resetsAt},
			{ID: "seven_day", DisplayName: "Weekly — all models", UsedPercent: 12},
		},
		ObservedAt: &checkedAt, CheckedAt: &checkedAt, AttemptedAt: &checkedAt,
		ReasonCode: domain.ClaudeCodePlanUsageReasonAvailable, Reason: "available",
	}
}

func newClaudeCodeUsageServer(t *testing.T, fake *fakeClaudeCodeUsage) *httptest.Server {
	t.Helper()
	return httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.DiscardHandler), nil, httpd.APIDeps{ClaudeCodeUsage: fake}, httpd.ControlDeps{}))
}

func TestClaudeCodeUsageRoutesExposeCachedAndEnsureShapes(t *testing.T) {
	fake := &fakeClaudeCodeUsage{result: claudeCodeUsageFixture()}
	srv := newClaudeCodeUsageServer(t, fake)
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/claude-code/usage", "")
	text := string(body)
	if status != http.StatusOK || !fake.cachedOK || fake.ensured {
		t.Fatalf("GET status=%d cached=%v ensured=%v body=%s", status, fake.cachedOK, fake.ensured, body)
	}
	for _, want := range []string{`"state":"available"`, `"plan":"max"`, `"remainingPercent":58`, `"emailAddress":"dev@example.com"`, `"organizationName":"Dev's Organization"`, `"percentIncrease":50`, `"displayName":"Weekly — all models"`, `"usedPercent":42`} {
		if !strings.Contains(text, want) {
			t.Fatalf("GET body lacks %s: %s", want, body)
		}
	}
	for _, forbidden := range []string{"accessToken", "refreshToken", "Keychain", "claudeAiOauth"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("GET body exposes %s: %s", forbidden, body)
		}
	}

	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/claude-code/usage/ensure", `{"force":true}`)
	if status != http.StatusOK || !fake.ensured || !fake.forced || !strings.Contains(string(body), `"reasonCode":"plan_usage_available"`) {
		t.Fatalf("POST status=%d ensured=%v forced=%v body=%s", status, fake.ensured, fake.forced, body)
	}

	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/claude-code/usage/ensure", `{"unknown":true}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "INVALID_JSON") {
		t.Fatalf("POST strict decode status=%d body=%s", status, body)
	}
}

func TestClaudeCodeUsageRoutesAnswerNotImplementedWithoutService(t *testing.T) {
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.DiscardHandler), nil, httpd.APIDeps{CodexAccounts: &fakeCodexAccounts{}}, httpd.ControlDeps{}))
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/claude-code/usage", "")
	if status != http.StatusNotImplemented {
		t.Fatalf("GET status=%d body=%s, want 501", status, body)
	}
	_, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/claude-code/usage/ensure", `{}`)
	if status != http.StatusNotImplemented {
		t.Fatalf("POST status=%d, want 501", status)
	}
}
