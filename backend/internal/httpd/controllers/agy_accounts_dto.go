package controllers

import (
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
)

func newAgyAccountsResponse(input agentsvc.AgyAccounts) AgyAccountsResponse { //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).
	accounts := make([]AgyAccountResponse, len(input.Accounts))
	for i := range input.Accounts {
		accounts[i] = newAgyAccountResponse(input.Accounts[i])
	}
	response := AgyAccountsResponse{
		ActiveAccountID: input.ActiveAccountID, AccountRevision: input.AccountRevision,
		Accounts: accounts, Capabilities: newAgyCapabilitiesResponse(input.Capabilities),
		DeviceReconciliation: AgyDeviceReconciliationResponse{
			Status: string(input.DeviceReconciliation.Status), ActiveAccountVerified: input.DeviceReconciliation.ActiveAccountVerified,
			ReasonCode: input.DeviceReconciliation.ReasonCode, Retryable: input.DeviceReconciliation.Retryable,
			AttemptedAt: input.DeviceReconciliation.AttemptedAt, VerifiedAt: input.DeviceReconciliation.VerifiedAt,
			NextRetryAt: input.DeviceReconciliation.NextRetryAt,
		},
	}
	if input.ActiveLogin != nil {
		response.ActiveLogin = &AgyActiveLoginResponse{
			OperationID: input.ActiveLogin.OperationID, AccountID: input.ActiveLogin.AccountID,
			Status: string(input.ActiveLogin.Status), ReasonCode: input.ActiveLogin.ReasonCode,
			Reason: input.ActiveLogin.Reason, ExpiresAt: input.ActiveLogin.ExpiresAt,
			ShellTerminal: AgyAccountLoginTerminalResponse{
				HandleID: input.ActiveLogin.ShellTerminal.HandleID, Title: input.ActiveLogin.ShellTerminal.Title,
				CreatedAt: input.ActiveLogin.ShellTerminal.CreatedAt,
			},
		}
	}
	if input.CurrentSwitch != nil {
		switchResponse := newAgySwitchResponse(*input.CurrentSwitch)
		response.CurrentSwitch = &switchResponse
	}
	return response
}

func newAgyAccountResponse(input domain.AgyAccountSnapshot) AgyAccountResponse {
	response := AgyAccountResponse{
		ID: input.ID, Label: input.Label, Status: string(input.Status), ReasonCode: input.ReasonCode, Reason: input.Reason,
		Active: input.Active, AuthMethod: string(input.AuthMethod), AccountEmail: input.AccountEmail, CreatedAt: input.CreatedAt,
		Authentication: AgyAuthenticationResponse{
			State: string(input.Authentication.State), Freshness: string(input.Authentication.Freshness),
			CheckedAt: input.Authentication.CheckedAt, AttemptedAt: input.Authentication.AttemptedAt,
			ReasonCode: input.Authentication.ReasonCode, Reason: input.Authentication.Reason,
		},
		Capacity: newAgyCapacityResponse(input.Capacity),
	}
	return response
}

func newAgyCapabilitiesResponse(input domain.AgyAccountCapabilities) AgyAccountCapabilitiesResponse {
	return AgyAccountCapabilitiesResponse{
		NativeLogin:  newAgyCapabilityResponse(input.NativeLogin),
		GlobalSwitch: newAgyCapabilityResponse(input.GlobalSwitch),
	}
}

func newAgyCapabilityResponse(input domain.AgyCapabilityObservation) AgyCapabilityObservationResponse {
	return AgyCapabilityObservationResponse{State: string(input.State), ReasonCode: input.ReasonCode, Reason: input.Reason}
}

func newAgyLoginResponse(input domain.AgyAccountLoginOperation) AgyAccountLoginResponse {
	response := AgyAccountLoginResponse{
		OperationID: input.OperationID, AccountID: input.AccountID, Status: string(input.Status),
		ReasonCode: input.ReasonCode, Reason: input.Reason, ExpiresAt: input.ExpiresAt,
	}
	if input.Account != nil {
		account := newAgyAccountResponse(*input.Account)
		response.Account = &account
	}
	return response
}

func newAgySwitchResponse(input domain.AgyAccountSwitch) AgyAccountSwitchResponse {
	return AgyAccountSwitchResponse{
		ID: input.ID, SourceKind: string(input.SourceKind), SourceAccountID: input.SourceAccountID, TargetAccountID: input.TargetAccountID,
		Phase: AgyAccountSwitchPhase(input.Phase), FailureCode: input.FailureCode,
		CredentialsCommittedAt: input.CredentialsCommittedAt,
		CreatedAt:              input.CreatedAt, UpdatedAt: input.UpdatedAt, CompletedAt: input.CompletedAt,
	}
}
