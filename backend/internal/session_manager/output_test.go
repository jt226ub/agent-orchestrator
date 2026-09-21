package sessionmanager

import (
	"errors"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func TestOutputReadsTheLiveTerminalScrollback(t *testing.T) {
	m, st, rt, _ := newManager()
	st.sessions["mer-1"] = mkLive("mer-1")
	rt.outputs = []string{"tests passed\n$ "}
	got, err := m.Output(ctx, "mer-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != "tests passed\n$ " || rt.outputCalls != 1 {
		t.Fatalf("output = %q (calls %d)", got, rt.outputCalls)
	}
	if _, err := m.Output(ctx, "mer-1", MaxOutputLines+1); err != nil {
		t.Fatalf("capped read failed: %v", err)
	}
}

func TestOutputRefusesUnknownAndTerminatedSessions(t *testing.T) {
	m, st, _, _ := newManager()
	if _, err := m.Output(ctx, "missing", 10); !errors.Is(err, ports.ErrSessionNotFound) {
		t.Fatalf("unknown session error = %v", err)
	}
	seedTerminal(st, "mer-2", domain.SessionMetadata{WorkspacePath: "/ws/mer-2", RuntimeHandleID: "h2"})
	if _, err := m.Output(ctx, "mer-2", 10); !errors.Is(err, ErrSessionNotRunning) {
		t.Fatalf("terminated session error = %v", err)
	}
	st.sessions["mer-3"] = domain.SessionRecord{ID: "mer-3", ProjectID: "mer"}
	if _, err := m.Output(ctx, "mer-3", 10); !errors.Is(err, ErrSessionNotRunning) {
		t.Fatalf("handle-less session error = %v", err)
	}
}
