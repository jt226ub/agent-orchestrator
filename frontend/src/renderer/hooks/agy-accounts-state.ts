import type { QueryClient } from "@tanstack/react-query";
import type { AgyAccount, AgyAccountSwitch, AgyAccountsResponse } from "./useAgyAccountsQuery";

export const agyAccountsQueryKey = ["agy-accounts"] as const;

export type AccountMergeMode = "replace" | "preserveMissing";

export function mergeAgyAccounts(
	current: AgyAccountsResponse | undefined,
	incoming: AgyAccountsResponse,
	mode: AccountMergeMode,
): AgyAccountsResponse {
	if (current && incoming.accountRevision < current.accountRevision) return current;
	const accounts = mode === "preserveMissing" && current
		? [...current.accounts.filter((account) => !incoming.accounts.some((next) => next.id === account.id)), ...incoming.accounts]
		: [...incoming.accounts];
	const presentedActiveID = incoming.deviceReconciliation?.status === "verified" && incoming.deviceReconciliation.activeAccountVerified
		? incoming.activeAccountId
		: undefined;
	const normalized = accounts.map((account) => ({
		...account,
		// Device ownership is intentionally not preserved across an unverified
		// reconciliation response. A stale In use badge is more misleading than the
		// short local-check transition.
		active: account.id === presentedActiveID,
	}));
	normalized.sort((left, right) => {
		if (left.active !== right.active) return left.active ? -1 : 1;
		return left.createdAt.localeCompare(right.createdAt) || left.id.localeCompare(right.id);
	});
	return { ...incoming, accounts: normalized };
}

export function writeAgyAccounts(
	queryClient: QueryClient,
	incoming: AgyAccountsResponse,
	mode: AccountMergeMode = "replace",
): void {
	queryClient.setQueryData<AgyAccountsResponse>(agyAccountsQueryKey, (current) =>
		mergeAgyAccounts(current, incoming, mode));
}

/**
 * A Agy account is only shown as signed in when its authentication
 * observation says so. The daemon establishes the active account's observation
 * with the CLI's quota command, the protected call the launch path uses, so an authorized
 * state here means the next Agy session can start.
 */
export function agyAccountAuthorized(account: Pick<AgyAccount, "authentication">): boolean {
	return account.authentication.state === "authorized" || account.authentication.state === "not_applicable";
}

export type AgyAuthenticationDisplay = {
	key:
		| "settings.agyAccounts.signedIn"
		| "settings.agyAccounts.signedOut"
		| "settings.agyAccounts.reason.authUnauthorized"
		| "settings.agyAccounts.authenticationChecking"
		| "settings.agyAccounts.authenticationCheckFailed"
		| "settings.agyAccounts.authenticationCheckTimeout"
		| "settings.agyAccounts.authenticationUpdateRequired"
		| "settings.agyAccounts.authenticationAgyNotInstalled";
	action: "retry" | "reauthenticate" | null;
	checking: boolean;
};

export function agyAuthenticationDisplay(account: Pick<AgyAccount, "authentication" | "status">): AgyAuthenticationDisplay {
	const authentication = account.authentication;
	if (account.status === "signed_out") {
		return { key: "settings.agyAccounts.signedOut", action: "reauthenticate", checking: false };
	}
	if (authentication.freshness === "checking") {
		return { key: "settings.agyAccounts.authenticationChecking", action: null, checking: true };
	}
	if (authentication.state === "unauthorized") {
		return { key: "settings.agyAccounts.reason.authUnauthorized", action: "reauthenticate", checking: false };
	}
	switch (authentication.reasonCode) {
		case "auth_skipped_not_installed":
			return { key: "settings.agyAccounts.authenticationAgyNotInstalled", action: null, checking: false };
		case "auth_check_unsupported":
			return { key: "settings.agyAccounts.authenticationUpdateRequired", action: null, checking: false };
		case "auth_check_timeout":
			return { key: "settings.agyAccounts.authenticationCheckTimeout", action: "retry", checking: false };
		case "auth_check_failed":
		case "auth_check_inconclusive":
			return { key: "settings.agyAccounts.authenticationCheckFailed", action: "retry", checking: false };
	}
	if (agyAccountAuthorized(account)) {
		return { key: "settings.agyAccounts.signedIn", action: null, checking: false };
	}
	if (authentication.reasonCode === "not_checked") {
		return { key: "settings.agyAccounts.authenticationChecking", action: null, checking: true };
	}
	return { key: "settings.agyAccounts.authenticationCheckFailed", action: "retry", checking: false };
}

// Switching installs an already-saved local credential. A temporary provider
// or network failure must not make that credential unusable; only a definitive
// signed-out state requires login first.
export function agyAccountCanSwitch(account: Pick<AgyAccount, "status">): boolean {
	return account.status === "valid";
}

