package claudecode

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type usageTestKeychain struct {
	value      []byte
	supported  bool
	err        error
	getService string
	getAccount string
}

func (k *usageTestKeychain) Supported() bool { return k.supported }
func (k *usageTestKeychain) Get(_ context.Context, service, account string) ([]byte, bool, error) {
	k.getService = service
	k.getAccount = account
	return append([]byte(nil), k.value...), len(k.value) > 0, k.err
}

type usageTestDoer func(*http.Request) (*http.Response, error)

func (f usageTestDoer) Do(request *http.Request) (*http.Response, error) { return f(request) }

func usageTestReader(t *testing.T, keychain *usageTestKeychain, config string) *claudeCodeUsageReader {
	t.Helper()
	home := t.TempDir()
	if config != "" {
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &claudeCodeUsageReader{
		keychain: keychain, account: "tester", home: home, endpoint: "https://example.test/api/oauth/usage",
		now: func() time.Time { return time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC) },
	}
}

func TestClaudeUsageReaderReadsTheDeviceCredentialOnce(t *testing.T) {
	config := `{
		"oauthAccount":{"accountUuid":"11111111-1111-4111-8111-111111111111","emailAddress":"dev@example.com","displayName":"Dev","organizationName":"Dev's Organization"},
		"cachedGrowthBookFeatures":{"tengu_rate_limit_promo_notices":[
			{"bar":"seven_day","text":"+50% weekly limits promo through Sep 13 · clau.de/cc-50-promo","variant":"claude"}
		]}
	}`
	keychain := &usageTestKeychain{supported: true, value: []byte(`{"claudeAiOauth":{"accessToken":"device-secret","subscriptionType":"max","rateLimitTier":"default_claude_max_5x"}}`)}
	reader := usageTestReader(t, keychain, config)
	requests := 0
	reader.httpClient = usageTestDoer(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Method != http.MethodGet || request.URL.String() != reader.endpoint {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer device-secret" || request.Header.Get("anthropic-beta") != claudeOAuthBetaHeader {
			t.Fatalf("unexpected request headers")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{
			"five_hour":{"utilization":12.5,"resets_at":"2026-09-03T12:00:00Z"},
			"seven_day":{"utilization":42,"resets_at":"2026-09-08T00:00:00Z"},
			"seven_day_opus":null,
			"limits":[{"kind":"weekly_scoped","percent":21,"resets_at":"2026-09-09T00:00:00Z","scope":{"model":{"display_name":"Opus 5"}}}]
		}`))}, nil
	})

	result, err := reader.ReadPlanUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if keychain.getService != claudeCanonicalKeychainService || keychain.getAccount != "tester" || requests != 1 {
		t.Fatalf("usage read touched the wrong credential: service=%q account=%q requests=%d", keychain.getService, keychain.getAccount, requests)
	}
	if len(result.Windows) != 3 || result.Windows[0].ID != "five_hour" || result.Windows[0].UsedPercent != 12.5 || result.Windows[1].ID != "seven_day" || result.Windows[2].DisplayName != "Weekly — Opus 5" {
		t.Fatalf("usage windows = %+v", result.Windows)
	}
	if result.Plan == nil || *result.Plan != "max" {
		t.Fatalf("plan = %v", result.Plan)
	}
	if result.Identity == nil || result.Identity.EmailAddress != "dev@example.com" || result.Identity.OrganizationName != "Dev's Organization" || result.Identity.DisplayName != "Dev" {
		t.Fatalf("identity = %+v", result.Identity)
	}
	if result.Promotion == nil || result.Promotion.PercentIncrease != 50 || result.Promotion.EndsOn != "2026-09-13" {
		t.Fatalf("promotion = %+v", result.Promotion)
	}
}

func TestClaudeUsageReaderClassifiesCredentialAndProviderFailures(t *testing.T) {
	respond := func(status int, body string) usageTestDoer {
		return func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
	}
	valid := []byte(`{"claudeAiOauth":{"accessToken":"device-secret","subscriptionType":"pro"}}`)
	tests := []struct {
		name     string
		keychain *usageTestKeychain
		doer     usageTestDoer
		want     error
	}{
		{name: "unsupported platform", keychain: &usageTestKeychain{supported: false}, want: ports.ErrClaudeCodePlanUsageUnsupported},
		{name: "no keychain item", keychain: &usageTestKeychain{supported: true}, want: ports.ErrClaudeCodePlanUsageSignedOut},
		{name: "keychain read failed", keychain: &usageTestKeychain{supported: true, value: valid, err: errors.New("denied")}, want: ports.ErrClaudeCodePlanUsageUnavailable},
		{name: "item without account credential", keychain: &usageTestKeychain{supported: true, value: []byte(`{"mcpOAuth":{}}`)}, want: ports.ErrClaudeCodePlanUsageSignedOut},
		{name: "item is not json", keychain: &usageTestKeychain{supported: true, value: []byte(`nope`)}, want: ports.ErrClaudeCodePlanUsageInvalid},
		{name: "rate limited", keychain: &usageTestKeychain{supported: true, value: valid}, doer: respond(http.StatusTooManyRequests, ""), want: ports.ErrClaudeCodePlanUsageRateLimited},
		{name: "token rejected", keychain: &usageTestKeychain{supported: true, value: valid}, doer: respond(http.StatusUnauthorized, ""), want: ports.ErrClaudeCodePlanUsageSignedOut},
		{name: "server error", keychain: &usageTestKeychain{supported: true, value: valid}, doer: respond(http.StatusBadGateway, ""), want: ports.ErrClaudeCodePlanUsageUnavailable},
		{name: "no usable windows", keychain: &usageTestKeychain{supported: true, value: valid}, doer: respond(http.StatusOK, `{"five_hour":{"utilization":140}}`), want: ports.ErrClaudeCodePlanUsageInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader := usageTestReader(t, test.keychain, "")
			reader.httpClient = test.doer
			if reader.httpClient == nil {
				reader.httpClient = usageTestDoer(func(*http.Request) (*http.Response, error) { t.Fatal("provider was contacted"); return nil, nil })
			}
			result, err := reader.ReadPlanUsage(context.Background())
			if !errors.Is(err, test.want) {
				t.Fatalf("err = %v, want %v", err, test.want)
			}
			if len(result.Windows) != 0 {
				t.Fatalf("windows = %+v, want none", result.Windows)
			}
		})
	}
}

func TestClaudeOAuthPlanRecognizesNativeCredentialValues(t *testing.T) {
	tests := []struct {
		name             string
		subscriptionType string
		rateLimitTier    string
		want             string
	}{
		{name: "native pro subscription", subscriptionType: "claude_pro", want: "pro"},
		{name: "native max subscription", subscriptionType: "claude_max", want: "max"},
		{name: "plain max subscription", subscriptionType: "max", rateLimitTier: "default_claude_max_5x", want: "max"},
		{name: "legacy pro tier", rateLimitTier: "default_claude_pro", want: "pro"},
		{name: "legacy max tier", rateLimitTier: "default_claude_max_20x", want: "max"},
		{name: "subscription wins", subscriptionType: "claude_team", rateLimitTier: "default_claude_max_5x", want: "team"},
		{name: "unknown values stay private", subscriptionType: "internal_preview", rateLimitTier: "private_tier", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := claudeOAuthPlan(test.subscriptionType, test.rateLimitTier); got != test.want {
				t.Fatalf("claudeOAuthPlan(%q, %q) = %q, want %q", test.subscriptionType, test.rateLimitTier, got, test.want)
			}
		})
	}
}

func TestClaudeUsageReaderRejectsSymlinkedConfig(t *testing.T) {
	reader := usageTestReader(t, &usageTestKeychain{supported: true}, "")
	target := filepath.Join(reader.home, "config.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(reader.home, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.readConfig(context.Background()); err == nil {
		t.Fatal("symlinked Claude config was accepted")
	}
}

func TestClaudeUsageConfigIdentityIsAllowlisted(t *testing.T) {
	reader := usageTestReader(t, &usageTestKeychain{supported: true}, `{"oauthAccount":{"accountUuid":"u","emailAddress":" dev@example.com ","organizationRole":"admin"}}`)
	config, err := reader.readConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identity := config.identity()
	if identity == nil || identity.EmailAddress != "dev@example.com" || identity.DisplayName != "" || identity.OrganizationName != "" {
		t.Fatalf("identity = %+v", identity)
	}
	empty := &claudeUsageConfig{}
	if empty.identity() != nil {
		t.Fatal("empty oauthAccount produced an identity")
	}
}

func TestParseClaudeWeeklyPromotionIgnoresExpiredOrMalformedNotices(t *testing.T) {
	now := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	if promotion := parseClaudeWeeklyPromotion("+50% weekly limits promo through Sep 13", now); promotion != nil {
		t.Fatalf("expired promotion = %+v", promotion)
	}
	if promotion := parseClaudeWeeklyPromotion("weekly promotion available", now); promotion != nil {
		t.Fatalf("malformed promotion = %+v", promotion)
	}
}
