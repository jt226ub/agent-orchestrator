package domain

import "time"

// AgyAccountSource identifies an AO-owned Agy credential slot. Device
// credentials are an import source only; once verified, the slot is managed.
type AgyAccountSource string

// AgyAccountSourceManaged identifies an AO-owned credential slot.
const AgyAccountSourceManaged AgyAccountSource = "managed"

// AgyAccountStatus describes whether a discovered account slot is usable.
type AgyAccountStatus string

const (
	// AgyAccountStatusValid means the descriptor and credential home are safe.
	AgyAccountStatusValid AgyAccountStatus = "valid"
	// AgyAccountStatusSignedOut means the descriptor and private home are safe,
	// but the account currently has no saved credential.
	AgyAccountStatusSignedOut AgyAccountStatus = "signed_out"
	// AgyAccountStatusBroken means the slot is visible but cannot be used.
	AgyAccountStatusBroken AgyAccountStatus = "broken"
)

// AgyDeviceReconciliationStatus describes whether AO has conclusively
// identified the account currently installed in Agy's device-global home.
// It is runtime state only; the durable active-account pointer remains the
// last known selection across daemon restarts.
type AgyDeviceReconciliationStatus string

const (
	// AgyDeviceReconciliationNotChecked means device discovery has not run.
	AgyDeviceReconciliationNotChecked AgyDeviceReconciliationStatus = "not_checked"
	// AgyDeviceReconciliationChecking means device discovery is in progress.
	AgyDeviceReconciliationChecking AgyDeviceReconciliationStatus = "checking"
	// AgyDeviceReconciliationVerified means the canonical device state was conclusively identified.
	AgyDeviceReconciliationVerified AgyDeviceReconciliationStatus = "verified"
	// AgyDeviceReconciliationTemporarilyUnavailable means discovery failed and will be retried.
	AgyDeviceReconciliationTemporarilyUnavailable AgyDeviceReconciliationStatus = "temporarily_unavailable"
	// AgyDeviceReconciliationBlocked means switching requires a user or environment change.
	AgyDeviceReconciliationBlocked AgyDeviceReconciliationStatus = "blocked"
)

// AgyDeviceReconciliation is the display-safe, ephemeral state of device
// discovery. Provider output, credential paths, and secret-bearing errors must
// never be copied into this projection.
type AgyDeviceReconciliation struct {
	Status                AgyDeviceReconciliationStatus `json:"status" enum:"not_checked,checking,verified,temporarily_unavailable,blocked"`
	ActiveAccountVerified bool                          `json:"activeAccountVerified"`
	ReasonCode            string                        `json:"reasonCode"`
	Retryable             bool                          `json:"retryable"`
	AttemptedAt           *time.Time                    `json:"attemptedAt,omitempty"`
	VerifiedAt            *time.Time                    `json:"verifiedAt,omitempty"`
	NextRetryAt           *time.Time                    `json:"nextRetryAt,omitempty"`
}

// AgyAuthMethod identifies the provider authentication mechanism.
type AgyAuthMethod string

const (
	// AgyAuthMethodGoogle represents browser-backed Google account authentication.
	AgyAuthMethodGoogle AgyAuthMethod = "google"
	// AgyAuthMethodOther represents a recognized unclassified mechanism.
	AgyAuthMethodOther AgyAuthMethod = "other"
	// AgyAuthMethodUnknown means the mechanism could not be established.
	AgyAuthMethodUnknown AgyAuthMethod = "unknown"
)

// AgyCapabilityState is the support state of one installed Agy capability.
type AgyCapabilityState string

const (
	// AgyCapabilitySupported means the installed protocol exposes the capability.
	AgyCapabilitySupported AgyCapabilityState = "supported"
	// AgyCapabilityUnsupported means a successful probe proved absence.
	AgyCapabilityUnsupported AgyCapabilityState = "unsupported"
	// AgyCapabilityUnknown means the capability probe was inconclusive.
	AgyCapabilityUnknown AgyCapabilityState = "unknown"
)

// AgyCapabilityObservation is a display-safe capability result.
type AgyCapabilityObservation struct {
	State      AgyCapabilityState `json:"state" enum:"supported,unsupported,unknown"`
	ReasonCode string             `json:"reasonCode"`
	Reason     string             `json:"reason"`
}

// AgyAccountCapabilities reports the account-management surface of the
// installed CLI. There is no usage summary and no reset credit for
// Antigravity; those Codex capabilities have no counterpart here.
type AgyAccountCapabilities struct {
	AccountRead  AgyCapabilityObservation `json:"accountRead"`
	NativeLogin  AgyCapabilityObservation `json:"nativeLogin"`
	CapacityRead AgyCapabilityObservation `json:"capacityRead"`
	GlobalSwitch AgyCapabilityObservation `json:"globalSwitch"`
}

