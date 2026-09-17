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

type fakeAgyCapacityReader struct {
	fakeAgent
	reads       atomic.Int32
	started     chan struct{}
	release     chan struct{}
	observation ports.AgyCapacityObservation
	err         error
}

func (f *fakeAgyCapacityReader) ReadAgyCapacity(ctx context.Context) (ports.AgyCapacityObservation, error) {
	f.reads.Add(1)
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ports.AgyCapacityObservation{}, ctx.Err()
		}
	}
	return f.observation, f.err
}

func agyCapacityTestObservation(now time.Time, used float64) ports.AgyCapacityObservation {
	minutes := int64(300)
	name := "Gemini Models"
	resetsAt := now.Add(2 * time.Hour)
	return ports.AgyCapacityObservation{
		ObservedAt:        now,
		Overall:           &domain.AgyCapacityBucket{LimitID: "gemini", DisplayName: &name, Primary: &domain.AgyCapacityWindow{UsedPercent: used, WindowDurationMinutes: &minutes, ResetsAt: &resetsAt}},
		AdditionalBuckets: []domain.AgyCapacityBucket{{LimitID: "3p", Primary: &domain.AgyCapacityWindow{UsedPercent: 0, WindowDurationMinutes: &minutes}}},
	}
}

// testClock is safe to advance from the test while the coordinator's read
// goroutine still stamps its completion log.
type testClock struct{ now atomic.Pointer[time.Time] }

func newTestClock(now time.Time) *testClock {
	clock := &testClock{}
	clock.set(now)
	return clock
}

func (c *testClock) set(now time.Time) { c.now.Store(&now) }
func (c *testClock) read() time.Time   { return *c.now.Load() }

func newAgyCapacityTestCoordinator(reader ports.AgyCapacityReader, now time.Time) (*agyCapacityCoordinator, *testClock) {
	clock := newTestClock(now)
	c := newAgyCapacityCoordinator(context.Background(), reader, nil)
	c.now = clock.read
	return c, clock
}

func TestAgyCapacitySingleFlightAndDisplayTTL(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	reader := &fakeAgyCapacityReader{started: make(chan struct{}, 1), release: make(chan struct{}), observation: agyCapacityTestObservation(now, 2)}
	c, _ := newAgyCapacityTestCoordinator(reader, now)
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
		t.Fatalf("fresh cache read the CLI again (%d reads)", reader.reads.Load())
	}
	if snapshot.State != domain.AgyCapacityAvailable || snapshot.Freshness != domain.AgentReadinessFresh || snapshot.RemainingPercent == nil || *snapshot.RemainingPercent != 98 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.Overall == nil || len(snapshot.AdditionalBuckets) != 1 || snapshot.ResetsAt == nil {
		t.Fatalf("snapshot buckets = %#v", snapshot)
	}
	if _, err := c.ensure(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if reader.reads.Load() != 2 {
		t.Fatalf("force did not read the CLI again (%d reads)", reader.reads.Load())
	}
}

func TestAgyCapacityRequestCancellationDoesNotCancelSharedRead(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	reader := &fakeAgyCapacityReader{started: make(chan struct{}, 1), release: make(chan struct{}), observation: agyCapacityTestObservation(now, 80)}
	c, _ := newAgyCapacityTestCoordinator(reader, now)
	waitCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.ensure(waitCtx, false)
		done <- err
	}()
	<-reader.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context cancellation", err)
	}
	close(reader.release)
	snapshot, err := c.ensure(context.Background(), false)
	if err != nil {
		t.Fatalf("join shared read: %v", err)
	}
	if snapshot.State != domain.AgyCapacityNearLimit || snapshot.ReasonCode != domain.AgyCapacityReasonNearLimit {
		t.Fatalf("snapshot = %#v, want near limit at 80%% used", snapshot)
	}
}

func TestAgyCapacityFailureKeepsLastGoodSnapshotStaleWithBackoff(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	reader := &fakeAgyCapacityReader{observation: agyCapacityTestObservation(now, 100)}
	c, clock := newAgyCapacityTestCoordinator(reader, now)
	first, err := c.ensure(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != domain.AgyCapacityExhausted {
		t.Fatalf("first = %#v, want exhausted at 100%% used", first)
	}
	reader.err = ports.ErrAgyCapacitySignedOut
	later := now.Add(agyCapacityDisplayTTL + time.Second)
	clock.set(later)
	second, err := c.ensure(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Freshness != domain.AgentReadinessStale || second.ReasonCode != domain.AgyCapacityReasonSkippedSignedOut || second.State != domain.AgyCapacityExhausted || second.CheckedAt == nil {
		t.Fatalf("second = %#v, want the last good snapshot marked stale with the signed-out reason", second)
	}
	if reader.reads.Load() != 2 {
		t.Fatalf("reads = %d", reader.reads.Load())
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

func TestAgyCapacityClassifiesReadFailures(t *testing.T) {
	cases := map[error]string{
		context.DeadlineExceeded:            domain.AgyCapacityReasonCheckTimeout,
		context.Canceled:                    domain.AgyCapacityReasonCheckStopped,
		ports.ErrAgentBinaryNotFound:        domain.AgyCapacityReasonNotInstalled,
		ports.ErrAgyCapacitySignedOut:       domain.AgyCapacityReasonSkippedSignedOut,
		ports.ErrAgyCapacityRequestRejected: domain.AgyCapacityReasonProviderRejected,
		errors.New("boom"):                  domain.AgyCapacityReasonCheckFailed,
	}
	for err, want := range cases {
		if code, _ := classifyAgyCapacityReadFailure(err); code != want {
			t.Fatalf("classify(%v) = %q, want %q", err, code, want)
		}
	}
}

func TestAgyCapacityNotInstalledKeepsUncheckedSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	reader := &fakeAgyCapacityReader{err: ports.ErrAgentBinaryNotFound}
	c, _ := newAgyCapacityTestCoordinator(reader, now)
	snapshot, err := c.ensure(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != domain.AgyCapacityUnknown || snapshot.ReasonCode != domain.AgyCapacityReasonNotInstalled || snapshot.CheckedAt != nil || snapshot.AttemptedAt == nil {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestServiceAgyCapacityUsesTheAgyAdapter(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	reader := &fakeAgyCapacityReader{observation: agyCapacityTestObservation(now, 10)}
	svc := NewWithAgents([]agentregistry.HarnessAgent{
		{Harness: domain.HarnessCodex, Manifest: adapters.Manifest{ID: "codex", Name: "Codex"}, Agent: fakeAgent{}},
		{Harness: domain.HarnessAgy, Manifest: adapters.Manifest{ID: "agy", Name: "Agy"}, Agent: reader},
	})
	cached, err := svc.CachedAgyCapacity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cached.ReasonCode != domain.AgyCapacityReasonNotChecked || reader.reads.Load() != 0 {
		t.Fatalf("cached = %#v (reads %d), want unchecked without contacting the CLI", cached, reader.reads.Load())
	}
	ensured, err := svc.EnsureAgyCapacity(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if ensured.State != domain.AgyCapacityAvailable || reader.reads.Load() != 1 {
		t.Fatalf("ensured = %#v (reads %d)", ensured, reader.reads.Load())
	}
	without := NewWithAgents([]agentregistry.HarnessAgent{{Harness: domain.HarnessCodex, Manifest: adapters.Manifest{ID: "codex"}, Agent: fakeAgent{}}})
	unsupported, err := without.EnsureAgyCapacity(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if unsupported.State != domain.AgyCapacityUnsupported {
		t.Fatalf("unsupported = %#v", unsupported)
	}
}
