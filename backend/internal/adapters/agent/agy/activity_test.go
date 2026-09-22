package agy

import (
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func TestDeriveActivityState(t *testing.T) {
	tests := []struct {
		name   string
		event  string
		want   domain.ActivityState
		wantOK bool
	}{
		{"pre invocation -> active", "pre-invocation", domain.ActivityActive, true},
		{"post tool use -> active", "post-tool-use", domain.ActivityActive, true},
		{"stop without the flag -> idle", "stop", domain.ActivityIdle, true},
		{"legacy before agent ignored", "before-agent", "", false},
		{"legacy after agent ignored", "after-agent", "", false},
		{"unknown event -> no signal", "unknown", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DeriveActivityState(tt.event, []byte(`{}`))
			if got != tt.want || ok != tt.wantOK {
				t.Fatalf("DeriveActivityState(%q) = (%q, %v), want (%q, %v)",
					tt.event, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// Agy ends its loop with fullyIdle:false to let a background run_command finish,
// then resumes on its own. That Stop is not a turn end.
func TestDeriveActivityState_StopHonoursFullyIdle(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    domain.ActivityState
	}{
		{"parked behind a background command", `{"executionNum":0,"fullyIdle":false,"terminationReason":"NO_TOOL_CALL"}`, domain.ActivityActive},
		{"loop finished", `{"executionNum":0,"fullyIdle":true,"terminationReason":"NO_TOOL_CALL"}`, domain.ActivityIdle},
		{"flag absent", `{"terminationReason":"NO_TOOL_CALL"}`, domain.ActivityIdle},
		{"malformed payload", `{not json`, domain.ActivityIdle},
		{"empty payload", ``, domain.ActivityIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DeriveActivityState("stop", []byte(tc.payload))
			if !ok || got != tc.want {
				t.Fatalf("DeriveActivityState(stop, %s) = (%q, %v), want (%q, true)", tc.payload, got, ok, tc.want)
			}
		})
	}
}
