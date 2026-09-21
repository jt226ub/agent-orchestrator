//go:build !darwin

package claudecode

import "context"

type unsupportedClaudeKeychain struct{}

// NewKeychain returns a credential store that reports Claude Code's native
// secure storage as unsupported on this platform.
func NewKeychain() Keychain { return unsupportedClaudeKeychain{} }

func (unsupportedClaudeKeychain) Supported() bool { return false }

func (unsupportedClaudeKeychain) Get(context.Context, string, string) ([]byte, bool, error) {
	return nil, false, nil
}
