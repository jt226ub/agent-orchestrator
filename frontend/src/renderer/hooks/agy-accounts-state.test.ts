import { describe, expect, it } from "vitest";
import type { AgyAccountsResponse } from "./useAgyAccountsQuery";
import { catalogFor } from "../i18n/messages";
import type { AppLocale } from "../i18n/locales";
import { agyAccountCanSwitch, agyAccountReasonCodes, agyAccountReasonKey, agyAuthenticationDisplay, agySwitchDisplay, mergeAgyAccounts } from "./agy-accounts-state";
import type { AgyAccountSwitch } from "./useAgyAccountsQuery";

const account = (id: string, createdAt: string, active = false) => ({ id, createdAt, active });

function response(accounts: ReturnType<typeof account>[], activeAccountId = "b"): AgyAccountsResponse {
	return {
		accountRevision: 7,
		activeAccountId,
		accounts,
		capabilities: {},
		deviceReconciliation: { status: "verified", activeAccountVerified: true, reasonCode: "verified", retryable: false },
	} as AgyAccountsResponse;
}

describe("mergeAgyAccounts", () => {
	it("preserves unrequested accounts for targeted ensures and derives active-first stable order", () => {
		const current = response([
			account("a", "2026-01-02T00:00:00Z", true),
			account("b", "2026-01-01T00:00:00Z"),
			account("c", "2026-01-01T00:00:00Z"),
		], "a");
		const incoming = response([account("b", "2026-01-03T00:00:00Z", false)], "b");

		const merged = mergeAgyAccounts(current, incoming, "preserveMissing");

		expect(merged.accounts.map(({ id, active }) => [id, active])).toEqual([
			["b", true],
			["c", false],
			["a", false],
		]);
		expect(merged.accountRevision).toBe(7);
	});

	it("removes absent accounts for authoritative GET, mutation, and SSE snapshots", () => {
		const current = response([account("a", "2026-01-01T00:00:00Z"), account("b", "2026-01-02T00:00:00Z")], "a");
		const incoming = response([account("b", "2026-01-02T00:00:00Z", false)], "b");

		expect(mergeAgyAccounts(current, incoming, "replace").accounts).toEqual([
			expect.objectContaining({ id: "b", active: true }),
		]);
	});

	it("shows no active row until local reconciliation verifies device ownership", () => {
		const current = response([
			account("a", "2026-01-02T00:00:00Z", true),
			account("b", "2026-01-01T00:00:00Z"),
		], "a");
		const incoming = {
			...response([account("a", "2026-01-02T00:00:00Z")], "a"),
			activeAccountId: undefined,
			deviceReconciliation: {
				status: "checking",
				activeAccountVerified: false,
				reasonCode: "checking",
				retryable: false,
			},
		} as AgyAccountsResponse;

		const merged = mergeAgyAccounts(current, incoming, "preserveMissing");

		expect(merged.accounts.map(({ id, active }) => [id, active])).toEqual([
			["b", false],
			["a", false],
		]);
	});

	it("clears the presented active row after reconciliation actually fails", () => {
		const current = response([account("a", "2026-01-01T00:00:00Z", true)], "a");
		const incoming = {
			...response([account("a", "2026-01-01T00:00:00Z")], "a"),
			deviceReconciliation: {
				status: "temporarily_unavailable",
				activeAccountVerified: false,
				reasonCode: "account_read_inconclusive",
				retryable: true,
			},
		} as AgyAccountsResponse;

		expect(mergeAgyAccounts(current, incoming, "preserveMissing").accounts).toEqual([
			expect.objectContaining({ id: "a", active: false }),
		]);
	});
});

describe("agyAuthenticationDisplay", () => {
	const display = (state: string, freshness: string, reasonCode: string, status = "valid") => agyAuthenticationDisplay({
		status,
		authentication: { state, freshness, reasonCode },
	} as Parameters<typeof agyAuthenticationDisplay>[0]);

	it("turns inconclusive authentication into an actionable retry", () => {
		expect(display("unknown", "stale", "auth_check_failed")).toEqual({
			key: "settings.agyAccounts.authenticationCheckFailed",
			action: "retry",
			checking: false,
		});
		expect(display("unknown", "stale", "auth_check_timeout").key).toBe("settings.agyAccounts.authenticationCheckTimeout");
	});

	it("keeps checking, unsupported, and rejected credentials distinct", () => {
		expect(display("unknown", "checking", "checking").key).toBe("settings.agyAccounts.authenticationChecking");
		expect(display("unknown", "stale", "auth_check_unsupported").key).toBe("settings.agyAccounts.authenticationUpdateRequired");
		expect(display("unauthorized", "fresh", "unauthorized")).toEqual({
			key: "settings.agyAccounts.reason.authUnauthorized",
			action: "reauthenticate",
			checking: false,
		});
	});
});

it("allows a locally valid saved credential to switch regardless of cached authentication", () => {
	expect(agyAccountCanSwitch({ status: "valid" })).toBe(true);
	expect(agyAccountCanSwitch({ status: "signed_out" })).toBe(false);
});

it("automatically settles legacy recovery journals as switch progress", () => {
	const display = agySwitchDisplay({
		id: "switch-1",
		sourceKind: "managed",
		sourceAccountId: "account-a",
		targetAccountId: "account-b",
		phase: "recovery_required",
		createdAt: "2026-09-02T00:00:00Z",
		updatedAt: "2026-09-02T00:01:00Z",
	} satisfies AgyAccountSwitch);

	expect(display.busy).toBe(true);
	expect(display.mutationBlocked).toBe(true);
	expect(display.key).toBe("settings.agyAccounts.switch.requested");
});

it("presents every normal credential phase as the same switch progress", () => {
	for (const phase of ["requested", "checkpointing_source", "activating_target"] as const) {
		const display = agySwitchDisplay({
			id: "switch-in-progress", sourceKind: "managed", sourceAccountId: "account-a", targetAccountId: "account-b",
			phase,
			createdAt: "2026-09-02T00:00:00Z", updatedAt: "2026-09-02T00:01:00Z",
		} satisfies AgyAccountSwitch);
		expect(display.key).toBe("settings.agyAccounts.switch.requested");
	}
});

it("maps every account reason to complete native locale copy with a safe unknown fallback", () => {
	const locales: AppLocale[] = ["en", "de", "es", "fr", "ja", "ko", "pt-BR", "zh-CN"];
	const switchKeys = ["requested", "completed", "failed", "unknown"].map((phase) => `settings.agyAccounts.switch.${phase}`);
	const keys = [
		...agyAccountReasonCodes.map(agyAccountReasonKey),
		...switchKeys,
		"settings.agyAccounts.authenticationChecking",
		"settings.agyAccounts.authenticationCheckFailed",
		"settings.agyAccounts.authenticationCheckTimeout",
		"settings.agyAccounts.authenticationUpdateRequired",
		"settings.agyAccounts.authenticationAgyNotInstalled",
		"settings.agyAccounts.authenticationRetryFailed",
		"settings.agyAccounts.retryingAuthentication",
		"settings.agyAccounts.tryAgain",
		"settings.agyAccounts.deviceRefreshFailed",
		"settings.agyAccounts.switch.unchanged",
	];
	for (const locale of locales) {
		const catalog = catalogFor(locale);
		for (const key of keys) {
			const value = catalog[key as keyof typeof catalog];
			expect(value, `${locale}: ${key}`).toBeTruthy();
			expect(value).not.toBe(key);
		}
	}
	expect(agyAccountReasonKey("provider-private-message")).toBe("settings.agyAccounts.reason.unknown");
});
