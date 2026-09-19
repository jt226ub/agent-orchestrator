package ports

import (
	"context"
	"errors"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

var (
	// ErrAgyAccountSwitchInProgress means the global Agy mutation gate is held.
	ErrAgyAccountSwitchInProgress = errors.New("agy account switch already in progress")
	// ErrAgyAccountAlreadyActive rejects selecting the current account.
	ErrAgyAccountAlreadyActive = errors.New("agy account is already active")
	// ErrAgyAccountSwitchIdempotencyConflict rejects reused mismatched keys.
	ErrAgyAccountSwitchIdempotencyConflict = errors.New("agy account switch idempotency conflict")
	// ErrAgyAccountLoginInProgress means native login owns the account gate.
	ErrAgyAccountLoginInProgress = errors.New("agy account login in progress")
	// ErrAgyGlobalAccountChanged reports an external account change during admission.
	ErrAgyGlobalAccountChanged = errors.New("global agy account changed")
	// ErrAgyGlobalCredentialStoreUnsupported rejects non-file-backed switching.
	ErrAgyGlobalCredentialStoreUnsupported = errors.New("global agy credential store is not safely file-backed")
	// ErrAgyAccountSwitchNotCommitted marks a failure that happened before the
	// device-global credential was mutated, so recovery is unnecessary.
	ErrAgyAccountSwitchNotCommitted = errors.New("agy account switch did not mutate the device credential")
)

// AgyOperationLease is one idempotently releasable ownership token for the
// device-global Agy home. Exclusive leases may outlive the admitting request
// while a daemon worker or recovery path owns a durable switch.
type AgyOperationLease interface {
	Release()
}

// AgyOperationGate serializes device-global Agy credential mutation with
// controller registration and ordinary clients of the active Agy home.
type AgyOperationGate interface {
	AcquireShared(context.Context) (release func(), err error)
	AcquireSharedWait(context.Context) (release func(), err error)
	AcquireExclusive(context.Context) (AgyOperationLease, error)
	ExclusivePendingOrHeld() bool
}

// AgyAccountCredentialManager is consumed by the account service's global
// switch coordinator. It exposes account identities and atomic credential activation,
// never credential bytes or homes.
type AgyAccountCredentialManager interface {
	WaitAgyAccountStoreReady(context.Context) error
	EnsureAgyDeviceAccountReconciled(context.Context) error
	BeginAgyAccountMutation(context.Context) error
	EndAgyAccountMutation()
	PrepareAgyAccountForSwitch(context.Context, string, string) (domain.AgyAccountSwitchSource, error)
	InspectAgyAccountSwitch(context.Context, string, domain.AgyAccountSwitchSourceKind, string, string) (domain.AgyAccountSwitchInstallationState, error)
	ActivatePreparedAgyAccountSwitch(context.Context, domain.AgyAccountSwitchSourceKind, string, string) error
	CleanupAgyAccountSwitch(context.Context, string) error
	CleanupInactiveAgyAccountSwitches(context.Context, string) error
}

// AgyAccountSwitchConfig is the validated input to the global switch coordinator.
type AgyAccountSwitchConfig struct {
	TargetAccountID string
	IdempotencyKey  string
}

// AgyAccountSwitchStore persists global switch facts and CAS transitions.
type AgyAccountSwitchStore interface {
	CreateAgyAccountSwitch(context.Context, domain.AgyAccountSwitch) (domain.AgyAccountSwitch, bool, error)
	GetAgyAccountSwitch(context.Context, string) (domain.AgyAccountSwitch, bool, error)
	GetAgyAccountSwitchByIdempotency(context.Context, string) (domain.AgyAccountSwitch, bool, error)
	GetActiveAgyAccountSwitch(context.Context) (domain.AgyAccountSwitch, bool, error)
	UpdateAgyAccountSwitch(context.Context, domain.AgyAccountSwitch, domain.AgyAccountSwitchPhase) (bool, error)
}
