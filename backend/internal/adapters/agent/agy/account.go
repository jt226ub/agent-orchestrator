package agy

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// accountEnvKeysUnset are removed from the CLI's environment for every
// account read: a configured key or standalone token would make the CLI bill
// that instead of the signed-in plan, which is what the account represents.
var accountEnvKeysUnset = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY", "JETSKI_OAUTH_TOKEN"}

// AccountFactory opens account clients for AO-owned Antigravity credential
// homes. The CLI keys its config directory off HOME, so a managed home is
// passed as HOME to the read process and nothing else; the device-global home
// is the user's real one and is read with the daemon's own environment.
type AccountFactory struct {
	resolve func(context.Context) (string, error)
	logger  *slog.Logger
}

// NewAccountFactoryWithResolver builds an account factory around the binary
// resolver the harness adapter already uses.
func NewAccountFactoryWithResolver(resolve func(context.Context) (string, error), logger *slog.Logger) *AccountFactory {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &AccountFactory{resolve: resolve, logger: logger}
}

var _ ports.AgyAccountClientFactory = (*AccountFactory)(nil)

// Open validates a managed home the way the Codex factory does (an absolute,
// non-symlinked, 0700 directory) and returns a client bound to it.
func (f *AccountFactory) Open(ctx context.Context, account ports.AgyAccountContext) (ports.AgyAccountClient, error) {
	if !filepath.IsAbs(account.Home) {
		return nil, errors.New("agy account home must be absolute")
	}
	if account.Managed {
		info, err := os.Lstat(account.Home)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
			return nil, errors.New("agy account home must be a private directory")
		}
	}
	binary, err := f.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return &accountClient{binary: binary, account: account}, nil
}

// Capabilities reports what the installed CLI can do for the Accounts surface.
// There is no schema to probe: the sign-in is the CLI itself, the identity is
// read from the credential file, and the quota command is the capacity read.
// GlobalSwitch is decided by the service from the device credential store.
func (f *AccountFactory) Capabilities(ctx context.Context) domain.AgyAccountCapabilities {
	supported := func(reason string) domain.AgyCapabilityObservation {
		return domain.AgyCapabilityObservation{State: domain.AgyCapabilitySupported, ReasonCode: domain.AgyCapabilityReasonSupported, Reason: reason}
	}
	unknown := domain.AgyCapabilityObservation{State: domain.AgyCapabilityUnknown, ReasonCode: domain.AgyCapabilityReasonUnknown, Reason: "Antigravity capability detection has not completed."}
	if _, err := f.resolve(ctx); err != nil {
		unsupported := domain.AgyCapabilityObservation{State: domain.AgyCapabilityUnsupported, ReasonCode: domain.AgyCapabilityReasonUnsupported, Reason: "The Antigravity CLI is not installed."}
		return domain.AgyAccountCapabilities{AccountRead: unsupported, NativeLogin: unsupported, CapacityRead: unsupported, GlobalSwitch: unknown}
	}
	return domain.AgyAccountCapabilities{
		AccountRead:  supported("AO reads the saved credential's identity locally."),
		NativeLogin:  supported("The Antigravity CLI signs in with a Google account in a terminal."),
		CapacityRead: supported("The Antigravity CLI reports plan capacity with its usage command."),
		GlobalSwitch: unknown,
	}
}

type accountClient struct {
	binary  string
	account ports.AgyAccountContext
}

// Read runs the quota command against the account's home: the one call that
// proves the plan still accepts the credential. Identity metadata is not part
// of the CLI's answer; the service derives it from the credential file.
func (c *accountClient) Read(ctx context.Context) (ports.AgyAccountObservation, error) {
	_, err := runAgyUsage(ctx, c.binary, c.homeEnv())
	switch {
	case err == nil:
		return ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationAuthorized, Method: domain.AgyAuthMethodGoogle}, nil
	case errors.Is(err, ports.ErrAgyCapacitySignedOut):
		return ports.AgyAccountObservation{Authentication: domain.AgentAuthenticationUnauthorized, Method: domain.AgyAuthMethodGoogle}, nil
	case ctx.Err() != nil:
		return ports.AgyAccountObservation{}, ctx.Err()
	default:
		return ports.AgyAccountObservation{}, ports.ErrAgyCapacityProviderUnavailable
	}
}

// ReadCapacity returns the plan limits the CLI reports for this account. A
// signed-out answer is the provider rejecting the credential.
func (c *accountClient) ReadCapacity(ctx context.Context) (ports.AgyCapacityObservation, error) {
	observation, err := runAgyUsage(ctx, c.binary, c.homeEnv())
	if errors.Is(err, ports.ErrAgyCapacitySignedOut) {
		return ports.AgyCapacityObservation{}, ports.ErrAgyOAuthTokenRevoked
	}
	return observation, err
}

func (c *accountClient) Close() error { return nil }

// homeEnv is the read process's environment: the daemon's, minus the keys
// that would bypass the plan, plus HOME at the managed home when the account
// is not the device-global one.
func (c *accountClient) homeEnv() []string {
	env := environmentWithout(os.Environ(), accountEnvKeysUnset)
	if !c.account.Managed {
		return env
	}
	env = environmentWithout(env, []string{"HOME"})
	return append(env, "HOME="+c.account.Home)
}

// runAgyUsage is the shared quota read used by the device-wide capacity
// reader and by account clients.
func runAgyUsage(ctx context.Context, binary string, env []string) (ports.AgyCapacityObservation, error) {
	stdout, stderr, err := runAgyPrint(ctx, binary, env, "/usage")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ports.AgyCapacityObservation{}, ctxErr
		}
		// The CLI answers a signed-out account on stderr and exits non-zero;
		// the message is classified but never retained.
		if capacitySignedOut(stderr) || capacitySignedOut(stdout) {
			return ports.AgyCapacityObservation{}, ports.ErrAgyCapacitySignedOut
		}
		return ports.AgyCapacityObservation{}, errors.Join(ports.ErrAgyCapacityRequestRejected, errors.New("agy capacity read failed"))
	}
	if capacitySignedOut(stdout) {
		return ports.AgyCapacityObservation{}, ports.ErrAgyCapacitySignedOut
	}
	return parseAgyUsage([]byte(stdout), time.Now().UTC())
}

// authenticationRequired is the CLI's headless prompt for a signed-out home;
// it appears on stdout before the login wait, which the read timeout ends.
func authenticationRequired(text string) bool {
	return strings.Contains(text, "Authentication required")
}
