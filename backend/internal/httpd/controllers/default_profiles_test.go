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
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	profilessvc "github.com/aoagents/agent-orchestrator/backend/internal/service/profiles"
)

type fakeDefaultProfiles struct {
	profiles map[string]domain.RoleProfile
	rules    map[string]string
	deleted  []string
}

func (f *fakeDefaultProfiles) snapshot() profilessvc.Snapshot {
	files := make([]profilessvc.RulesFileInfo, 0, len(f.rules))
	for name, content := range f.rules {
		files = append(files, profilessvc.RulesFileInfo{Name: name, SizeBytes: int64(len(content)), UpdatedAt: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)})
	}
	return profilessvc.Snapshot{Profiles: f.profiles, RulesFiles: files}
}

func (f *fakeDefaultProfiles) Get(context.Context) (profilessvc.Snapshot, error) {
	return f.snapshot(), nil
}

func (f *fakeDefaultProfiles) SetProfiles(_ context.Context, profiles map[string]domain.RoleProfile) (profilessvc.Snapshot, error) {
	if err := (domain.DefaultProfiles{Profiles: profiles}).Validate(); err != nil {
		return profilessvc.Snapshot{}, apierr.Invalid("INVALID_DEFAULT_PROFILES", err.Error(), nil)
	}
	f.profiles = profiles
	return f.snapshot(), nil
}

func (f *fakeDefaultProfiles) ReadRules(_ context.Context, name string) (profilessvc.RulesFile, error) {
	content, ok := f.rules[name]
	if !ok {
		return profilessvc.RulesFile{}, apierr.NotFound("RULES_FILE_NOT_FOUND", "missing")
	}
	return profilessvc.RulesFile{Name: name, Content: content, UpdatedAt: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeDefaultProfiles) WriteRules(ctx context.Context, name, content string) (profilessvc.RulesFile, error) {
	if !profilessvc.ValidRulesFileName(name) {
		return profilessvc.RulesFile{}, apierr.Invalid("RULES_FILE_NAME_INVALID", "bad name", nil)
	}
	f.rules[name] = content
	return f.ReadRules(ctx, name)
}

func (f *fakeDefaultProfiles) DeleteRules(_ context.Context, name string) error {
	delete(f.rules, name)
	f.deleted = append(f.deleted, name)
	return nil
}

func newDefaultProfilesServer(t *testing.T, fake *fakeDefaultProfiles) *httptest.Server {
	t.Helper()
	return httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.DiscardHandler), nil, httpd.APIDeps{DefaultProfiles: fake}, httpd.ControlDeps{}))
}

func TestDefaultProfilesRoutesRoundTripProfilesAndRulesFiles(t *testing.T) {
	fake := &fakeDefaultProfiles{profiles: map[string]domain.RoleProfile{}, rules: map[string]string{"contract.md": "# Drive"}}
	srv := newDefaultProfilesServer(t, fake)
	defer srv.Close()

	body, status, _ := doRequest(t, srv, http.MethodGet, "/api/v1/settings/profiles", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"profiles":{}`) || !strings.Contains(string(body), `"name":"contract.md"`) || !strings.Contains(string(body), `"sizeBytes":7`) {
		t.Fatalf("GET status=%d body=%s", status, body)
	}

	body, status, _ = doRequest(t, srv, http.MethodPut, "/api/v1/settings/profiles", `{"profiles":{"flash-coder":{"agent":"agy","agentConfig":{"model":"gemini-3.8-flash-high"},"rulesFile":"flash-coder.md"}}}`)
	if status != http.StatusOK || !strings.Contains(string(body), `"flash-coder"`) || fake.profiles["flash-coder"].RulesFile != "flash-coder.md" {
		t.Fatalf("PUT status=%d body=%s stored=%+v", status, body, fake.profiles)
	}
	body, status, _ = doRequest(t, srv, http.MethodPut, "/api/v1/settings/profiles", `{"profiles":{"bad":{"agent":"nope"}}}`)
	if status != http.StatusBadRequest || !strings.Contains(string(body), "INVALID_DEFAULT_PROFILES") {
		t.Fatalf("PUT invalid status=%d body=%s", status, body)
	}
	_, status, _ = doRequest(t, srv, http.MethodPut, "/api/v1/settings/profiles", `{"unknown":true}`)
	if status != http.StatusBadRequest {
		t.Fatalf("PUT strict decode status=%d", status)
	}

	body, status, _ = doRequest(t, srv, http.MethodGet, "/api/v1/settings/rules/contract.md", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"content":"# Drive"`) {
		t.Fatalf("GET rules status=%d body=%s", status, body)
	}
	_, status, _ = doRequest(t, srv, http.MethodGet, "/api/v1/settings/rules/worker.md", "")
	if status != http.StatusNotFound {
		t.Fatalf("GET missing rules status=%d", status)
	}
	body, status, _ = doRequest(t, srv, http.MethodPut, "/api/v1/settings/rules/worker.md", `{"content":"Quote test output verbatim."}`)
	if status != http.StatusOK || fake.rules["worker.md"] != "Quote test output verbatim." || !strings.Contains(string(body), `"name":"worker.md"`) {
		t.Fatalf("PUT rules status=%d body=%s stored=%q", status, body, fake.rules["worker.md"])
	}
	_, status, _ = doRequest(t, srv, http.MethodPut, "/api/v1/settings/rules/worker.md", `{}`)
	if status != http.StatusBadRequest {
		t.Fatalf("PUT rules without content status=%d", status)
	}
	_, status, _ = doRequest(t, srv, http.MethodPut, "/api/v1/settings/rules/notes.txt", `{"content":"x"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("PUT rules with a bad name status=%d", status)
	}
	_, status, _ = doRequest(t, srv, http.MethodDelete, "/api/v1/settings/rules/worker.md", "")
	if status != http.StatusNoContent || len(fake.deleted) != 1 || fake.deleted[0] != "worker.md" {
		t.Fatalf("DELETE status=%d deleted=%v", status, fake.deleted)
	}
}

func TestDefaultProfilesRoutesAnswerNotImplementedWithoutService(t *testing.T) {
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.DiscardHandler), nil, httpd.APIDeps{CodexAccounts: &fakeCodexAccounts{}}, httpd.ControlDeps{}))
	defer srv.Close()
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/settings/profiles"},
		{http.MethodPut, "/api/v1/settings/profiles"},
		{http.MethodGet, "/api/v1/settings/rules/worker.md"},
		{http.MethodPut, "/api/v1/settings/rules/worker.md"},
		{http.MethodDelete, "/api/v1/settings/rules/worker.md"},
	} {
		if _, status, _ := doRequest(t, srv, route.method, route.path, `{}`); status != http.StatusNotImplemented {
			t.Fatalf("%s %s status=%d, want 501", route.method, route.path, status)
		}
	}
}
