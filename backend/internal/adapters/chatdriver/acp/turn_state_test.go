package acp

import (
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// A turn that ends because it reached a budget ended normally: the assistant
// answered up to a limit that resets on the next turn. Recording those as failed
// held every message queued behind them, because a failed turn deliberately does
// not release the queue (issue #4861) -- so an `ao send` to a busy Chat worker
// was accepted, durably queued, and then never dispatched.
//
// A refusal stays failed: unlike a budget, whatever produced it is likely to
// produce it again for the next queued message, which is the cascade #4861 holds
// the queue to avoid.
func TestTurnStateFromStopReason(t *testing.T) {
	for _, tt := range []struct {
		reason acpsdk.StopReason
		want   domain.TurnState
	}{
		{acpsdk.StopReasonEndTurn, domain.TurnStateCompleted},
		{acpsdk.StopReasonMaxTokens, domain.TurnStateCompleted},
		{acpsdk.StopReasonMaxTurnRequests, domain.TurnStateCompleted},
		{acpsdk.StopReasonCancelled, domain.TurnStateInterrupted},
		{acpsdk.StopReasonRefusal, domain.TurnStateFailed},
		{acpsdk.StopReason("something_new"), domain.TurnStateFailed},
	} {
		t.Run(string(tt.reason), func(t *testing.T) {
			if got := turnState(tt.reason); got != tt.want {
				t.Fatalf("turnState(%q) = %q, want %q", tt.reason, got, tt.want)
			}
		})
	}
}
