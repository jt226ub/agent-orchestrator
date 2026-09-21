package daemon

import (
	"os"
	"testing"
)

func TestScrubInheritedSessionMarkersDropsTheClaudeChildMarker(t *testing.T) {
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "1")
	t.Setenv("CLAUDE_CONFIG_DIR", "/keep/me")
	scrubInheritedSessionMarkers()
	if value, ok := os.LookupEnv("CLAUDE_CODE_CHILD_SESSION"); ok {
		t.Fatalf("marker survived the scrub: %q", value)
	}
	if got := os.Getenv("CLAUDE_CONFIG_DIR"); got != "/keep/me" {
		t.Fatalf("unrelated variable changed: %q", got)
	}
	scrubInheritedSessionMarkers() // idempotent when the marker is absent
}