const reasonKeys = {
	account_valid: "settings.agyAccounts.reason.accountValid",
	account_signed_out: "settings.agyAccounts.reason.accountSignedOut",
	account_descriptor_invalid: "settings.agyAccounts.reason.accountDescriptorInvalid",
	account_credential_home_missing: "settings.agyAccounts.reason.accountCredentialHomeMissing",
	account_unsafe_path: "settings.agyAccounts.reason.accountUnsafePath",
	authorized: "settings.agyAccounts.reason.authAuthorized",
	unauthorized: "settings.agyAccounts.reason.authUnauthorized",
	not_applicable: "settings.agyAccounts.reason.authNotApplicable",
	not_checked: "settings.agyAccounts.reason.authNotChecked",
	auth_check_failed: "settings.agyAccounts.reason.authCheckFailed",
	auth_check_inconclusive: "settings.agyAccounts.reason.authCheckInconclusive",
	auth_check_timeout: "settings.agyAccounts.reason.authCheckTimeout",
	auth_check_unsupported: "settings.agyAccounts.reason.authCheckUnsupported",
	auth_skipped_not_installed: "settings.agyAccounts.reason.authSkippedNotInstalled",
	capacity_available: "settings.agyAccounts.reason.capacityAvailable",
	capacity_near_limit: "settings.agyAccounts.reason.capacityNearLimit",
	capacity_exhausted: "settings.agyAccounts.reason.capacityExhausted",
	capacity_unsupported: "settings.agyAccounts.reason.capacityUnsupported",
	capacity_not_checked: "settings.agyAccounts.reason.capacityNotChecked",
	capacity_checking: "settings.agyAccounts.reason.capacityChecking",
	capacity_check_failed: "settings.agyAccounts.reason.capacityCheckFailed",
	capacity_client_start_failed: "settings.agyAccounts.reason.capacityClientStartFailed",
	capacity_provider_rejected: "settings.agyAccounts.reason.capacityProviderRejected",
	capacity_provider_unavailable: "settings.agyAccounts.reason.capacityProviderUnavailable",
	capacity_check_stopped: "settings.agyAccounts.reason.capacityCheckStopped",
	capacity_check_inconclusive: "settings.agyAccounts.reason.capacityCheckInconclusive",
	capacity_check_timeout: "settings.agyAccounts.reason.capacityCheckTimeout",
	capacity_skipped_auth_unknown: "settings.agyAccounts.reason.capacitySkippedAuthUnknown",
	capacity_skipped_signed_out: "settings.agyAccounts.reason.capacitySkippedSignedOut",
	capacity_account_unavailable: "settings.agyAccounts.reason.capacityAccountUnavailable",
	capacity_invalidated: "settings.agyAccounts.reason.capacityInvalidated",
	capacity_not_installed: "settings.agyAccounts.reason.capacityNotInstalled",
	daemon_token_override: "settings.agyAccounts.reason.daemonTokenOverride",
	supported: "settings.agyAccounts.reason.supported",
	unsupported: "settings.agyAccounts.reason.unsupported",
	unknown: "settings.agyAccounts.reason.unknown",
	global_credential_store_unsupported: "settings.agyAccounts.reason.globalCredentialStoreUnsupported",
	global_account_changed: "settings.agyAccounts.reason.globalAccountChanged",
	login_pending: "settings.agyAccounts.reason.loginPending",
	login_completed: "settings.agyAccounts.reason.loginCompleted",
	login_cancelled: "settings.agyAccounts.reason.loginCancelled",
	login_failed: "settings.agyAccounts.reason.loginFailed",
	login_unauthorized: "settings.agyAccounts.reason.loginUnauthorized",
	login_expired: "settings.agyAccounts.reason.loginExpired",
	switch_state_unavailable: "settings.agyAccounts.reason.switchStateUnavailable",
} as const;

export const agyAccountReasonCodes = Object.keys(reasonKeys) as Array<keyof typeof reasonKeys>;

export type AgyAccountMessageKey = (typeof reasonKeys)[keyof typeof reasonKeys]
	| "settings.agyAccounts.switch.requested"
	| "settings.agyAccounts.switch.completed"
	| "settings.agyAccounts.switch.failed"
	| "settings.agyAccounts.switch.unknown";

export function agyAccountReasonKey(reasonCode: string | null | undefined): AgyAccountMessageKey {
	return reasonKeys[reasonCode as keyof typeof reasonKeys] ?? "settings.agyAccounts.reason.unknown";
}

export type AgySwitchDisplay = {
	key: AgyAccountMessageKey;
	tone: "muted" | "warning" | "error";
	busy: boolean;
	mutationBlocked: boolean;
};

export function agySwitchDisplay(switchState: AgyAccountSwitch): AgySwitchDisplay {
	const phase = switchState.phase;
	const terminal = phase === "completed" || phase === "failed";
	const busy = !terminal;
	let key: AgyAccountMessageKey;
	if (busy) {
		key = "settings.agyAccounts.switch.requested";
	} else if (phase === "completed") {
		key = "settings.agyAccounts.switch.completed";
	} else if (phase === "failed") {
		key = "settings.agyAccounts.switch.failed";
	} else {
		key = "settings.agyAccounts.switch.unknown";
	}
	return {
		key,
		tone: phase === "failed" ? "error" : "muted",
		busy,
		mutationBlocked: phase !== "completed" && phase !== "failed",
	};
}
