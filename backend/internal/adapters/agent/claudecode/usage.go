package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	claudeOAuthUsageEndpoint = "https://api.anthropic.com/api/oauth/usage"
	claudeOAuthBetaHeader    = "oauth-2025-04-20"
	claudeUsageUserAgent     = "claude-cli/2.1.220"
	claudeUsageBodyLimit     = 1 << 20
	claudeConfigReadLimit    = 8 << 20
	claudeUsageHTTPTimeout   = 5 * time.Second
)

type claudeHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// claudeCodeUsageReader reads the device's signed-in Claude Code subscription
// limits the way the CLI's own /usage screen does: with the credential Claude
// Code stores in the native Keychain, against Anthropic's OAuth usage
// endpoint. The token is used for one request and never retained.
type claudeCodeUsageReader struct {
	keychain   Keychain
	account    string
	home       string
	httpClient claudeHTTPDoer
	endpoint   string
	now        func() time.Time
}

func newClaudeCodeUsageReader(keychain Keychain, home string) *claudeCodeUsageReader {
	return &claudeCodeUsageReader{
		keychain: keychain, account: KeychainAccount(), home: home,
		httpClient: &http.Client{Timeout: claudeUsageHTTPTimeout}, endpoint: claudeOAuthUsageEndpoint,
		now: time.Now,
	}
}

// ReadPlanUsage implements ports.ClaudeCodeUsageReader for the device credential.
func (p *Plugin) ReadPlanUsage(ctx context.Context) (ports.ClaudeCodePlanUsageObservation, error) {
	p.usageMu.Lock()
	reader := p.usage
	if reader == nil {
		home, err := os.UserHomeDir()
		if err != nil {
			p.usageMu.Unlock()
			return ports.ClaudeCodePlanUsageObservation{}, ports.ErrClaudeCodePlanUsageUnavailable
		}
		reader = newClaudeCodeUsageReader(NewKeychain(), home)
		p.usage = reader
	}
	p.usageMu.Unlock()
	return reader.ReadPlanUsage(ctx)
}

type claudeOAuthCredential struct {
	ClaudeAIOAuth struct {
		AccessToken      string `json:"accessToken"`
		SubscriptionType string `json:"subscriptionType"`
		RateLimitTier    string `json:"rateLimitTier"`
	} `json:"claudeAiOauth"`
}

type claudeUsageWindowWire struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    *string  `json:"resets_at"`
}

