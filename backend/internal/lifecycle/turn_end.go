package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/sessionguard"
)

// turnEndExcerptRunes bounds the worker's last assistant update quoted in a
// turn-end notice: enough to tell a finished task from a question, without
// pasting a whole report into the orchestrator's turn.
const turnEndExcerptRunes = 600

// turnEndDeliveryTimeout bounds one delivery attempt. Delivery runs detached
// from the hook request that reported the worker's turn end: a wedged
// orchestrator controller must neither stall that hook nor hold a goroutine
// forever, and a notice that could not be written is queued, not dropped.
const turnEndDeliveryTimeout = 20 * time.Second

// TurnEndRetryInterval is how often queued notices are retried (see
// RunTurnEndRetries) in addition to the flush on the orchestrator's next idle.
const TurnEndRetryInterval = 30 * time.Second

// turnEndState holds notices that could not be delivered the moment a worker's
// turn ended because its orchestrator was mid-turn, awaiting the human, or not
// yet accepting input. They are flushed, as one message, when that
// orchestrator next goes idle.
type turnEndState struct {
	mu      sync.Mutex
	pending map[domain.SessionID][]string
	// inflight counts detached deliveries so tests (and shutdown) can wait.
	inflight sync.WaitGroup
}

// workerTurnEnded reports the one transition that means "the worker finished
// a turn": active to idle on a live worker that an orchestrator spawned. An
// idle reached from waiting_input or blocked is the human answering, not the
// worker finishing, and a worker without a durable parent has nobody to tell.
func workerTurnEnded(prev domain.ActivityState, next domain.SessionRecord) bool {
	return prev == domain.ActivityActive &&
		next.Activity.State == domain.ActivityIdle &&
		next.Kind == domain.KindWorker &&
		next.ParentSessionID != "" &&
		!next.IsTerminated
}

// turnEndNotice renders the daemon-authored message an orchestrator receives
// when one of its workers finishes a turn. Worker-controlled text is
// sanitized and bounded: it is quoted for context, never as an instruction.
func turnEndNotice(worker domain.SessionRecord, at time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[AO] Worker %s", worker.ID)
	if name := strings.TrimSpace(domain.SanitizeControlChars(worker.DisplayName)); name != "" {
		fmt.Fprintf(&b, " (%q)", name)
	}
	fmt.Fprintf(&b, " finished its turn and is idle (%s).", at.UTC().Format(time.RFC3339))
	if excerpt := boundedExcerpt(worker.Metadata.LatestAssistantUpdate, turnEndExcerptRunes); excerpt != "" {
		fmt.Fprintf(&b, "\nIts last message:\n%s", excerpt)
	}
	fmt.Fprintf(&b, "\nRead its screen with `ao session tail %s`; continue it with `ao send --session %s --message \"...\"`.", worker.ID, worker.ID)
	return b.String()
}

func boundedExcerpt(text string, limit int) string {
	text = strings.TrimSpace(domain.SanitizeControlChars(text))
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + " […]"
}

// applyTurnEndSignals runs after an activity projection commits, outside the
// manager lock: a finished worker notifies its orchestrator, and an
// orchestrator that just went idle receives whatever its workers reported
// while it was busy.
func (m *Manager) applyTurnEndSignals(ctx context.Context, prev domain.ActivityState, next domain.SessionRecord, now time.Time) {
	if m.guard == nil {
		return
	}
	if workerTurnEnded(prev, next) {
		parent, notice := next.ParentSessionID, turnEndNotice(next, now)
		m.goTurnEnd(ctx, func(ctx context.Context) { m.deliverTurnEndNotice(ctx, parent, notice) })
	}
	if next.Kind == domain.KindOrchestrator && next.Activity.State == domain.ActivityIdle && prev != domain.ActivityIdle {
		id := next.ID
		m.goTurnEnd(ctx, func(ctx context.Context) { m.flushTurnEndNotices(ctx, id) })
	}
}

// goTurnEnd runs one delivery step detached from the caller's request context
// and bounded by turnEndDeliveryTimeout.
func (m *Manager) goTurnEnd(parent context.Context, step func(context.Context)) {
	m.turnEnds.inflight.Add(1)
	go func() {
		defer m.turnEnds.inflight.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), turnEndDeliveryTimeout)
		defer cancel()
		step(ctx)
	}()
}

// waitTurnEndDeliveries blocks until every detached delivery step has
// finished. Tests use it; production never needs to wait.
func (m *Manager) waitTurnEndDeliveries() { m.turnEnds.inflight.Wait() }

