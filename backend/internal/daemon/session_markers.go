package daemon

import "os"

// inheritedSessionMarkers are environment variables a parent agent process
// sets on its children to mark them as helpers. The daemon is often started
// from such a process (an `ao` invoked inside a Claude Code session, or a dev
// launch of the desktop app from one), and every session it spawns inherits
// the daemon's environment through the terminal runtime. Left in place, the
// marker makes Claude Code skip transcript saving for AO's own sessions,
// which breaks restore and resume. The sessions AO spawns are the user's
// sessions, never helpers, so the marker is dropped once at boot.
var inheritedSessionMarkers = []string{"CLAUDE_CODE_CHILD_SESSION"}

// scrubInheritedSessionMarkers removes the parent-process markers from the
// daemon's environment so spawned sessions never see them.
func scrubInheritedSessionMarkers() {
	for _, name := range inheritedSessionMarkers {
		_ = os.Unsetenv(name)
	}
}
