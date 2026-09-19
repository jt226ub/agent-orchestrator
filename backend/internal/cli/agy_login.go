package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

// newAgyLoginCommand is an internal, trusted terminal entry point. It runs the
// Antigravity CLI's own Google sign-in while HOME points at AO's private
// pending account slot; the daemon verifies the staged token on its own.
func newAgyLoginCommand(ctx *commandContext) *cobra.Command {
	return &cobra.Command{
		Use:    "agy-login",
		Short:  "Sign a managed Antigravity account in (internal)",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return ctx.runAgyLogin(cmd.Context(), cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func (c *commandContext) runAgyLogin(ctx context.Context, in io.Reader, out, stderr io.Writer) error {
	agy, err := c.deps.LookPath("agy")
	if err != nil {
		// The installer puts agy in ~/.local/bin; a daemon whose PATH did not
		// read the shell profile still has to find it.
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			if candidate := filepath.Join(home, ".local", "bin", "agy"); fileIsExecutable(candidate) {
				agy = candidate
				err = nil
			}
		}
	}
	if err != nil {
		return fmt.Errorf("agy CLI is not installed or is not available on PATH")
	}
	style := newCodexLoginStyle(out)
	if _, err := fmt.Fprintf(out, "%s\n\n%s\n\n", style.wrap(ansiCyanBold, "Add an Antigravity account"), style.wrap(ansiDim, "Sign in with the Google account to add. This window closes once AO has saved the account.")); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, agy) //nolint:gosec // resolved from PATH or the installer's location
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
	cmd.Env = os.Environ()
	return cmd.Run()
}

func fileIsExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0
}
