package omp

import (
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// stopPayload is the part of the managed extension's Stop callback AO reads:
// the stopReason of the run's final assistant message.
type stopPayload struct {
	StopReason string `json:"stop_reason"`
}

// DeriveActivityState maps callbacks from AO's managed OMP extension onto the
// durable activity states used by session status derivation.
//
// A run whose final assistant message stopped on "error" or "aborted" did not
// finish the work: OMP parks it behind its "Retry" prompt and waits for a
// person. Observed on OMP 18.2.8: an edit whose match was not unique aborted
// its streaming preview and the worker sat at "F5 to Retry" for minutes while
// AO reported it idle and told its orchestrator it had finished its turn. That
// is waiting_input. A payload without the field -- an extension written by an
// older AO -- keeps the historical Stop -> idle mapping.
func DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool) {
	switch event {
	case "session-start":
		return domain.ActivityIdle, true
	case "stop":
		var stop stopPayload
		if err := json.Unmarshal(payload, &stop); err == nil &&
			(stop.StopReason == "error" || stop.StopReason == "aborted") {
			return domain.ActivityWaitingInput, true
		}
		return domain.ActivityIdle, true
	case "user-prompt-submit", "permission-resolved":
		return domain.ActivityActive, true
	case "permission-request":
		return domain.ActivityWaitingInput, true
	case "session-end":
		return domain.ActivityExited, true
	default:
		return "", false
	}
}
