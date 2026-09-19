package sessionmanager

import (
	"context"

	"github.com/aoagents/agent-orchestrator/backend/internal/agyops"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

func (m *Manager) acquireAgyControllerAdmission(ctx context.Context, harness domain.AgentHarness) (func(), error) {
	if harness != domain.HarnessAgy || m.agyOperationGate == nil {
		return func() {}, nil
	}
	// Device reconciliation uses this gate exclusively. A launch joins the
	// shared side while reconciliation is actively mutating state, but a prior
	// inconclusive device read is not itself a launch failure. Native Agy
	// readiness remains the authority in that degraded case.
	return m.agyOperationGate.AcquireSharedWait(ctx)
}

func defaultAgyOperationGate(gate ports.AgyOperationGate) ports.AgyOperationGate {
	if gate != nil {
		return gate
	}
	return agyops.NewGate()
}
