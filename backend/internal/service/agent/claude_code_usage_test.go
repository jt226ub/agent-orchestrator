package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters"
	agentregistry "github.com/aoagents/agent-orchestrator/backend/internal/adapters/agent/registry"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

type fakeClaudeCodeUsageReader struct {
	fakeAgent
	reads       atomic.Int32
	started     chan struct{}
	release     chan struct{}
	observation ports.ClaudeCodePlanUsageObservation
	err         error
}

func (f *fakeClaudeCodeUsageReader) ReadPlanUsage(ctx context.Context) (ports.ClaudeCodePlanUsageObservation, error) {
	f.reads.Add(1)
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ports.ClaudeCodePlanUsageObservation{}, ctx.Err()
		}
	}
	return f.observation, f.err
}

func claudeCodeUsageTestObservation(now time.Time, fiveHour, weekly float64) ports.ClaudeCodePlanUsageObservation {
	plan := "max"
	resetsAt := now.Add(2 * time.Hour)
	return ports.ClaudeCodePlanUsageObservation{
		ObservedAt: now, Plan: &plan,
		Identity: &domain.ClaudeCodePlanUsageIdentity{EmailAddress: "dev@example.com", OrganizationName: "Dev's Organization"},
		Windows: []domain.ClaudeCodePlanUsageWindow{
			{ID: "five_hour", DisplayName: "5-hour limit", UsedPercent: fiveHour, ResetsAt: &resetsAt},
			{ID: "seven_day", DisplayName: "Weekly — all models", UsedPercent: weekly},
			{ID: "seven_day_opus", DisplayName: "Weekly — Opus", UsedPercent: 99},
		},
	}
}

func newClaudeCodeUsageTestCoordinator(reader ports.ClaudeCodeUsageReader, now time.Time) (*claudeCodeUsageCoordinator, *testClock) {
	clock := newTestClock(now)
	c := newClaudeCodeUsageCoordinator(context.Background(), reader, nil)
	c.now = clock.read
	return c, clock
}

