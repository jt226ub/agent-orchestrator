package daemon

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

// agyAgentRelauncher is the session manager's in-place restart (exit, then
// resume): the worktree stays and the native conversation continues.
type agyAgentRelauncher interface {
	RestartAgent(ctx context.Context, id domain.SessionID) (sessionmanager.RestoreResult, error)
}

// agyRotationSessions is the session side of Antigravity account rotation.
type agyRotationSessions struct {
	store interface {
		ListAllSessions(ctx context.Context) ([]domain.SessionRecord, error)
	}
	agents   agyAgentRelauncher
	notifier interface {
		NotifyOrchestrator(ctx context.Context, orchestrator domain.SessionID, notice string)
	}
}

// liveAgyTerminal reports a live Antigravity terminal session. agy has no Chat
// driver, and an agent already recorded exited was stopped on purpose, so both
// are left alone.
func liveAgyTerminal(rec domain.SessionRecord) bool {
	return !rec.IsTerminated && rec.Harness == domain.HarnessAgy &&
		domain.NormalizeSessionMode(rec.Mode) != domain.SessionModeChat &&
		rec.Activity.State != domain.ActivityExited
}

func (s agyRotationSessions) RelaunchIdleAgySessions(ctx context.Context, only []domain.SessionID) ([]agentsvc.AgyRelaunchedSession, []domain.SessionID, error) {
	recs, err := s.store.ListAllSessions(ctx)
	if err != nil {
		return nil, nil, err
	}
	var want map[domain.SessionID]bool
	if only != nil {
		want = make(map[domain.SessionID]bool, len(only))
		for _, id := range only {
			want[id] = true
		}
	}
	var relaunched []agentsvc.AgyRelaunchedSession
	var busy []domain.SessionID
	for _, rec := range recs {
		if !liveAgyTerminal(rec) || (want != nil && !want[rec.ID]) {
			continue
		}
		// Only a worker between turns is moved: interrupting one mid-turn would
		// lose work, and one on a model group that still has quota is fine.
		if rec.Activity.State != domain.ActivityIdle && rec.Activity.State != domain.ActivityWaitingInput {
			busy = append(busy, rec.ID)
			continue
		}
		out := agentsvc.AgyRelaunchedSession{ID: rec.ID, Parent: rec.ParentSessionID, DisplayName: rec.DisplayName}
		if _, err := s.agents.RestartAgent(ctx, rec.ID); err != nil {
			out.Err = err
		}
		relaunched = append(relaunched, out)
	}
	return relaunched, busy, nil
}

func (s agyRotationSessions) AgyOrchestrators(ctx context.Context) ([]domain.SessionID, error) {
	recs, err := s.store.ListAllSessions(ctx)
	if err != nil {
		return nil, err
	}
	live := map[domain.SessionID]bool{}
	for _, rec := range recs {
		if !rec.IsTerminated && rec.Kind == domain.KindOrchestrator {
			live[rec.ID] = true
		}
	}
	seen := map[domain.SessionID]bool{}
	var out []domain.SessionID
	for _, rec := range recs {
		if liveAgyTerminal(rec) && live[rec.ParentSessionID] && !seen[rec.ParentSessionID] {
			seen[rec.ParentSessionID] = true
			out = append(out, rec.ParentSessionID)
		}
	}
	return out, nil
}

func (s agyRotationSessions) NotifyOrchestrator(ctx context.Context, orchestrator domain.SessionID, notice string) {
	s.notifier.NotifyOrchestrator(ctx, orchestrator, notice)
}

// The daemon starts the rotation only when its session manager is a relauncher;
// a drift here would switch the feature off silently.
var _ agyAgentRelauncher = (*sessionmanager.Manager)(nil)