// RunTurnEndRetries retries queued notices every interval until ctx ends. The
// flush on an orchestrator's next idle covers the common case; this covers an
// orchestrator that stays idle while its controller was briefly unable to
// take a message, and a delivery that timed out.
func (m *Manager) RunTurnEndRetries(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = TurnEndRetryInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.RetryTurnEndNotices(ctx)
		}
	}
}

// RetryTurnEndNotices attempts one flush for every orchestrator with queued
// notices. Each attempt is bounded by turnEndDeliveryTimeout.
func (m *Manager) RetryTurnEndNotices(ctx context.Context) {
	m.turnEnds.mu.Lock()
	targets := make([]domain.SessionID, 0, len(m.turnEnds.pending))
	for id := range m.turnEnds.pending {
		targets = append(targets, id)
	}
	m.turnEnds.mu.Unlock()
	for _, id := range targets {
		attempt, cancel := context.WithTimeout(ctx, turnEndDeliveryTimeout)
		m.flushTurnEndNotices(attempt, id)
		cancel()
	}
}

// deliverTurnEndNotice writes the notice under the coordination policy: it
// lands only when the orchestrator is idle at the write boundary (or mid-turn
// on a harness that can be steered). Anything else is queued for the
// orchestrator's next idle, except an orchestrator that is gone for good.
func (m *Manager) deliverTurnEndNotice(ctx context.Context, orchestrator domain.SessionID, notice string) {
	if m.guard == nil {
		return
	}
	outcome, err := m.guard.NudgeCoordination(ctx, orchestrator, notice, m.steerActive)
	switch turnEndOutcome(outcome, err) {
	case turnEndDelivered:
		return
	case turnEndDropped:
		slog.Default().Info("lifecycle: worker turn-end notice dropped; orchestrator unavailable", "orchestrator", orchestrator, "outcome", outcome.String())
		return
	}
	if err != nil {
		// The guard reports Sent with an error when the write itself failed
		// (a controller that refused or timed out): the notice did not land.
		slog.Default().Warn("lifecycle: worker turn-end notice failed; queued for retry", "orchestrator", orchestrator, "outcome", outcome.String(), "err", err)
	} else {
		slog.Default().Info("lifecycle: worker turn-end notice queued", "orchestrator", orchestrator, "outcome", outcome.String())
	}
	m.turnEnds.mu.Lock()
	if m.turnEnds.pending == nil {
		m.turnEnds.pending = map[domain.SessionID][]string{}
	}
	m.turnEnds.pending[orchestrator] = append(m.turnEnds.pending[orchestrator], notice)
	m.turnEnds.mu.Unlock()
}

// flushTurnEndNotices delivers an orchestrator's queued notices as one message.
// A refused flush keeps them for the next idle; a delivered one clears them.
func (m *Manager) flushTurnEndNotices(ctx context.Context, orchestrator domain.SessionID) {
	if m.guard == nil {
		return
	}
	m.turnEnds.mu.Lock()
	queued := m.turnEnds.pending[orchestrator]
	m.turnEnds.mu.Unlock()
	if len(queued) == 0 {
		return
	}
	outcome, err := m.guard.NudgeCoordination(ctx, orchestrator, strings.Join(queued, "\n\n"), m.steerActive)
	if err != nil {
		slog.Default().Warn("lifecycle: queued worker turn-end notices failed; kept for retry", "orchestrator", orchestrator, "count", len(queued), "err", err)
	}
	switch turnEndOutcome(outcome, err) {
	case turnEndDelivered, turnEndDropped:
		m.turnEnds.mu.Lock()
		// Drop only what was flushed; a notice queued meanwhile stays.
		remaining := m.turnEnds.pending[orchestrator][len(queued):]
		if len(remaining) == 0 {
			delete(m.turnEnds.pending, orchestrator)
		} else {
			m.turnEnds.pending[orchestrator] = append([]string(nil), remaining...)
		}
		m.turnEnds.mu.Unlock()
	}
}

type turnEndResult int

const (
	// turnEndRetry means the notice did not land but the orchestrator may take
	// it later: busy, awaiting the human, not yet accepting input, or a write
	// that failed or timed out.
	turnEndRetry turnEndResult = iota
	turnEndDelivered
	turnEndDropped
)

// turnEndOutcome classifies a guard outcome. Sent counts only without an
// error: the guard returns Sent alongside the error when the messenger write
// itself failed, and that notice has not reached anyone.
func turnEndOutcome(outcome sessionguard.Outcome, err error) turnEndResult {
	switch outcome {
	case sessionguard.Sent:
		if err == nil {
			return turnEndDelivered
		}
		return turnEndRetry
	case sessionguard.SuppressedNotFound, sessionguard.SuppressedTerminated, sessionguard.SuppressedExited:
		return turnEndDropped
	default:
		return turnEndRetry
	}
}
