package agy

import (
	"encoding/json"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// stopPayload is the part of Agy's Stop hook context AO reads. Agy ends its
// execution loop in two ways: for good (fullyIdle true), or to let a
// background command it started (`run_command` past its WaitMsBeforeAsync)
// finish, after which it resumes on its own with PostToolUse and a new
// invocation (fullyIdle false). Observed on Antigravity CLI 1.2.7: a
// `sleep 130` run_command produced Stop{fullyIdle:false} at once and
// Stop{fullyIdle:true} only after the command returned.
type stopPayload struct {
	FullyIdle *bool `json:"fullyIdle"`
}

// DeriveActivityState maps an Agy hook event onto AO's activity state. A Stop
// that is not fully idle is the agent parking a turn behind a background tool,
// not the end of the turn: reporting idle there tells an orchestrator the work
// is done while a test suite is still running. Payloads without the flag
// (older Agy builds) keep the historical Stop -> idle mapping.
func DeriveActivityState(event string, payload []byte) (domain.ActivityState, bool) {
	switch event {
	case "pre-invocation", "post-tool-use":
		return domain.ActivityActive, true
	case "stop":
		var stop stopPayload
		if err := json.Unmarshal(payload, &stop); err == nil && stop.FullyIdle != nil && !*stop.FullyIdle {
			return domain.ActivityActive, true
		}
		return domain.ActivityIdle, true
	default:
		return "", false
	}
}
