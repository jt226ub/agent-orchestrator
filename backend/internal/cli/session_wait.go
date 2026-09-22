package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"
)

const (
	// sessionWaitPollInterval is how often wait re-reads the session. Activity
	// changes through agent hooks, so a one-second poll costs little and
	// reports a finished turn promptly.
	sessionWaitPollInterval = time.Second
	// defaultSessionWaitSettle is how long a session must already have been
	// idle for wait to accept that idle at once. A worker that was just sent a
	// message reads idle for a moment before its hooks report active; waiting
	// out that gap keeps wait from returning before the turn even starts.
	defaultSessionWaitSettle  = 3 * time.Second
	defaultSessionWaitTimeout = 30 * time.Minute
)

type sessionWaitResponse struct {
	SessionID string `json:"sessionId"`
	// Outcome is the settled activity state (idle, waiting_input, blocked,
	// exited) or "terminated".
	Outcome        string     `json:"outcome"`
	Status         string     `json:"status,omitempty"`
	LastActivityAt *time.Time `json:"lastActivityAt,omitempty"`
	WaitedSeconds  float64    `json:"waitedSeconds"`
}

// errSessionWaitTimeout is returned when the session did not settle in time.
var errSessionWaitTimeout = errors.New("timed out waiting for the session")

func newSessionWaitCommand(ctx *commandContext) *cobra.Command {
	var opts sessionOptions
	var timeout, settle time.Duration
	cmd := &cobra.Command{
		Use:   "wait <id>",
		Short: "Block until a session's turn ends",
		Long: "Block until the session is no longer working: its turn ended (idle), it needs input (waiting_input or blocked), " +
			"its agent exited, or it was terminated. A session that has already been idle for longer than --settle returns at once; " +
			"one that went idle a moment ago is given that long to start the turn it was just handed.",
		Args: oneSessionIDArg,
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := normalizeSessionID(args[0])
			if err != nil {
				return err
			}
			return ctx.waitSession(cmd.Context(), cmd, id, timeout, settle, opts)
		},
	}
	f := cmd.Flags()
	addSessionProjectFlag(f, &opts.project, "Project id to scope the lookup")
	f.DurationVar(&timeout, "timeout", defaultSessionWaitTimeout, "Give up after this long (exit status 1)")
	f.DurationVar(&settle, "settle", defaultSessionWaitSettle, "How long a session must already have been idle to count as settled")
	f.BoolVar(&opts.json, "json", false, "Output as JSON")
	return cmd
}

func (c *commandContext) waitSession(ctx context.Context, cmd *cobra.Command, id string, timeout, settle time.Duration, opts sessionOptions) error {
	if timeout <= 0 {
		return usageError{fmt.Errorf("--timeout must be positive")}
	}
	if settle < 0 {
		return usageError{fmt.Errorf("--settle must not be negative")}
	}
	if opts.project != "" {
		if _, err := c.fetchScopedSession(ctx, id, opts.project); err != nil {
			return err
		}
	}
	start := c.deps.Now()
	sawActive := false
	path := "sessions/" + url.PathEscape(id)
	for {
		var res sessionResponse
		if err := c.getJSON(ctx, path, &res); err != nil {
			return err
		}
		sess := res.Session
		now := c.deps.Now()
		state := sess.Activity.State
		outcome := ""
		switch {
		case sess.IsTerminated:
			outcome = "terminated"
		case state == "active":
			sawActive = true
		case state == "idle":
			// Idle counts once the turn we watched ended, or once the session
			// has plainly been idle since before we started looking.
			if sawActive || now.Sub(sess.Activity.LastActivityAt) >= settle {
				outcome = state
			}
		case state == "waiting_input", state == "blocked", state == "exited":
			outcome = state
		}
		if outcome != "" {
			return writeSessionWait(cmd, sess, outcome, now.Sub(start), opts.json)
		}
		if now.Sub(start) >= timeout {
			return fmt.Errorf("%w %s after %s (activity %s)", errSessionWaitTimeout, id, timeout.Round(time.Second), emptyDash(state))
		}
		// Deps.Sleep keeps this loop deterministic in unit tests.
		c.deps.Sleep(sessionWaitPollInterval)
	}
}

func writeSessionWait(cmd *cobra.Command, sess sessionDTO, outcome string, waited time.Duration, asJSON bool) error {
	if asJSON {
		res := sessionWaitResponse{SessionID: sess.ID, Outcome: outcome, Status: sess.Status, WaitedSeconds: waited.Seconds()}
		if !sess.Activity.LastActivityAt.IsZero() {
			at := sess.Activity.LastActivityAt
			res.LastActivityAt = &at
		}
		return writeJSON(cmd.OutOrStdout(), res)
	}
	_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s (waited %s)\n", sess.ID, outcome, waited.Round(time.Second))
	return err
}
