//go:build darwin

package claudecode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

const (
	claudeSecurityBinary  = "/usr/bin/security"
	claudeSecurityTimeout = 5 * time.Second
)

type claudeSecurityRunner func(context.Context, []string) ([]byte, int, error)

type macOSClaudeKeychain struct{ run claudeSecurityRunner }

// NewKeychain returns the macOS Claude Code credential store.
func NewKeychain() Keychain { return &macOSClaudeKeychain{run: runClaudeSecurity} }

func (*macOSClaudeKeychain) Supported() bool { return true }

func (s *macOSClaudeKeychain) Get(ctx context.Context, service, account string) ([]byte, bool, error) {
	out, code, err := s.run(ctx, []string{"find-generic-password", "-a", account, "-w", "-s", service})
	if err != nil {
		return nil, false, err
	}
	if code == 44 {
		return nil, false, nil
	}
	if code != 0 {
		return nil, false, fmt.Errorf("keychain read failed for Claude Code with status %d", code)
	}
	return bytes.TrimSuffix(out, []byte("\n")), true, nil
}

func runClaudeSecurity(ctx context.Context, args []string) ([]byte, int, error) {
	callCtx, cancel := context.WithTimeout(ctx, claudeSecurityTimeout)
	defer cancel()
	out, err := exec.CommandContext(callCtx, claudeSecurityBinary, args...).Output()
	if callCtx.Err() != nil {
		return nil, -1, fmt.Errorf("keychain operation timed out for Claude Code: %w", callCtx.Err())
	}
	if err == nil {
		return out, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, exitErr.ExitCode(), nil
	}
	return nil, -1, err
}