// AgyAccountSnapshot is the display-safe cached state for one AO account.
type AgyAccountSnapshot struct {
	ID             string                         `json:"id"`
	Label          string                         `json:"label"`
	Source         AgyAccountSource               `json:"source" enum:"managed"`
	Status         AgyAccountStatus               `json:"status" enum:"valid,signed_out,broken"`
	ReasonCode     string                         `json:"reasonCode"`
	Reason         string                         `json:"reason"`
	Active         bool                           `json:"active"`
	Authentication AgentAuthenticationObservation `json:"authentication"`
	AuthMethod     AgyAuthMethod                  `json:"authMethod" enum:"google,other,unknown"`
	AccountEmail   *string                        `json:"accountEmail,omitempty"`
	Capacity       AgyCapacitySnapshot            `json:"capacity"`
	CreatedAt      time.Time                      `json:"createdAt"`
}

const (
	// AgyAccountReasonValid identifies a usable account slot.
	AgyAccountReasonValid = "account_valid"
	// AgyAccountReasonSignedOut identifies a retained account without credentials.
	AgyAccountReasonSignedOut = "account_signed_out"
	// AgyAccountReasonDescriptorInvalid identifies a malformed descriptor.
	AgyAccountReasonDescriptorInvalid = "account_descriptor_invalid"
	// AgyAccountReasonHomeMissing identifies a missing credential home.
	AgyAccountReasonHomeMissing = "account_credential_home_missing"
	// AgyAccountReasonUnsafePath identifies unsafe ownership or link state.
	AgyAccountReasonUnsafePath = "account_unsafe_path"
	// AgyCapabilityReasonSupported is the safe supported reason code.
	AgyCapabilityReasonSupported = "supported"
	// AgyCapabilityReasonUnsupported is the safe unsupported reason code.
	AgyCapabilityReasonUnsupported = "unsupported"
	// AgyCapabilityReasonUnknown is the safe inconclusive reason code.
	AgyCapabilityReasonUnknown = "unknown"
)

// AgyAccountLoginStatus is the daemon-owned pending login lifecycle state.
type AgyAccountLoginStatus string

const (
	// AgyAccountLoginPending means the native login terminal is still active.
	AgyAccountLoginPending AgyAccountLoginStatus = "pending"
	// AgyAccountLoginVerifying means structured verification is running.
	AgyAccountLoginVerifying AgyAccountLoginStatus = "verifying"
	// AgyAccountLoginUnauthorized means verification confirmed signed-out state.
	AgyAccountLoginUnauthorized AgyAccountLoginStatus = "unauthorized"
	// AgyAccountLoginRetryable means the local credential is not ready yet.
	AgyAccountLoginRetryable AgyAccountLoginStatus = "retryable"
	// AgyAccountLoginCompleted means a verified account was committed.
	AgyAccountLoginCompleted AgyAccountLoginStatus = "completed"
	// AgyAccountLoginCancelled means terminal and staging were removed.
	AgyAccountLoginCancelled AgyAccountLoginStatus = "cancelled"
	// AgyAccountLoginFailed means the operation could not complete safely.
	AgyAccountLoginFailed AgyAccountLoginStatus = "failed"
	// AgyAccountLoginExpired means the terminal exceeded its lifetime.
	AgyAccountLoginExpired AgyAccountLoginStatus = "expired"
)

const (
	// AgyAccountLoginReasonPending is the safe pending-login reason code.
	AgyAccountLoginReasonPending = "login_pending"
	// AgyAccountLoginReasonCompleted is the safe completed-login reason code.
	AgyAccountLoginReasonCompleted = "login_completed"
	// AgyAccountLoginReasonCancelled is the safe cancelled-login reason code.
	AgyAccountLoginReasonCancelled = "login_cancelled"
	// AgyAccountLoginReasonFailed is the safe failed-login reason code.
	AgyAccountLoginReasonFailed = "login_failed"
	// AgyAccountLoginReasonUnauthorized is the safe signed-out reason code.
	AgyAccountLoginReasonUnauthorized = "login_unauthorized"
	// AgyAccountLoginReasonExpired is the safe expiry reason code.
	AgyAccountLoginReasonExpired = "login_expired"
)

// AgyAccountLoginOperation is the safe transient login projection.
type AgyAccountLoginOperation struct {
	OperationID string                `json:"operationId"`
	AccountID   string                `json:"accountId,omitempty"`
	Status      AgyAccountLoginStatus `json:"status" enum:"pending,verifying,unauthorized,retryable,completed,cancelled,failed,expired"`
	ReasonCode  string                `json:"reasonCode"`
	Reason      string                `json:"reason"`
	Account     *AgyAccountSnapshot   `json:"account,omitempty"`
	ExpiresAt   time.Time             `json:"expiresAt"`
}
