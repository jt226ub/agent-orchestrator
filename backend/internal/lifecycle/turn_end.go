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

// turnEndState holds notices that could not be delivered the moment a worker's
// turn ended because its orchestrator was mid-turn, awaiting the human, or not
// yet accepting input. They are flushed, as one message, when that
// orchestrator next goes idle.
type turnEndState struct {
	mu      sync.Mutex
	pending map[domain.SessionID][]string
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
	if workerTurnEnded(prev, next) {
		m.deliverTurnEndNotice(ctx, next.ParentSessionID, turnEndNotice(next, now))
	}
	if next.Kind == domain.KindOrchestrator && next.Activity.State == domain.ActivityIdle && prev != domain.ActivityIdle {
		m.flushTurnEndNotices(ctx, next.ID)
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
	if err != nil {
		slog.Default().Warn("lifecycle: worker turn-end notice failed", "orchestrator", orchestrator, "outcome", outcome.String(), "err", err)
	}
	switch outcome {
	case sessionguard.Sent:
		return
	case sessionguard.SuppressedNotFound, sessionguard.SuppressedTerminated, sessionguard.SuppressedExited:
		slog.Default().Info("lifecycle: worker turn-end notice dropped; orchestrator unavailable", "orchestrator", orchestrator, "outcome", outcome.String())
		return
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
		slog.Default().Warn("lifecycle: queued worker turn-end notices failed", "orchestrator", orchestrator, "count", len(queued), "err", err)
	}
	switch outcome {
	case sessionguard.Sent, sessionguard.SuppressedNotFound, sessionguard.SuppressedTerminated, sessionguard.SuppressedExited:
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
