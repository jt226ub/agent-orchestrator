package controllers_test

import (
	"context"
	"encoding/json"
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

type fakeAgyCapacity struct {
	result   domain.AgyCapacitySnapshot
	ensured  bool
	forced   bool
	cachedOK bool
}

func (f *fakeAgyCapacity) CachedAgyCapacity(context.Context) (domain.AgyCapacitySnapshot, error) {
	f.cachedOK = true
	return f.result, nil
}

func (f *fakeAgyCapacity) EnsureAgyCapacity(_ context.Context, force bool) (domain.AgyCapacitySnapshot, error) {
	f.ensured, f.forced = true, force
	return f.result, nil
}

func agyCapacityFixture() domain.AgyCapacitySnapshot {
	used := 24.1
	remaining := 75.9
	group := "Gemini Models"
	other := "Claude and GPT models"
	fiveHours := int64(300)
	weekly := int64(10080)
	resetsAt := time.Date(2026, time.September, 23, 14, 16, 19, 0, time.UTC)
	checkedAt := time.Date(2026, time.September, 17, 15, 45, 0, 0, time.UTC)
	return domain.AgyCapacitySnapshot{
		State: domain.AgyCapacityAvailable, Freshness: domain.AgentReadinessFresh,
		UsedPercent: &used, RemainingPercent: &remaining, ResetsAt: &resetsAt, ObservedAt: &checkedAt, CheckedAt: &checkedAt, AttemptedAt: &checkedAt,
		ReasonCode: domain.AgyCapacityReasonAvailable, Reason: "available",
		Overall: &domain.AgyCapacityBucket{
			LimitID: "gemini-secret", DisplayName: &group,
			Primary:   &domain.AgyCapacityWindow{UsedPercent: 1.7, WindowDurationMinutes: &fiveHours, ResetsAt: &checkedAt},
			Secondary: &domain.AgyCapacityWindow{UsedPercent: 24.1, WindowDurationMinutes: &weekly, ResetsAt: &resetsAt},
		},
		AdditionalBuckets: []domain.AgyCapacityBucket{{
			LimitID: "3p", DisplayName: &other,
			Primary: &domain.AgyCapacityWindow{UsedPercent: 0, WindowDurationMinutes: &fiveHours},
		}},
	}
}

func newAgyCapacityServer(t *testing.T, fake *fakeAgyCapacity) *httptest.Server {
	t.Helper()
	return httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.DiscardHandler), nil, httpd.APIDeps{AgyCapacity: fake}, httpd.ControlDeps{}))
}

func TestAgyCapacityRoutesExposeCachedAndEnsureShapes(t *testing.T) {
	fake := &fakeAgyCapacity{result: agyCapacityFixture()}
	srv := newAgyCapacityServer(t, fake)
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/agy/capacity", "")
	text := string(body)
	if status != http.StatusOK || !fake.cachedOK || fake.ensured {
		t.Fatalf("GET status=%d cached=%v ensured=%v body=%s", status, fake.cachedOK, fake.ensured, body)
	}
	if !strings.Contains(text, `"state":"available"`) || !strings.Contains(text, `"remainingPercent":75.9`) || !strings.Contains(text, `"displayName":"Gemini Models"`) || !strings.Contains(text, `"windowDurationMinutes":10080`) {
		t.Fatalf("GET body=%s", body)
	}
	if strings.Contains(text, "gemini-secret") || strings.Contains(text, `"limitId"`) {
		t.Fatalf("GET body exposes provider limit identifiers: %s", body)
	}
	var decoded struct {
		AdditionalBuckets []json.RawMessage `json:"additionalBuckets"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || len(decoded.AdditionalBuckets) != 1 {
		t.Fatalf("GET additionalBuckets=%v err=%v", decoded.AdditionalBuckets, err)
	}

	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/capacity/ensure", `{"force":true}`)
	if status != http.StatusOK || !fake.ensured || !fake.forced || !strings.Contains(string(body), `"reasonCode":"capacity_available"`) {
		t.Fatalf("POST status=%d ensured=%v forced=%v body=%s", status, fake.ensured, fake.forced, body)
	}

	body, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/capacity/ensure", `{"unknown":true}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "INVALID_JSON") {
		t.Fatalf("POST strict decode status=%d body=%s", status, body)
	}
}

func TestAgyCapacityRoutesAnswerNotImplementedWithoutService(t *testing.T) {
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.DiscardHandler), nil, httpd.APIDeps{CodexAccounts: &fakeCodexAccounts{}}, httpd.ControlDeps{}))
	defer srv.Close()
	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/agents/agy/capacity", "")
	if status != http.StatusNotImplemented {
		t.Fatalf("GET status=%d body=%s, want 501", status, body)
	}
	_, status, _ = doRequest(t, srv, http.MethodPost, "/api/v1/agents/agy/capacity/ensure", `{}`)
	if status != http.StatusNotImplemented {
		t.Fatalf("POST status=%d, want 501", status)
	}
}