type claudeUsageScopedLimitWire struct {
	Kind     string   `json:"kind"`
	Percent  *float64 `json:"percent"`
	ResetsAt *string  `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

func (r *claudeCodeUsageReader) ReadPlanUsage(ctx context.Context) (ports.ClaudeCodePlanUsageObservation, error) {
	if r.keychain == nil || !r.keychain.Supported() {
		return ports.ClaudeCodePlanUsageObservation{}, ports.ErrClaudeCodePlanUsageUnsupported
	}
	credential, found, err := r.keychain.Get(ctx, claudeCanonicalKeychainService, r.account)
	if err != nil {
		return ports.ClaudeCodePlanUsageObservation{}, ports.ErrClaudeCodePlanUsageUnavailable
	}
	if !found {
		return ports.ClaudeCodePlanUsageObservation{}, ports.ErrClaudeCodePlanUsageSignedOut
	}
	var stored claudeOAuthCredential
	if json.Unmarshal(credential, &stored) != nil {
		return ports.ClaudeCodePlanUsageObservation{}, ports.ErrClaudeCodePlanUsageInvalid
	}
	if strings.TrimSpace(stored.ClaudeAIOAuth.AccessToken) == "" {
		// Claude keeps shared plugin and MCP fields in the item after a logout.
		return ports.ClaudeCodePlanUsageObservation{}, ports.ErrClaudeCodePlanUsageSignedOut
	}
	observation := ports.ClaudeCodePlanUsageObservation{ObservedAt: r.now().UTC()}
	if plan := claudeOAuthPlan(stored.ClaudeAIOAuth.SubscriptionType, stored.ClaudeAIOAuth.RateLimitTier); plan != "" {
		observation.Plan = &plan
	}
	if config, configErr := r.readConfig(ctx); configErr == nil && config != nil {
		observation.Identity = config.identity()
		observation.Promotion = config.promotion(r.now())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint, http.NoBody)
	if err != nil {
		return observation, ports.ErrClaudeCodePlanUsageUnavailable
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+stored.ClaudeAIOAuth.AccessToken)
	req.Header.Set("anthropic-beta", claudeOAuthBetaHeader)
	req.Header.Set("User-Agent", claudeUsageUserAgent)
	response, err := r.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return observation, ctxErr
		}
		return observation, ports.ErrClaudeCodePlanUsageUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return observation, ports.ErrClaudeCodePlanUsageRateLimited
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return observation, ports.ErrClaudeCodePlanUsageSignedOut
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return observation, ports.ErrClaudeCodePlanUsageUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, claudeUsageBodyLimit+1))
	if err != nil || len(body) > claudeUsageBodyLimit {
		return observation, ports.ErrClaudeCodePlanUsageInvalid
	}
	var wire map[string]json.RawMessage
	if json.Unmarshal(body, &wire) != nil {
		return observation, ports.ErrClaudeCodePlanUsageInvalid
	}
	windows := normalizeClaudeUsageWindows(wire)
	windows = append(windows, normalizeClaudeScopedUsageLimits(wire["limits"], windows)...)
	if len(windows) == 0 {
		return observation, ports.ErrClaudeCodePlanUsageInvalid
	}
	observation.Windows = windows
	return observation, nil
}

func normalizeClaudeSubscriptionType(value string) string {
	switch normalized := strings.ToLower(strings.TrimSpace(value)); normalized {
	case "free", "pro", "max", "team", "business", "enterprise":
		return normalized
	case "claude_free", "claude_pro", "claude_max", "claude_team", "claude_business", "claude_enterprise":
		return strings.TrimPrefix(normalized, "claude_")
	default:
		return ""
	}
}

func claudeOAuthPlan(subscriptionType, rateLimitTier string) string {
	if plan := normalizeClaudeSubscriptionType(subscriptionType); plan != "" {
		return plan
	}
	// Older Claude credentials may omit subscriptionType but retain a stable
	// provider rate-limit tier. Map only recognized values; never expose the raw
	// credential field through AO's display APIs.
	normalized := strings.ToLower(strings.TrimSpace(rateLimitTier))
	switch {
	case strings.Contains(normalized, "claude_pro"):
		return "pro"
	case strings.Contains(normalized, "claude_max"):
		return "max"
	case strings.Contains(normalized, "claude_team"):
		return "team"
	case strings.Contains(normalized, "claude_enterprise"):
		return "enterprise"
	default:
		return ""
	}
}

func normalizeClaudeScopedUsageLimits(raw json.RawMessage, existing []domain.ClaudeCodePlanUsageWindow) []domain.ClaudeCodePlanUsageWindow {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var values []claudeUsageScopedLimitWire
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	seen := make(map[string]struct{}, len(existing)+len(values))
	for _, window := range existing {
		seen[window.ID] = struct{}{}
	}
	windows := make([]domain.ClaudeCodePlanUsageWindow, 0, len(values))
	for _, value := range values {
		if value.Kind != "weekly_scoped" || value.Scope == nil || value.Scope.Model == nil || value.Percent == nil {
			continue
		}
		displayName := strings.TrimSpace(value.Scope.Model.DisplayName)
		if displayName == "" || !validClaudePercent(*value.Percent) {
			continue
		}
		id := "weekly_scoped:" + strings.ToLower(displayName)
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		window := domain.ClaudeCodePlanUsageWindow{ID: id, DisplayName: "Weekly — " + displayName, UsedPercent: *value.Percent, ResetsAt: parseClaudeResetTime(value.ResetsAt)}
		windows = append(windows, window)
	}
	return windows
}

var claudeUsageWindowOrder = []string{
	"five_hour", "seven_day", "seven_day_fable", "seven_day_opus", "seven_day_sonnet", "seven_day_oauth_apps",
}

var claudeUsageWindowNames = map[string]string{
	"five_hour":            "5-hour limit",
	"seven_day":            "Weekly — all models",
	"seven_day_fable":      "Weekly — Fable",
	"seven_day_opus":       "Weekly — Opus",
	"seven_day_sonnet":     "Weekly — Sonnet",
	"seven_day_oauth_apps": "Weekly — connected apps",
}

func normalizeClaudeUsageWindows(values map[string]json.RawMessage) []domain.ClaudeCodePlanUsageWindow {
	ordered := append([]string(nil), claudeUsageWindowOrder...)
	known := make(map[string]struct{}, len(ordered))
	for _, id := range ordered {
		known[id] = struct{}{}
	}
	additional := make([]string, 0)
	for id := range values {
		if _, ok := known[id]; !ok && strings.HasPrefix(id, "seven_day_") {
			additional = append(additional, id)
		}
	}
	sort.Strings(additional)
	ordered = append(ordered, additional...)
	windows := make([]domain.ClaudeCodePlanUsageWindow, 0, len(ordered))
	for _, id := range ordered {
		raw, ok := values[id]
		if !ok || string(raw) == "null" {
			continue
		}
		var wire claudeUsageWindowWire
		if json.Unmarshal(raw, &wire) != nil || wire.Utilization == nil || !validClaudePercent(*wire.Utilization) {
			continue
		}
		windows = append(windows, domain.ClaudeCodePlanUsageWindow{ID: id, DisplayName: claudeUsageWindowName(id), UsedPercent: *wire.Utilization, ResetsAt: parseClaudeResetTime(wire.ResetsAt)})
	}
	return windows
}

func validClaudePercent(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 100
}

func parseClaudeResetTime(value *string) *time.Time {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*value))
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func claudeUsageWindowName(id string) string {
	if name := claudeUsageWindowNames[id]; name != "" {
		return name
	}
	name := strings.TrimPrefix(id, "seven_day_")
	name = strings.ReplaceAll(name, "_", " ")
	if name == "" {
		return "Weekly limit"
	}
	return "Weekly — " + strings.ToUpper(name[:1]) + name[1:]
}

// claudeUsageConfig is the allowlisted projection of ~/.claude.json that the
// usage reader needs: who is signed in and any cached rate-limit promotion.
type claudeUsageConfig struct {
	OAuthAccount struct {
		AccountUUID      string `json:"accountUuid"`
		EmailAddress     string `json:"emailAddress"`
		DisplayName      string `json:"displayName"`
		OrganizationName string `json:"organizationName"`
	} `json:"oauthAccount"`
	CachedGrowthBookFeatures map[string]json.RawMessage `json:"cachedGrowthBookFeatures"`
}

type claudePromotionWire struct {
	Bar  string `json:"bar"`
	Text string `json:"text"`
}

var claudeWeeklyPromotionPattern = regexp.MustCompile(`(?i)\+?(\d+)%\s+weekly\s+limits?\s+promo\s+through\s+([a-z]+)\s+(\d{1,2})`)

func (r *claudeCodeUsageReader) readConfig(ctx context.Context) (*claudeUsageConfig, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(r.home, ".claude.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > claudeConfigReadLimit {
		return nil, ports.ErrClaudeCodePlanUsageInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > claudeConfigReadLimit {
		return nil, ports.ErrClaudeCodePlanUsageInvalid
	}
	var config claudeUsageConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, ports.ErrClaudeCodePlanUsageInvalid
	}
	return &config, nil
}

func (c *claudeUsageConfig) identity() *domain.ClaudeCodePlanUsageIdentity {
	identity := domain.ClaudeCodePlanUsageIdentity{
		EmailAddress:     strings.TrimSpace(c.OAuthAccount.EmailAddress),
		DisplayName:      strings.TrimSpace(c.OAuthAccount.DisplayName),
		OrganizationName: strings.TrimSpace(c.OAuthAccount.OrganizationName),
	}
	if identity == (domain.ClaudeCodePlanUsageIdentity{}) {
		return nil
	}
	return &identity
}

func (c *claudeUsageConfig) promotion(now time.Time) *domain.ClaudeCodePlanPromotion {
	raw := c.CachedGrowthBookFeatures["tengu_rate_limit_promo_notices"]
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var notices []claudePromotionWire
	if json.Unmarshal(raw, &notices) != nil {
		return nil
	}
	for _, notice := range notices {
		if notice.Bar != "seven_day" {
			continue
		}
		if promotion := parseClaudeWeeklyPromotion(notice.Text, now); promotion != nil {
			return promotion
		}
	}
	return nil
}

func parseClaudeWeeklyPromotion(text string, now time.Time) *domain.ClaudeCodePlanPromotion {
	match := claudeWeeklyPromotionPattern.FindStringSubmatch(strings.TrimSpace(text))
	if len(match) != 4 {
		return nil
	}
	percent, err := strconv.Atoi(match[1])
	if err != nil || percent <= 0 {
		return nil
	}
	var end time.Time
	for _, layout := range []string{"Jan 2 2006", "January 2 2006"} {
		end, err = time.ParseInLocation(layout, match[2]+" "+match[3]+" "+strconv.Itoa(now.Year()), time.UTC)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil
	}
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	if end.Before(today) {
		return nil
	}
	return &domain.ClaudeCodePlanPromotion{PercentIncrease: percent, EndsOn: end.Format("2006-01-02")}
}

var _ ports.ClaudeCodeUsageReader = (*Plugin)(nil)
