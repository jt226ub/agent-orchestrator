package ports

import (
	"context"
	"errors"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// Safe Antigravity capacity failures cross the adapter/service boundary
// without retaining raw CLI output, which may contain account details.
var (
	ErrAgyCapacitySignedOut       = errors.New("agy capacity signed out")
	ErrAgyCapacityRequestRejected = errors.New("agy capacity request rejected")
)

// AgyCapacityObservation is normalized plan data before cache freshness and
// safe display reasons are applied by the daemon-owned coordinator.
type AgyCapacityObservation struct {
	Overall           *domain.AgyCapacityBucket
	AdditionalBuckets []domain.AgyCapacityBucket
	ObservedAt        time.Time
}

// AgyCapacityProvider is the daemon-owned capacity coordinator as the session
// manager sees it for profile quota admission: a display-fresh snapshot,
// read from the CLI only when the cache is stale.
type AgyCapacityProvider interface {
	EnsureAgyCapacity(ctx context.Context, force bool) (domain.AgyCapacitySnapshot, error)
}

// AgyCapacityReader is implemented by the Agy adapter: it asks the installed
// Antigravity CLI for the signed-in account's plan limits.
type AgyCapacityReader interface {
	ReadAgyCapacity(ctx context.Context) (AgyCapacityObservation, error)
}