func TestClaudeCodeUsageSingleFlightAndDisplayTTL(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	reader := &fakeClaudeCodeUsageReader{started: make(chan struct{}, 1), release: make(chan struct{}), observation: claudeCodeUsageTestObservation(now, 2, 30)}
	c, _ := newClaudeCodeUsageTestCoordinator(reader, now)
	done := make(chan struct{}, 2)
	for range 2 {
		go func() {
			_, _ = c.ensure(context.Background(), false)
			done <- struct{}{}
		}()
	}
	<-reader.started
	close(reader.release)
	<-done
	<-done
	if got := reader.reads.Load(); got != 1 {
		t.Fatalf("reads = %d, want one shared read", got)
	}
	snapshot, err := c.ensure(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if reader.reads.Load() != 1 {
		t.Fatalf("fresh cache read the provider again (%d reads)", reader.reads.Load())
	}
	if snapshot.State != domain.ClaudeCodePlanUsageAvailable || snapshot.Freshness != domain.AgentReadinessFresh || snapshot.RemainingPercent == nil || *snapshot.RemainingPercent != 70 {
		t.Fatalf("snapshot = %#v, want 70%% remaining from the worst general window", snapshot)
	}
	if snapshot.Plan == nil || *snapshot.Plan != "max" || snapshot.Identity == nil || snapshot.Identity.EmailAddress != "dev@example.com" || len(snapshot.Windows) != 3 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if _, err := c.ensure(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if reader.reads.Load() != 2 {
		t.Fatalf("force did not read the provider again (%d reads)", reader.reads.Load())
	}
}

func TestClaudeCodeUsageFailureKeepsLastGoodSnapshotStaleWithBackoff(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	reader := &fakeClaudeCodeUsageReader{observation: claudeCodeUsageTestObservation(now, 100, 10)}
	c, clock := newClaudeCodeUsageTestCoordinator(reader, now)
	first, err := c.ensure(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if first.RemainingPercent == nil || *first.RemainingPercent != 0 {
		t.Fatalf("first = %#v, want exhausted 5-hour window", first)
	}
	reader.err = ports.ErrClaudeCodePlanUsageRateLimited
	later := now.Add(claudeCodeUsageDisplayTTL + time.Second)
	clock.set(later)
	second, err := c.ensure(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Freshness != domain.AgentReadinessStale || second.ReasonCode != domain.ClaudeCodePlanUsageReasonRateLimited || second.State != domain.ClaudeCodePlanUsageAvailable || len(second.Windows) != 3 {
		t.Fatalf("second = %#v, want the last good snapshot marked stale with the rate-limited reason", second)
	}
	if _, err := c.ensure(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if reader.reads.Load() != 2 {
		t.Fatalf("backoff did not defer the retry (%d reads)", reader.reads.Load())
	}
	clock.set(later.Add(defaultReadinessRetryDelays[0] + time.Second))
	if _, err := c.ensure(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if reader.reads.Load() != 3 {
		t.Fatalf("retry after backoff did not read (%d reads)", reader.reads.Load())
	}
}

func TestClaudeCodeUsageSignedOutKeepsIdentityAndPlan(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	plan := "pro"
	reader := &fakeClaudeCodeUsageReader{err: ports.ErrClaudeCodePlanUsageSignedOut, observation: ports.ClaudeCodePlanUsageObservation{Plan: &plan, Identity: &domain.ClaudeCodePlanUsageIdentity{EmailAddress: "dev@example.com"}}}
	c, _ := newClaudeCodeUsageTestCoordinator(reader, now)
	snapshot, err := c.ensure(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != domain.ClaudeCodePlanUsageUnknown || snapshot.ReasonCode != domain.ClaudeCodePlanUsageReasonSignedOut || snapshot.CheckedAt != nil || snapshot.AttemptedAt == nil {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Plan == nil || *snapshot.Plan != "pro" || snapshot.Identity == nil || snapshot.Identity.EmailAddress != "dev@example.com" || len(snapshot.Windows) != 0 {
		t.Fatalf("snapshot = %#v, want the plan and identity kept without windows", snapshot)
	}
	unsupported := &fakeClaudeCodeUsageReader{err: ports.ErrClaudeCodePlanUsageUnsupported}
	c, _ = newClaudeCodeUsageTestCoordinator(unsupported, now)
	snapshot, err = c.ensure(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != domain.ClaudeCodePlanUsageUnsupported || snapshot.ReasonCode != domain.ClaudeCodePlanUsageReasonUnsupported {
		t.Fatalf("snapshot = %#v, want unsupported", snapshot)
	}
}

func TestClaudeCodeUsageClassifiesReadFailures(t *testing.T) {
	cases := map[error]string{
		context.DeadlineExceeded:                domain.ClaudeCodePlanUsageReasonCheckTimeout,
		context.Canceled:                        domain.ClaudeCodePlanUsageReasonCheckStopped,
		ports.ErrClaudeCodePlanUsageUnsupported: domain.ClaudeCodePlanUsageReasonUnsupported,
		ports.ErrClaudeCodePlanUsageSignedOut:   domain.ClaudeCodePlanUsageReasonSignedOut,
		ports.ErrClaudeCodePlanUsageRateLimited: domain.ClaudeCodePlanUsageReasonRateLimited,
		ports.ErrClaudeCodePlanUsageInvalid:     domain.ClaudeCodePlanUsageReasonInvalidResponse,
		ports.ErrClaudeCodePlanUsageUnavailable: domain.ClaudeCodePlanUsageReasonUnavailable,
		errors.New("boom"):                      domain.ClaudeCodePlanUsageReasonUnavailable,
	}
	for err, want := range cases {
		if code, _ := classifyClaudeCodeUsageReadFailure(err); code != want {
			t.Fatalf("classify(%v) = %q, want %q", err, code, want)
		}
	}
}

func TestServiceClaudeCodeUsageUsesTheClaudeCodeAdapter(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	reader := &fakeClaudeCodeUsageReader{observation: claudeCodeUsageTestObservation(now, 10, 20)}
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		{Harness: domain.HarnessCodex, Manifest: adapters.Manifest{ID: "codex", Name: "Codex"}, Agent: fakeAgent{}},
		{Harness: domain.HarnessClaudeCode, Manifest: adapters.Manifest{ID: "claude-code", Name: "Claude Code"}, Agent: reader},
	})
	cached, err := svc.CachedClaudeCodeUsage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cached.ReasonCode != domain.ClaudeCodePlanUsageReasonNotChecked || reader.reads.Load() != 0 {
		t.Fatalf("cached = %#v (reads %d), want unchecked without contacting the provider", cached, reader.reads.Load())
	}
	ensured, err := svc.EnsureClaudeCodeUsage(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if ensured.State != domain.ClaudeCodePlanUsageAvailable || reader.reads.Load() != 1 {
		t.Fatalf("ensured = %#v (reads %d)", ensured, reader.reads.Load())
	}
	without := NewWithAgents([]agentregistry.HarnessAgent{{Harness: domain.HarnessCodex, Manifest: adapters.Manifest{ID: "codex"}, Agent: fakeAgent{}}})
	unsupported, err := without.EnsureClaudeCodeUsage(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.State != domain.ClaudeCodePlanUsageUnsupported {
		t.Fatalf("unsupported = %#v", unsupported)
	}
}
