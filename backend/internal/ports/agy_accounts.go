package ports

import (
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Safe account failures cross the adapter/service boundary without retaining
// raw CLI output, which may contain account details.
var (
	// ErrAgyOAuthTokenRevoked means the CLI reported the saved credential as
	// signed out: the plan no longer accepts it.
	ErrAgyOAuthTokenRevoked = errors.New("agy oauth token revoked")
	// ErrAgyCapacityProviderUnavailable means the CLI could not be run at all.
	ErrAgyCapacityProviderUnavailable = errors.New("agy capacity provider unavailable")
	// ErrAgyAccountLogoutUnsupported reports that the Antigravity CLI offers no
	// sign-out verb: AO can only delete a saved account's credential slot.
	ErrAgyAccountLogoutUnsupported = errors.New("agy account logout unsupported")
)

// AgyAccountContext selects the home directory one Antigravity account client
// runs the CLI against. The CLI keys its config directory off HOME, so a
// managed home is an AO-owned skeleton holding only the credential files;
// the device-global home is the user's real one and is never rewritten.
type AgyAccountContext struct {
	Home    string
	Managed bool
}

// AgyAccountObservation is the safe subset of a credential check retained by
// AO: whether the CLI accepted the credential, and the display metadata the
// daemon derived locally from the credential file (never the tokens).
type AgyAccountObservation struct {
	Authentication domain.AgentAuthenticationState
	Method         domain.AgyAuthMethod
	Email          *string
}

// AgyAccountClient reads one account's state by running the installed CLI
// against that account's home.
type AgyAccountClient interface {
	// Read reports whether the account's credential is accepted by the plan;
	// it runs the CLI's quota command, the one protected call the CLI offers.
	Read(ctx context.Context) (AgyAccountObservation, error)
	ReadCapacity(ctx context.Context) (AgyCapacityObservation, error)
	Close() error
}

// AgyAccountClientFactory opens account clients and reports the installed
// CLI's surface without exposing transport details to services.
type AgyAccountClientFactory interface {
	Open(ctx context.Context, account AgyAccountContext) (AgyAccountClient, error)
	Capabilities(ctx context.Context) domain.AgyAccountCapabilities
}
