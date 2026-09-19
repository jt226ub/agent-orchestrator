package domain //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).

import "time"

// AgyAccountSwitchSourceKind describes what occupied the device credential
// store before a durable switch began.
type AgyAccountSwitchSourceKind string

const (
	// AgyAccountSwitchSourceManaged means the device credential belongs to a saved AO account.
	AgyAccountSwitchSourceManaged AgyAccountSwitchSourceKind = "managed"
	// AgyAccountSwitchSourceDevice means the device has an unmatched credential.
	AgyAccountSwitchSourceDevice AgyAccountSwitchSourceKind = "device"
	// AgyAccountSwitchSourceNone means the device has no Agy credential.
	AgyAccountSwitchSourceNone AgyAccountSwitchSourceKind = "none"
)

// AgyAccountSwitchSource is the daemon-private, reconciled source snapshot
// used to admit a switch.
type AgyAccountSwitchSource struct {
	Kind      AgyAccountSwitchSourceKind
	AccountID string
}

// AgyAccountSwitchInstallationState is the conclusively observed local
// device state used to settle an interrupted credential switch.
type AgyAccountSwitchInstallationState string

// Local Agy account switch installation states.
const (
	AgyAccountSwitchTargetInstalled    AgyAccountSwitchInstallationState = "target"
	AgyAccountSwitchSourceInstalled    AgyAccountSwitchInstallationState = "source"
	AgyAccountSwitchCredentialMissing  AgyAccountSwitchInstallationState = "missing"
	AgyAccountSwitchExternalCredential AgyAccountSwitchInstallationState = "external"
)

// AgyAccountSwitchPhase is the durable global credential-switch phase.
type AgyAccountSwitchPhase string

const (
	// AgyAccountSwitchRequested is the initial durable switch phase.
	AgyAccountSwitchRequested AgyAccountSwitchPhase = "requested"
	// AgyAccountSwitchCheckpointCredential journals the global source credential.
	AgyAccountSwitchCheckpointCredential AgyAccountSwitchPhase = "checkpointing_source"
	// AgyAccountSwitchActivatingAccount stages the selected target credential.
	AgyAccountSwitchActivatingAccount AgyAccountSwitchPhase = "activating_target"
	// AgyAccountSwitchRecoveryRequired is retained only to settle journals
	// written by older builds. New switches never enter this phase.
	AgyAccountSwitchRecoveryRequired AgyAccountSwitchPhase = "recovery_required"
	// AgyAccountSwitchCompleted means target activation succeeded.
	AgyAccountSwitchCompleted AgyAccountSwitchPhase = "completed"
	// AgyAccountSwitchFailed means the source remained or was restored safely.
	AgyAccountSwitchFailed AgyAccountSwitchPhase = "failed"
)

// Terminal reports whether no more switch work may run automatically.
func (p AgyAccountSwitchPhase) Terminal() bool {
	return p == AgyAccountSwitchCompleted || p == AgyAccountSwitchFailed
}

// AgyAccountSwitch is the durable global account-switch operation.
type AgyAccountSwitch struct {
	ID                     string                     `json:"id"`
	SourceKind             AgyAccountSwitchSourceKind `json:"sourceKind" enum:"managed,device,none"`
	SourceAccountID        string                     `json:"sourceAccountId,omitempty"`
	TargetAccountID        string                     `json:"targetAccountId"`
	Phase                  AgyAccountSwitchPhase      `json:"phase"`
	FailureCode            string                     `json:"failureCode,omitempty"`
	CredentialsCommittedAt *time.Time                 `json:"credentialsCommittedAt,omitempty"`
	CreatedAt              time.Time                  `json:"createdAt"`
	UpdatedAt              time.Time                  `json:"updatedAt"`
	CompletedAt            *time.Time                 `json:"completedAt,omitempty"`
	// Daemon-private idempotency data.
	IdempotencyKey string `json:"-"`
}
