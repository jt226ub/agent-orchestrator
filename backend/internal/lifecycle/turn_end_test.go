package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func turnEndFixture(orchestratorState domain.ActivityState) (*Manager, *fakeStore, *fakeMessenger) {
	m, st, msg := newManager()
	orch := domain.SessionRecord{
		ID: "mer-0", ProjectID: "mer", Kind: domain.KindOrchestrator,
		Activity:      domain.Activity{State: orchestratorState, LastActivityAt: time.Now()},
		FirstSignalAt: time.Now().Add(-time.Minute),
	}
	st.sessions["mer-0"] = orch
	worker := working("mer-1")
	worker.Kind = domain.KindWorker
	worker.ParentSessionID = "mer-0"
	worker.DisplayName = "P2.5 animation"
	worker.Metadata.RuntimeLaunchID = "launch-1"
	worker.Metadata.LatestAssistantUpdate = "All 129 tests pass.\x1b[2J Committed 55aaf8e on ao/mer-1/root."
	st.sessions["mer-1"] = worker
	return m, st, msg
}

func endTurn(t *testing.T, m *Manager, id domain.SessionID) {
	t.Helper()
	if err := m.ApplyActivitySignal(ctx, id, ports.ActivitySignal{
		Valid: true, State: domain.ActivityIdle, Event: "stop", LaunchID: "launch-1", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	// Delivery is detached from the hook request; settle it before asserting.
	m.waitTurnEndDeliveries()
}

func pendingNotices(m *Manager, orchestrator domain.SessionID) int {
	m.turnEnds.mu.Lock()
	defer m.turnEnds.mu.Unlock()
	return len(m.turnEnds.pending[orchestrator])
}

func TestTurnEnd_NotifiesIdleOrchestrator(t *testing.T) {
	m, _, msg := turnEndFixture(domain.ActivityIdle)

	endTurn(t, m, "mer-1")

	if len(msg.msgs) != 1 || msg.ids[0] != "mer-0" {
		t.Fatalf("messages = %q to %v, want one notice to mer-0", msg.msgs, msg.ids)
	}
	got := msg.msgs[0]
	for _, want := range []string{
		`[AO] Worker mer-1 ("P2.5 animation") finished its turn and is idle`,
		"Its last message:\nAll 129 tests pass.[2J Committed 55aaf8e",
		"ao session tail mer-1",
		"ao send --session mer-1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Fatalf("notice carries an escape sequence from the worker:\n%q", got)
	}
}

func TestTurnEnd_QueuesWhileOrchestratorBusyAndFlushesOnceOnIdle(t *testing.T) {
	m, st, msg := turnEndFixture(domain.ActivityActive)
	second := st.sessions["mer-1"]
	second.ID = "mer-2"
	second.DisplayName = "reviewer"
	st.sessions["mer-2"] = second

	endTurn(t, m, "mer-1")
	endTurn(t, m, "mer-2")
	if len(msg.msgs) != 0 {
		t.Fatalf("busy orchestrator received %q, want nothing until idle", msg.msgs)
	}

	// The orchestrator's own turn ends: both notices arrive as one message.
	orch := st.sessions["mer-0"]
	orch.Metadata.RuntimeLaunchID = "launch-1"
	st.sessions["mer-0"] = orch
	endTurn(t, m, "mer-0")
	if len(msg.msgs) != 1 || msg.ids[0] != "mer-0" {
		t.Fatalf("messages = %q to %v, want one combined flush to mer-0", msg.msgs, msg.ids)
	}
	if !strings.Contains(msg.msgs[0], "Worker mer-1") || !strings.Contains(msg.msgs[0], "Worker mer-2") {
		t.Fatalf("flush lacks a worker:\n%s", msg.msgs[0])
	}

	// A later idle does not resend.
	orch = st.sessions["mer-0"]
	orch.Activity.State = domain.ActivityActive
	st.sessions["mer-0"] = orch
	endTurn(t, m, "mer-0")
	if len(msg.msgs) != 1 {
		t.Fatalf("second idle resent notices: %q", msg.msgs)
	}
}

func TestTurnEnd_IgnoresNonTurnEndsAndOrphans(t *testing.T) {
	m, st, msg := turnEndFixture(domain.ActivityIdle)

	// waiting_input -> idle is the human answering, not a finished turn.
	w := st.sessions["mer-1"]
	w.Activity.State = domain.ActivityWaitingInput
	st.sessions["mer-1"] = w
	endTurn(t, m, "mer-1")

	// No parent: nobody to tell.
	w = st.sessions["mer-1"]
	w.Activity.State = domain.ActivityActive
	w.ParentSessionID = ""
	st.sessions["mer-1"] = w
	endTurn(t, m, "mer-1")

	// An orchestrator finishing its own turn is not a worker report.
	orch := st.sessions["mer-0"]
	orch.Activity.State = domain.ActivityActive
	orch.Metadata.RuntimeLaunchID = "launch-1"
	orch.ParentSessionID = "mer-9"
	st.sessions["mer-0"] = orch
	endTurn(t, m, "mer-0")

	if len(msg.msgs) != 0 {
		t.Fatalf("unexpected notices: %q", msg.msgs)
	}
}

func TestTurnEnd_DropsNoticeForTerminatedOrchestrator(t *testing.T) {
	m, st, msg := turnEndFixture(domain.ActivityIdle)
	orch := st.sessions["mer-0"]
	orch.IsTerminated = true
	st.sessions["mer-0"] = orch

	endTurn(t, m, "mer-1")
	if len(msg.msgs) != 0 {
		t.Fatalf("terminated orchestrator received %q", msg.msgs)
	}
	if queued := pendingNotices(m, "mer-0"); queued != 0 {
		t.Fatalf("notice queued for a terminated orchestrator: %d", queued)
	}
}

// A write the controller refuses (a wedged chat controller, a timeout) comes
// back from the guard as Sent plus an error. The notice has not landed: it is
// queued and delivered by the periodic retry once the write succeeds.
func TestTurnEnd_QueuesFailedWritesAndRetriesThem(t *testing.T) {
	m, _, msg := turnEndFixture(domain.ActivityIdle)
	msg.err = errors.New("chat controller: send timed out")

	endTurn(t, m, "mer-1")
	if len(msg.msgs) != 0 || pendingNotices(m, "mer-0") != 1 {
		t.Fatalf("failed write: delivered=%d queued=%d, want 0 delivered and 1 queued", len(msg.msgs), pendingNotices(m, "mer-0"))
	}

	// Still failing: the retry keeps it.
	m.RetryTurnEndNotices(ctx)
	if pendingNotices(m, "mer-0") != 1 {
		t.Fatalf("retry against a failing controller dropped the notice")
	}

	msg.err = nil
	m.RetryTurnEndNotices(ctx)
	if len(msg.msgs) != 1 || msg.ids[0] != "mer-0" || !strings.Contains(msg.msgs[0], "Worker mer-1") {
		t.Fatalf("retry delivered %q to %v, want the queued notice to mer-0", msg.msgs, msg.ids)
	}
	if pendingNotices(m, "mer-0") != 0 {
		t.Fatalf("delivered notice still queued")
	}
	m.RetryTurnEndNotices(ctx)
	if len(msg.msgs) != 1 {
		t.Fatalf("retry resent a delivered notice: %q", msg.msgs)
	}
}

// The hook request that reports the turn end may already be cancelled by the
// time delivery runs; delivery must not inherit that cancellation.
func TestTurnEnd_DeliveryOutlivesTheHookRequest(t *testing.T) {
	m, _, msg := turnEndFixture(domain.ActivityIdle)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := m.ApplyActivitySignal(cancelled, "mer-1", ports.ActivitySignal{
		Valid: true, State: domain.ActivityIdle, Event: "stop", LaunchID: "launch-1", Timestamp: time.Now(),
	}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	m.waitTurnEndDeliveries()
	if len(msg.msgs) != 1 {
		t.Fatalf("delivery under a cancelled hook context produced %q, want one notice", msg.msgs)
	}
}
