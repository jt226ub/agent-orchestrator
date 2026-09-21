package sessionmanager

import (
	"context"
	"errors"
	"fmt"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

const (
	// DefaultOutputLines is the terminal scrollback Output returns when the
	// caller names no count.
	DefaultOutputLines = 80
	// MaxOutputLines bounds one Output read so a caller cannot ask the runtime
	// for an unbounded scrollback.
	MaxOutputLines = 2000
)

// ErrSessionNotRunning reports an Output read on a session that has no live
// terminal: terminated, or never given a runtime handle.
var ErrSessionNotRunning = errors.New("session has no running terminal")

// Output returns the last lines of a session's terminal, the same scrollback
// the desktop terminal shows, so an orchestrator can read what a worker
// printed rather than infer it from activity alone. lines outside
// [1, MaxOutputLines] falls back to DefaultOutputLines or is capped.
func (m *Manager) Output(ctx context.Context, id domain.SessionID, lines int) (string, error) {
	rec, ok, err := m.store.GetSession(ctx, id)
	if err != nil {
		return "", fmt.Errorf("output %s: %w", id, err)
	}
	if !ok {
		return "", ports.ErrSessionNotFound
	}
	handle := runtimeHandle(rec.Metadata)
	if rec.IsTerminated || handle.ID == "" {
		return "", ErrSessionNotRunning
	}
	if lines <= 0 {
		lines = DefaultOutputLines
	}
	if lines > MaxOutputLines {
		lines = MaxOutputLines
	}
	output, err := m.runtime.GetOutput(ctx, handle, lines)
	if err != nil {
		return "", fmt.Errorf("output %s: %w", id, err)
	}
	return output, nil
}
