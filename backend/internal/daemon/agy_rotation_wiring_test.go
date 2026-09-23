package daemon

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

type rotationStoreFake []domain.SessionRecord

func (f rotationStoreFake) ListAllSessions(context.Context) ([]domain.SessionRecord, error) {
	return f, nil
}

type relauncherFake struct {
	calls      []string
	restartErr map[domain.SessionID]error
}

func (f *relauncherFake) RestartAgent(_ context.Context, id domain.SessionID) (sessionmanager.RestoreResult, error) {
	f.calls = append(f.calls, "restart:"+string(id))
	return sessionmanager.RestoreResult{}, f.restartErr[id]
}

func rotationRec(id string, harness domain.AgentHarness, state domain.ActivityState, parent string) domain.SessionRecord {
	return domain.SessionRecord{
		ID: domain.SessionID(id), Harness: harness, Kind: domain.KindWorker, Mode: domain.SessionModeTUI,
		Activity: domain.Activity{State: state}, ParentSessionID: domain.SessionID(parent),
	}
}

// Only live Antigravity terminal workers between turns are relaunched; a busy
// one is reported for later, and everything else -- other harnesses, chat,
// already-exited or terminated sessions -- is left alone.
func TestAgyRotationSessionsRelaunchesOnlyIdleAgyTerminals(t *testing.T) {
	terminated := rotationRec("gone", domain.HarnessAgy, domain.ActivityIdle, "orch")
	terminated.IsTerminated = true
	chat := rotationRec("chat", domain.HarnessAgy, domain.ActivityIdle, "orch")
	chat.Mode = domain.SessionModeChat
	orchestrator := domain.SessionRecord{ID: "orch", Kind: domain.KindOrchestrator, Harness: domain.HarnessClaudeCode}
	store := rotationStoreFake{
		orchestrator,
		rotationRec("idle", domain.HarnessAgy, domain.ActivityIdle, "orch"),
		rotationRec("waiting", domain.HarnessAgy, domain.ActivityWaitingInput, "orch"),
		rotationRec("busy", domain.HarnessAgy, domain.ActivityActive, "orch"),
		rotationRec("omp", domain.HarnessOMP, domain.ActivityIdle, "orch"),
		rotationRec("exited", domain.HarnessAgy, domain.ActivityExited, "orch"),
		terminated, chat,
	}
	agents := &relauncherFake{restartErr: map[domain.SessionID]error{"waiting": errors.New("restart failed")}}
	s := agyRotationSessions{store: store, agents: agents}

	relaunched, busy, err := s.RelaunchIdleAgySessions(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"restart:idle", "restart:waiting"}; !slices.Equal(agents.calls, want) {
		t.Fatalf("calls = %v, want %v", agents.calls, want)
	}
	if !slices.Equal(busy, []domain.SessionID{"busy"}) {
		t.Fatalf("busy = %v, want [busy]", busy)
	}
	if len(relaunched) != 2 || relaunched[0].Err != nil || relaunched[1].Err == nil || relaunched[0].Parent != "orch" {
		t.Fatalf("relaunched = %+v", relaunched)
	}

	agents.calls = nil
	if _, _, err := s.RelaunchIdleAgySessions(context.Background(), []domain.SessionID{"idle"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"restart:idle"}; !slices.Equal(agents.calls, want) {
		t.Fatalf("restricted calls = %v, want %v", agents.calls, want)
	}

	orchestrators, err := s.AgyOrchestrators(context.Background())
	if err != nil || !slices.Equal(orchestrators, []domain.SessionID{"orch"}) {
		t.Fatalf("orchestrators = %v, %v; want [orch] once", orchestrators, err)
	}
}
