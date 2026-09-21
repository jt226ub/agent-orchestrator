package claudecode

import (
	"context"
	"os"
	"os/user"
	"strconv"
	"strings"
)

// claudeCanonicalKeychainService is Claude Code's device-global credential
// service name in the macOS Keychain.
const claudeCanonicalKeychainService = "Claude Code-credentials"

// Keychain reads opaque Claude credential JSON. Implementations never place
// values in process arguments or in returned error text.
type Keychain interface {
	Supported() bool
	Get(ctx context.Context, service, account string) ([]byte, bool, error)
}

// KeychainAccount returns the OS account name Claude Code uses for Keychain entries.
func KeychainAccount() string {
	return claudeKeychainAccount(os.Getenv("USER"), func() (string, error) {
		current, err := user.LookupId(strconv.Itoa(os.Geteuid()))
		if err != nil {
			return "", err
		}
		return current.Username, nil
	})
}

func claudeKeychainAccount(envUser string, lookup func() (string, error)) string {
	if value := strings.TrimSpace(envUser); value != "" {
		return value
	}
	if lookup != nil {
		if value, err := lookup(); err == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "claude-code-user"
}
