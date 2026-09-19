import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, expect, it, vi } from "vitest";
import { writeAgyAccounts } from "../../hooks/agy-accounts-state";
import type { AgyAccountsResponse } from "../../hooks/useAgyAccountsQuery";
import { useUiStore } from "../../stores/ui-store";
import { TooltipProvider } from "../ui/tooltip";
import { AgyAccountsSection } from "./AgyAccountsSection";

const { deleteMock, getMock, postMock, scrollIntoViewMock, terminalStateCallback, terminalTarget } = vi.hoisted(() => ({
	deleteMock: vi.fn(),
	getMock: vi.fn(),
	postMock: vi.fn(),
	scrollIntoViewMock: vi.fn(),
	terminalStateCallback: { value: undefined as ((state: "exited" | "error") => void) | undefined },
	terminalTarget: { value: undefined as { handleId: string; generation: string; title: string } | undefined },
}));

vi.mock("../../lib/api-client", () => ({
	apiClient: { DELETE: deleteMock, GET: getMock, POST: postMock },
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed",
}));

vi.mock("../TerminalPane", () => ({
	TerminalPane: ({ onTerminalStateChange, terminalTarget: target }: { onTerminalStateChange?: (state: "exited" | "error") => void; terminalTarget: { handleId: string; generation: string; title: string } }) => {
		terminalStateCallback.value = onTerminalStateChange;
		terminalTarget.value = target;
		return <div data-testid="inline-terminal-body" />;
	},
}));

const capability = (state = "supported") => ({ state, reasonCode: state, reason: state === "supported" ? "Available." : "Unavailable." });
const authentication = { state: "authorized", freshness: "fresh", checkedAt: "2026-08-31T10:00:00Z", attemptedAt: "2026-08-31T10:00:00Z", reasonCode: "authorized", reason: "Antigravity is signed in." };
const capacity = { state: "available", freshness: "fresh", usedPercent: 4, remainingPercent: 96, resetsAt: null, observedAt: "2026-08-31T10:00:00Z", checkedAt: "2026-08-31T10:00:00Z", attemptedAt: "2026-08-31T10:00:00Z", reasonCode: "capacity_available", reason: "Capacity is available.", overall: null, additionalBuckets: [] };
const activeAccount = { id: "11111111-1111-4111-8111-111111111111", label: "active@example.com", source: "managed", status: "valid", reasonCode: "account_valid", reason: "Available.", active: true, authentication, authMethod: "google", accountEmail: "active@example.com", capacity, createdAt: "2026-08-31T09:00:00Z" };
const inactiveAccount = { ...activeAccount, id: "22222222-2222-4222-8222-222222222222", label: "other@example.com", accountEmail: "other@example.com", active: false, createdAt: "2026-08-31T09:05:00Z" };
const accountResponse = {
	activeAccountId: activeAccount.id,
	accountRevision: 3,
	accounts: [activeAccount, inactiveAccount],
	capabilities: {
		nativeLogin: capability(), globalSwitch: capability(),
	},
	deviceReconciliation: { status: "verified", activeAccountVerified: true, reasonCode: "verified", retryable: false },
};
const pendingLogin = {
	operation: { operationId: "login-1", status: "pending", reasonCode: "login_pending", reason: "Waiting for Antigravity sign-in.", expiresAt: "2026-08-31T10:15:00Z" },
	shellTerminal: { handleId: "shellterm-login-1", title: "Add Antigravity account", createdAt: "2026-08-31T10:00:00Z" },
};

function renderSection() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return { queryClient, ...render(<QueryClientProvider client={queryClient}><TooltipProvider><AgyAccountsSection /></TooltipProvider></QueryClientProvider>) };
}

beforeEach(() => {
	Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrollIntoViewMock });
	scrollIntoViewMock.mockReset();
	terminalStateCallback.value = undefined;
	terminalTarget.value = undefined;
	useUiStore.setState({ settingsModal: { scope: "global", section: "agents" } });
	getMock.mockReset().mockResolvedValue({ data: accountResponse });
	deleteMock.mockReset().mockResolvedValue({ data: accountResponse });
	postMock.mockReset().mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/accounts/ensure") return Promise.resolve({ data: accountResponse });
		if (path === "/api/v1/agents/agy/accounts/login-terminal") return Promise.resolve({ data: pendingLogin });
		return Promise.resolve({ data: {} });
	});
});

it("shows active-first account cards with correct remaining capacity", async () => {
	renderSection();
	expect(await screen.findByText("active@example.com")).toBeInTheDocument();
	expect(screen.getByText("In use")).toBeInTheDocument();
	expect(screen.getAllByText(/96% remaining/).length).toBeGreaterThan(0);
	expect(screen.queryByText(/The selected account is the device/)).not.toBeInTheDocument();
	expect(screen.queryByText(/credential/i)).not.toBeInTheDocument();
	expect(screen.queryByText(/billing/i)).not.toBeInTheDocument();
});

it("preserves the complete account list when expanding accounts performs targeted ensures", async () => {
	postMock.mockImplementation((path: string, request?: { body?: { accountIds?: string[] } }) => {
		if (path !== "/api/v1/agents/agy/accounts/ensure") return Promise.resolve({ data: {} });
		const ids = request?.body?.accountIds ?? [];
		if (ids.length === 0) return Promise.resolve({ data: accountResponse });
		return Promise.resolve({ data: { ...accountResponse, accounts: accountResponse.accounts.filter((account) => ids.includes(account.id)) } });
	});
	const { container } = renderSection();
	expect(await screen.findByText("active@example.com")).toBeInTheDocument();
	expect(screen.getByText("other@example.com")).toBeInTheDocument();

	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);
	await waitFor(() => expect(postMock).toHaveBeenCalledWith(
		"/api/v1/agents/agy/accounts/ensure",
		{ body: { accountIds: [activeAccount.id]} },
	));
	expect(screen.getByText("active@example.com")).toBeInTheDocument();
	expect(screen.getByText("other@example.com")).toBeInTheDocument();

	fireEvent.click(container.querySelector(`[data-account-id="${inactiveAccount.id}"] button`) as HTMLButtonElement);
	await waitFor(() => expect(postMock).toHaveBeenCalledWith(
		"/api/v1/agents/agy/accounts/ensure",
		{ body: { accountIds: [inactiveAccount.id]} },
	));
	expect(screen.getByText("active@example.com")).toBeInTheDocument();
	expect(screen.getByText("other@example.com")).toBeInTheDocument();
});

it("does not offer switching when the device account has no reconciled source", async () => {
	const unreconciledAccount = { ...activeAccount, active: false };
	const unreconciledResponse = {
		...accountResponse,
		activeAccountId: undefined,
		accounts: [unreconciledAccount],
		deviceReconciliation: { status: "blocked", activeAccountVerified: false, reasonCode: "global_credential_store_unsupported", retryable: false },
	};
	getMock.mockResolvedValue({ data: unreconciledResponse });
	postMock.mockResolvedValue({ data: unreconciledResponse });

	renderSection();
	expect(await screen.findByText("Couldn’t refresh the Antigravity account.")).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "Switch to this account" })).not.toBeInTheDocument();
});

it("shows a simple empty state when Antigravity is signed out on the device", async () => {
	const signedOutDevice = {
		...accountResponse,
		activeAccountId: undefined,
		accounts: accountResponse.accounts.map((account) => ({ ...account, active: false })),
		deviceReconciliation: { status: "verified", activeAccountVerified: false, reasonCode: "verified", retryable: false },
	};
	getMock.mockResolvedValue({ data: signedOutDevice });
	postMock.mockResolvedValue({ data: signedOutDevice });

	const { container } = renderSection();
	expect(await screen.findAllByText("No account is currently in use. Choose an account below.")).toHaveLength(1);
	expect(screen.queryByText(/No Antigravity account is currently in use/)).not.toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "Switch account" })).not.toBeInTheDocument();
	const firstRow = container.querySelector(`[data-account-id="${activeAccount.id}"]`) as HTMLElement;
	const signedIn = within(firstRow).getByText("Signed in");
	const subscription = within(firstRow).getByText("Google · 96% remaining");
	expect(signedIn.closest("p")).toBe(subscription.closest("p"));
	const useButton = within(firstRow).getByRole("button", { name: "Use this account active@example.com" });
	expect(useButton).toBeEnabled();
	expect(useButton).toHaveTextContent(/^Use$/);
	expect(firstRow.querySelector('button[aria-expanded="false"]')).toBeInTheDocument();
	expect(firstRow.firstElementChild).toHaveClass("items-start");
	expect(firstRow.querySelector(".lucide-chevron-down")).toHaveClass("mt-1.5");
});

it("does not offer an empty provider expand interaction when no accounts exist", async () => {
	const emptyResponse = {
		...accountResponse,
		activeAccountId: undefined,
		accounts: [],
		deviceReconciliation: { status: "verified", activeAccountVerified: false, reasonCode: "verified", retryable: false },
	};
	getMock.mockResolvedValue({ data: emptyResponse });
	postMock.mockResolvedValue({ data: emptyResponse });

	const { container } = renderSection();
	expect(await screen.findByText("Sign in to use Antigravity.")).toBeInTheDocument();
	const provider = container.querySelector('[data-agent-provider="agy"]') as HTMLElement;
	expect(provider.querySelector('header button[aria-expanded]')).not.toBeInTheDocument();
	expect(provider.querySelector("header .lucide-chevron-down")).not.toBeInTheDocument();
	expect(within(provider).getByRole("button", { name: "Add account" })).toBeEnabled();
});


it("keeps saved accounts and local actions available while device reconciliation retries", async () => {
	const degraded = {
		...accountResponse,
		deviceReconciliation: {
			status: "temporarily_unavailable",
			activeAccountVerified: false,
			reasonCode: "account_read_inconclusive",
			retryable: true,
			nextRetryAt: "2026-09-09T10:00:01Z",
		},
	};
	getMock.mockResolvedValue({ data: degraded });
	postMock.mockImplementation((path: string) => path === "/api/v1/agents/agy/accounts/ensure"
		? Promise.resolve({ data: degraded })
		: Promise.resolve({ data: {} }));
	const { container } = renderSection();

	expect((await screen.findAllByText("Couldn’t refresh the Antigravity account.")).length).toBeGreaterThan(0);
	expect(screen.getByRole("button", { name: "Try again" })).toBeEnabled();
	expect(screen.getByText("active@example.com")).toBeInTheDocument();
	expect(screen.getByText("other@example.com")).toBeInTheDocument();
	expect(screen.queryByText("In use")).not.toBeInTheDocument();
	expect(screen.getByRole("button", { name: "Add account" })).toBeEnabled();
	expect(screen.queryByRole("button", { name: "Switch account" })).not.toBeInTheDocument();

	const previouslyActiveRow = container.querySelector(`[data-account-id="${activeAccount.id}"]`) as HTMLElement;
	fireEvent.click(previouslyActiveRow.querySelector('button[aria-expanded="false"]') as HTMLButtonElement);
	expect(within(previouslyActiveRow).getByRole("button", { name: "Use this account active@example.com" })).toBeEnabled();

	const inactiveRow = container.querySelector(`[data-account-id="${inactiveAccount.id}"]`) as HTMLElement;
	fireEvent.click(inactiveRow.querySelector('button[aria-expanded="false"]') as HTMLButtonElement);
});

it("retries an inconclusive sign-in check without opening the login terminal", async () => {
	const unknownAccount = {
		...activeAccount,
		authentication: {
			...authentication,
			state: "unknown",
			freshness: "stale",
			reasonCode: "auth_check_failed",
			reason: "Authentication check failed.",
		},
	};
	const unknownResponse = { ...accountResponse, accounts: [unknownAccount, inactiveAccount] };
	getMock.mockResolvedValue({ data: unknownResponse });
	postMock.mockImplementation((path: string, request?: { body?: { forceAuthentication?: boolean } }) => {
		if (path !== "/api/v1/agents/agy/accounts/ensure") return Promise.resolve({ data: {} });
		return Promise.resolve({ data: request?.body?.forceAuthentication ? accountResponse : unknownResponse });
	});
	const { container } = renderSection();

	expect((await screen.findAllByText("Couldn’t verify sign-in.")).length).toBeGreaterThan(0);
	expect(screen.queryByText("Authentication unknown")).not.toBeInTheDocument();
	const row = container.querySelector(`[data-account-id="${activeAccount.id}"]`) as HTMLElement;
	fireEvent.click(within(row).getByRole("button", { name: /active@example.com/i }));
	fireEvent.click(within(row).getAllByRole("button", { name: "Try again" })[0]);

	await waitFor(() => expect(postMock).toHaveBeenCalledWith(
		"/api/v1/agents/agy/accounts/ensure",
		{ body: { accountIds: [activeAccount.id], forceAuthentication: true } },
	));
	expect(screen.queryByRole("button", { name: "Antigravity sign-in" })).not.toBeInTheDocument();
	expect((await screen.findAllByText("Signed in")).length).toBeGreaterThan(0);
});

it("removes the local reconciliation error silently when it recovers", async () => {
	const degraded = {
		...accountResponse,
		deviceReconciliation: {
			status: "temporarily_unavailable",
			activeAccountVerified: false,
			reasonCode: "account_read_inconclusive",
			retryable: true,
		},
	};
	getMock.mockResolvedValue({ data: degraded });
	postMock.mockResolvedValue({ data: degraded });
	const { queryClient } = renderSection();
	await screen.findAllByText("Couldn’t refresh the Antigravity account.");

	act(() => writeAgyAccounts(queryClient, accountResponse as unknown as AgyAccountsResponse));

	await waitFor(() => expect(screen.queryByText("Couldn’t refresh the Antigravity account.")).not.toBeInTheDocument());
	expect(screen.queryByText("Antigravity account refreshed.")).not.toBeInTheDocument();
	expect(screen.getByText("In use")).toBeInTheDocument();
});

it("does not offer a retry for a permanently blocked device check", async () => {
	const blocked = {
		...accountResponse,
		deviceReconciliation: {
			status: "blocked",
			activeAccountVerified: false,
			reasonCode: "account_discovery_unavailable",
			retryable: false,
		},
	};
	getMock.mockResolvedValue({ data: blocked });
	renderSection();

	expect((await screen.findAllByText("Couldn’t refresh the Antigravity account.")).length).toBeGreaterThan(0);
	expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
});

it("uses the durable switch result when reconciliation briefly has no active account", async () => {
	const switchingResponse = {
		...accountResponse,
		currentSwitch: {
			id: "33333333-3333-4333-8333-333333333333",
			phase: "activating_target",
			failureCode: undefined,
			sourceAccountId: activeAccount.id,
			sourceKind: "managed",
			targetAccountId: inactiveAccount.id,
			createdAt: "2026-08-31T10:00:00Z",
			updatedAt: "2026-08-31T10:01:00Z",
		},
	};
	const settledResponse = {
		...accountResponse,
		accountRevision: 4,
		activeAccountId: inactiveAccount.id,
		accounts: [{ ...inactiveAccount, active: true }, { ...activeAccount, active: false }],
	};
	const reconciliationSnapshot = {
		...accountResponse,
		activeAccountId: undefined,
		currentSwitch: undefined,
		deviceReconciliation: { status: "checking", activeAccountVerified: false, reasonCode: "checking", retryable: false },
	};
	getMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/account-switches/{switchId}") {
			return Promise.resolve({ data: { ...switchingResponse.currentSwitch, phase: "completed" } });
		}
		return Promise.resolve({ data: switchingResponse });
	});
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/accounts/ensure") {
			return Promise.resolve({ data: settledResponse });
		}
		return Promise.resolve({ data: switchingResponse });
	});
	const { queryClient } = renderSection();
	await screen.findByLabelText("Switching to other@example.com…");

	act(() => queryClient.setQueryData(["agy-accounts"], reconciliationSnapshot));

	const outcome = await screen.findByRole("status");
	expect(outcome).toHaveTextContent("Switched to other@example.com.");
	expect(outcome).not.toHaveTextContent("Couldn't switch accounts.");
	expect(outcome).toHaveAttribute("aria-live", "polite");
	expect(outcome).toBeVisible();
	expect(getMock).toHaveBeenCalledWith("/api/v1/agents/agy/account-switches/{switchId}", {
		params: { path: { switchId: switchingResponse.currentSwitch.id } },
	});
	await waitFor(() => expect(postMock).toHaveBeenCalledWith(
		"/api/v1/agents/agy/accounts/ensure",
		{ body: { accountIds: [inactiveAccount.id]} },
	));
});

it("reports when a failed switch leaves the previous account unchanged", async () => {
	const switchingResponse = {
		...accountResponse,
		currentSwitch: {
			id: "33333333-3333-4333-8333-333333333333",
			phase: "activating_target",
			failureCode: "activation_failed",
			sourceAccountId: activeAccount.id,
			sourceKind: "managed",
			targetAccountId: inactiveAccount.id,
			createdAt: "2026-08-31T10:00:00Z",
			updatedAt: "2026-08-31T10:01:00Z",
		},
	};
	getMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/account-switches/{switchId}") {
			return Promise.resolve({ data: { ...switchingResponse.currentSwitch, phase: "failed" } });
		}
		return Promise.resolve({ data: switchingResponse });
	});
	postMock.mockResolvedValue({ data: switchingResponse });
	const { queryClient } = renderSection();
	await screen.findByLabelText("Switching to other@example.com…");

	act(() => queryClient.setQueryData(["agy-accounts"], {
		...accountResponse,
		accountRevision: 4,
		currentSwitch: undefined,
	}));

	const outcome = await screen.findByRole("status");
	expect(outcome).toHaveTextContent("Couldn't switch accounts. You're still using active@example.com.");
	expect(outcome).toHaveAttribute("aria-live", "polite");
	expect(outcome).toBeVisible();
});

it("uses safe fallback headings and preserves stale values without exposing raw limit ids", async () => {
	const staleAccount = {
		...activeAccount,
		capacity: {
			...capacity,
			freshness: "stale",
			reasonCode: "capacity_provider_rejected",
			reason: "raw provider text must not be rendered",
			checkedAt: "2026-08-31T10:00:00Z",
			overall: null,
			additionalBuckets: [{
				limitId: "provider-secret-bucket-id",
				reached: "not_reached",
				primary: { usedPercent: 75, windowDurationMinutes: 60, resetsAt: null },
			}],
		},
	};
	const staleResponse = { ...accountResponse, accounts: [staleAccount] };
	getMock.mockResolvedValue({ data: staleResponse });
	postMock.mockResolvedValue({ data: staleResponse });
	const { container } = renderSection();
	await screen.findByText("active@example.com");
	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);

	expect(await screen.findByText("Additional usage limits")).toBeInTheDocument();
	expect(screen.queryByText("provider-secret-bucket-id")).not.toBeInTheDocument();
	expect(screen.getByRole("status")).toHaveTextContent(/Last updated/);
	expect(screen.getByRole("status")).not.toHaveTextContent("raw provider text");
	expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "25");
});

it("shows a safe provider-unavailable reason when no previous usage limits exist", async () => {
	const unavailableAccount = {
		...activeAccount,
		capacity: {
			...capacity,
			state: "unknown",
			freshness: "stale",
			remainingPercent: null,
			reasonCode: "capacity_provider_unavailable",
			reason: "raw transport error must not be rendered",
			checkedAt: null,
			overall: null,
			additionalBuckets: [],
		},
	};
	const unavailableResponse = { ...accountResponse, accounts: [unavailableAccount] };
	getMock.mockResolvedValue({ data: unavailableResponse });
	postMock.mockResolvedValue({ data: unavailableResponse });
	const { container } = renderSection();
	expect((await screen.findAllByText("active@example.com")).length).toBeGreaterThan(0);
	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);

	expect(await screen.findByText("Usage unavailable.")).toBeInTheDocument();
	expect(screen.getByRole("button", { name: "Try again" })).toBeEnabled();
	expect(screen.queryByText("raw transport error")).not.toBeInTheDocument();
});

it("quietly refreshes invalidated capacity without showing an internal warning", async () => {
	const invalidatedAccount = {
		...activeAccount,
		capacity: {
			...capacity,
			state: "unknown",
			freshness: "stale",
			remainingPercent: null,
			reasonCode: "capacity_invalidated",
			reason: "internal invalidation detail",
			overall: null,
			additionalBuckets: [],
		},
		usageSummary: {
			lifetimeTokens: 62700000000,
			peakDailyTokens: 2000000000,
			longestRunningTurnSeconds: 26340,
			currentStreakDays: 2,
			longestStreakDays: 99,
			observedAt: "2026-08-31T10:00:00Z",
		},
	};
	const invalidatedResponse = { ...accountResponse, accounts: [invalidatedAccount] };
	getMock.mockResolvedValue({ data: invalidatedResponse });
	postMock.mockResolvedValue({ data: invalidatedResponse });
	const { container } = renderSection();
	await screen.findAllByText("active@example.com");
	fireEvent.click(container.querySelector(`[data-account-id="${activeAccount.id}"] button`) as HTMLButtonElement);

	expect(screen.queryByText("Usage capacity changed and must be checked again.")).not.toBeInTheDocument();
	expect(screen.queryByText("internal invalidation detail")).not.toBeInTheDocument();
});

it("collapses the provider while rotating only its chevron", async () => {
	renderSection();
	await screen.findByText("active@example.com");
	const providerToggle = screen.getByRole("button", { name: /Antigravity/ });
	const icon = providerToggle.querySelector("img");
	const chevron = providerToggle.querySelector("svg");
	expect(icon).not.toBeNull();
	expect(chevron).not.toBeNull();
	fireEvent.click(providerToggle);
	expect(screen.queryByText("active@example.com")).not.toBeInTheDocument();
	expect(icon?.getAttribute("class")).not.toContain("rotate");
	expect(chevron?.getAttribute("class")).toContain("rotate");
});

it("starts account login immediately with no name prompt and auto-scrolls the inline terminal", async () => {
	renderSection();
	await screen.findByText("active@example.com");
	const addButton = screen.getByRole("button", { name: "Add account" });
	expect(addButton.textContent).toBe("");
	fireEvent.click(addButton);
	await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/agents/agy/accounts/login-terminal"));
	expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
	expect(await screen.findByTestId("inline-terminal-body")).toBeInTheDocument();
	expect(scrollIntoViewMock).toHaveBeenCalledWith({ behavior: "smooth", block: "nearest" });
	expect(useUiStore.getState().settingsModal).toEqual({ scope: "global", section: "agents" });
	expect(screen.getByRole("button", { name: "Add account" })).toBeDisabled();
});

it("reattaches a daemon-projected login terminal across remounts without opening or cancelling it", async () => {
	const activeLogin = {
		operationId: "login-rehydrate",
		status: "pending",
		reasonCode: "login_pending",
		reason: "private daemon copy",
		expiresAt: "2026-08-31T10:15:00Z",
		shellTerminal: { handleId: "shellterm-rehydrate", title: "Existing sign-in", createdAt: "2026-08-31T10:00:00Z" },
	};
	const projected = { ...accountResponse, activeLogin };
	getMock.mockResolvedValue({ data: projected });
	postMock.mockImplementation((path: string) => path === "/api/v1/agents/agy/accounts/ensure" ? Promise.resolve({ data: projected }) : Promise.resolve({ data: {} }));
	const first = renderSection();
	await screen.findByTestId("inline-terminal-body");
	expect(terminalTarget.value).toMatchObject({ handleId: "shellterm-rehydrate", generation: "2026-08-31T10:00:00Z", title: "Existing sign-in" });
	expect(postMock.mock.calls.filter(([path]) => String(path).includes("login-terminal") || String(path).includes("/cancel"))).toHaveLength(0);
	first.unmount();
	renderSection();
	await screen.findByTestId("inline-terminal-body");
	expect(terminalTarget.value?.handleId).toBe("shellterm-rehydrate");
	expect(postMock.mock.calls.filter(([path]) => String(path).includes("login-terminal") || String(path).includes("/cancel"))).toHaveLength(0);
});

it("surfaces account-service unavailability instead of loading forever", async () => {
	const unavailable = new Error("Antigravity account management is unavailable");
	getMock.mockResolvedValue({ error: unavailable });
	postMock.mockResolvedValue({ error: unavailable });
	renderSection();

	expect((await screen.findAllByText("Antigravity account management is unavailable", {}, { timeout: 3_000 })).length).toBeGreaterThan(0);
	expect(screen.queryByText("Loading Antigravity accounts…")).not.toBeInTheDocument();
	expect(screen.getByRole("button", { name: "Add account" })).toBeDisabled();
});

it("verifies exactly once on terminal exit and collapses after structured success", async () => {
	const completedAccount = { ...inactiveAccount, id: "33333333-3333-4333-8333-333333333333", label: "new@example.com", accountEmail: "new@example.com" };
	// The verified operation is enough to update the card immediately. A
	// follow-up cached-list refresh is best-effort and must not keep the dead
	// terminal open when it fails.
	getMock.mockResolvedValueOnce({ data: accountResponse }).mockRejectedValue(new Error("refresh unavailable"));
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/accounts/ensure") return Promise.resolve({ data: accountResponse });
		if (path === "/api/v1/agents/agy/accounts/login-terminal") return Promise.resolve({ data: pendingLogin });
		if (path.includes("/verify")) return Promise.resolve({ data: { ...pendingLogin.operation, status: "completed", reasonCode: "login_completed", reason: "Antigravity account added.", account: completedAccount } });
		return Promise.resolve({ data: {} });
	});
	renderSection();
	await screen.findByText("active@example.com");
	fireEvent.click(screen.getByRole("button", { name: "Add account" }));
	await screen.findByTestId("inline-terminal-body");
	act(() => terminalStateCallback.value?.("exited"));
	await waitFor(() => expect(postMock.mock.calls.filter(([path]) => String(path).includes("/verify"))).toHaveLength(1));
	await waitFor(() => expect(screen.queryByTestId("inline-terminal-body")).not.toBeInTheDocument());
	expect(await screen.findByText("new@example.com")).toBeInTheDocument();
});

it("retains terminal output when verification is unauthorized", async () => {
	const unauthorizedLogin = {
		...pendingLogin,
		operation: { ...pendingLogin.operation, operationId: "login-unauthorized" },
		shellTerminal: { ...pendingLogin.shellTerminal, handleId: "shellterm-login-unauthorized" },
	};
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/accounts/ensure") return Promise.resolve({ data: accountResponse });
		if (path === "/api/v1/agents/agy/accounts/login-terminal") return Promise.resolve({ data: unauthorizedLogin });
		if (path.includes("/verify")) return Promise.resolve({ data: { ...unauthorizedLogin.operation, status: "unauthorized", reasonCode: "login_unauthorized", reason: "Antigravity is still signed out." } });
		return Promise.resolve({ data: {} });
	});
	renderSection();
	await screen.findByText("active@example.com");
	fireEvent.click(screen.getByRole("button", { name: "Add account" }));
	await screen.findByTestId("inline-terminal-body");
	act(() => terminalStateCallback.value?.("exited"));
	expect(await screen.findByRole("button", { name: "Retry" })).toBeEnabled();
	expect(screen.getByTestId("inline-terminal-body")).toBeInTheDocument();
});

it("signs in again inline and replaces the existing account card", async () => {
	const signedOutAccount = {
		...inactiveAccount,
		status: "signed_out",
		reasonCode: "account_signed_out",
		reason: "This Antigravity account is signed out.",
		authentication: { ...authentication, state: "unauthorized", reasonCode: "unauthorized", reason: "Sign in again to use this Antigravity account." },
	};
	const signedOutResponse = { ...accountResponse, accounts: [activeAccount, signedOutAccount] };
	const restoredAccount = { ...signedOutAccount, status: "valid", reasonCode: "account_valid", reason: "Available.", authentication };
	const restoredResponse = { ...accountResponse, accounts: [activeAccount, restoredAccount] };
	const reauthentication = {
		...pendingLogin,
		operation: { ...pendingLogin.operation, operationId: "reauth-1", accountId: signedOutAccount.id },
		shellTerminal: { ...pendingLogin.shellTerminal, handleId: "shellterm-reauth-1", title: "Sign in to Antigravity account" },
	};
	getMock.mockResolvedValueOnce({ data: signedOutResponse }).mockResolvedValue({ data: restoredResponse });
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/accounts/ensure") return Promise.resolve({ data: signedOutResponse });
		if (path === "/api/v1/agents/agy/accounts/{accountId}/login-terminal") return Promise.resolve({ data: reauthentication });
		if (path.includes("/verify")) return Promise.resolve({ data: { ...reauthentication.operation, status: "completed", reasonCode: "login_completed", reason: "Antigravity account signed in.", account: restoredAccount } });
		return Promise.resolve({ data: {} });
	});
	const { container } = renderSection();
	await screen.findByText("other@example.com");
	const signedOutRow = container.querySelector(`[data-account-id="${signedOutAccount.id}"]`) as HTMLElement;
	expect(within(signedOutRow).queryByRole("button", { name: /other@example.com/i })).not.toBeInTheDocument();
	expect(within(signedOutRow).queryByText("ChatGPT")).not.toBeInTheDocument();
	const signInButton = within(signedOutRow).getByRole("button", { name: "Sign in again" });
	const deleteButton = within(signedOutRow).getByRole("button", { name: "Delete account" });
	expect(signInButton).toBeEnabled();
	expect(deleteButton.textContent).toBe("");
	fireEvent.click(signInButton);
	await waitFor(() => expect(postMock).toHaveBeenCalledWith(
		"/api/v1/agents/agy/accounts/{accountId}/login-terminal",
		{ params: { path: { accountId: signedOutAccount.id } } },
	));
	expect(await screen.findByTestId("inline-terminal-body")).toBeInTheDocument();
	expect(screen.getByTestId("agy-account-login-terminal")).toBeInTheDocument();
	act(() => terminalStateCallback.value?.("exited"));
	await waitFor(() => expect(screen.queryByTestId("inline-terminal-body")).not.toBeInTheDocument());
	expect(container.querySelectorAll(`[data-account-id="${signedOutAccount.id}"]`)).toHaveLength(1);
	expect(within(container.querySelector(`[data-account-id="${signedOutAccount.id}"]`) as HTMLElement).getByText("Signed in")).toBeInTheDocument();
});

it("deletes a signed-out account after confirmation", async () => {
	const signedOutAccount = {
		...inactiveAccount,
		status: "signed_out",
		reasonCode: "account_signed_out",
		reason: "This Antigravity account is signed out.",
		authentication: { ...authentication, state: "unauthorized", reasonCode: "unauthorized", reason: "Sign in again to use this Antigravity account." },
	};
	const signedOutResponse = { ...accountResponse, accounts: [activeAccount, signedOutAccount] };
	const deletedResponse = { ...accountResponse, accounts: [activeAccount] };
	getMock.mockResolvedValue({ data: signedOutResponse });
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/accounts/ensure") return Promise.resolve({ data: signedOutResponse });
		return Promise.resolve({ data: {} });
	});
	deleteMock.mockResolvedValue({ data: deletedResponse });

	const { container } = renderSection();
	await screen.findByText("other@example.com");
	const signedOutRow = container.querySelector(`[data-account-id="${signedOutAccount.id}"]`) as HTMLElement;
	expect(screen.queryByText("Login expired.")).not.toBeInTheDocument();
	expect(screen.queryByText("Usage unavailable.")).not.toBeInTheDocument();
	expect(within(signedOutRow).queryByText("ChatGPT")).not.toBeInTheDocument();
	const deleteButton = within(signedOutRow).getByRole("button", { name: "Delete account" });
	expect(deleteButton).toBeEnabled();
	expect(deleteButton.textContent).toBe("");
	fireEvent.click(deleteButton);
	const dialog = await screen.findByRole("dialog");
	expect(dialog).toHaveTextContent("Delete this Antigravity account?");
	fireEvent.click(within(dialog).getByRole("button", { name: "Delete account" }));

	await waitFor(() => expect(deleteMock).toHaveBeenCalledWith(
		"/api/v1/agents/agy/accounts/{accountId}",
		{ params: { path: { accountId: signedOutAccount.id } } },
	));
	await waitFor(() => expect(screen.queryByText("other@example.com")).not.toBeInTheDocument());
	expect(screen.getByText("active@example.com")).toBeInTheDocument();
});

it("deletes an invalid sign-in with one daemon-owned request", async () => {
	const invalidAuthentication = { ...authentication, state: "unauthorized", reasonCode: "unauthorized", reason: "Antigravity needs authentication." };
	const invalidAccount = {
		...activeAccount,
		authentication: invalidAuthentication,
		capacity: { ...capacity, state: "unknown", plan: null, usedPercent: null, remainingPercent: null, overall: null },
	};
	const invalidResponse = { ...accountResponse, accounts: [invalidAccount, inactiveAccount] };
	const deletedResponse = { ...accountResponse, activeAccountId: undefined, accountRevision: 4, accounts: [inactiveAccount] };
	getMock.mockResolvedValue({ data: invalidResponse });
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/accounts/ensure") return Promise.resolve({ data: invalidResponse });
		return Promise.resolve({ data: {} });
	});
	deleteMock.mockResolvedValue({ data: deletedResponse });

	const { container } = renderSection();
	await screen.findByText("active@example.com");
	const invalidRow = container.querySelector(`[data-account-id="${invalidAccount.id}"]`) as HTMLElement;
	expect((await screen.findAllByText("Login expired.")).length).toBeGreaterThan(0);
	expect(screen.queryByText("Antigravity reports this account as signed out.")).not.toBeInTheDocument();
	expect(within(invalidRow).queryByText("ChatGPT")).not.toBeInTheDocument();
	expect(within(invalidRow).queryByRole("button", { name: /active@example.com/i })).not.toBeInTheDocument();
	fireEvent.click(within(invalidRow).getByRole("button", { name: "Delete account" }));
	const dialog = await screen.findByRole("dialog");
	fireEvent.click(within(dialog).getByRole("button", { name: "Delete account" }));

	await waitFor(() => expect(deleteMock).toHaveBeenCalledWith(
		"/api/v1/agents/agy/accounts/{accountId}",
		{ params: { path: { accountId: invalidAccount.id } } },
	));
	expect(postMock).not.toHaveBeenCalledWith(
		"/api/v1/agents/agy/accounts/{accountId}/logout",
		expect.anything(),
	);
	await waitFor(() => expect(screen.queryByText("active@example.com")).not.toBeInTheDocument());
});

it("starts a global switch without revision admission", async () => {
	const switchOperation = { id: "switch-1", phase: "requested", failureCode: null };
	vi.stubGlobal("crypto", { randomUUID: () => "idempotency-1" });
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/accounts/ensure") return Promise.resolve({ data: accountResponse });
		if (path === "/api/v1/agents/agy/account-switches") return Promise.resolve({ data: switchOperation });
		return Promise.resolve({ data: pendingLogin });
	});
	renderSection();
	await screen.findByText("other@example.com");
	expect(screen.queryByRole("button", { name: "Switch to this account" })).not.toBeInTheDocument();
	await userEvent.click(screen.getByRole("button", { name: "Switch account" }));
	await userEvent.click(await screen.findByRole("menuitem", { name: /other@example.com/ }));
	const dialog = await screen.findByRole("dialog");
	expect(dialog).toHaveTextContent("Switch to other@example.com?");
	expect(dialog).toHaveTextContent("New AO sessions will use this account.");
	expect(dialog).not.toHaveTextContent("external terminals, IDEs, and ChatGPT");
	fireEvent.click(within(dialog).getByRole("button", { name: "Switch account" }));
	await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/agents/agy/account-switches", {
		body: { targetAccountId: inactiveAccount.id, idempotencyKey: "idempotency-1" },
	}));
	vi.unstubAllGlobals();
});

it("keeps a locally valid target switchable during a temporary sign-in check failure", async () => {
	const temporarilyUnverified = {
		...inactiveAccount,
		authentication: {
			...authentication,
			state: "unknown",
			freshness: "stale",
			reasonCode: "auth_check_failed",
			reason: "Could not reach Antigravity.",
		},
	};
	const response = { ...accountResponse, accounts: [activeAccount, temporarilyUnverified] };
	getMock.mockResolvedValue({ data: response });
	postMock.mockImplementation((path: string) => path === "/api/v1/agents/agy/accounts/ensure"
		? Promise.resolve({ data: response })
		: Promise.resolve({ data: {} }));

	renderSection();
	await screen.findByText("other@example.com");
	await userEvent.click(screen.getByRole("button", { name: "Switch account" }));
	expect(await screen.findByRole("menuitem", { name: /other@example.com/ })).toBeEnabled();
});

it("locks the switch confirmation while the request is submitted", async () => {
	vi.stubGlobal("crypto", { randomUUID: () => "switch-idempotency" });
	let finishSwitch: ((value: { data: object }) => void) | undefined;
	postMock.mockImplementation((path: string) => {
		if (path === "/api/v1/agents/agy/accounts/ensure") return Promise.resolve({ data: accountResponse });
		if (path === "/api/v1/agents/agy/account-switches") return new Promise((resolve) => { finishSwitch = resolve; });
		return Promise.resolve({ data: pendingLogin });
	});
	renderSection();
	await screen.findByText("other@example.com");

	const openSwitchDialog = async () => {
		await userEvent.click(screen.getByRole("button", { name: "Switch account" }));
		await userEvent.click(await screen.findByRole("menuitem", { name: /other@example.com/ }));
		return screen.findByRole("dialog");
	};

	const dialog = await openSwitchDialog();
	await userEvent.click(within(dialog).getByRole("button", { name: "Switch account" }));
	await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/agents/agy/account-switches", {
		body: { targetAccountId: inactiveAccount.id, idempotencyKey: "switch-idempotency" },
	}));
	expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeDisabled();
	expect(within(dialog).getByRole("button", { name: "Switch account" })).toBeDisabled();

	finishSwitch?.({ data: {} });
	await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
	vi.unstubAllGlobals();
});

const unauthorizedAuthentication = { ...authentication, state: "unauthorized", reasonCode: "unauthorized", reason: "Antigravity needs authentication." };
const launchFailureResponse = {
	...accountResponse,
	accounts: [{ ...activeAccount, authentication: unauthorizedAuthentication }, inactiveAccount],
};

it("shows the active account's reauthentication state and CTA as soon as a failed launch publishes it", async () => {
	const { container, queryClient } = renderSection();
	expect(await screen.findByText("active@example.com · 96% remaining")).toBeInTheDocument();
	const activeRow = container.querySelector(`[data-account-id="${activeAccount.id}"]`) as HTMLElement;
	fireEvent.click(activeRow.querySelector("button") as HTMLButtonElement);
	expect(await within(activeRow).findByText("Signed in")).toBeInTheDocument();
	const reads = getMock.mock.calls.length;

	// The daemon publishes the account event the rejected spawn produced.
	act(() => { writeAgyAccounts(queryClient, launchFailureResponse as never, "replace"); });

	expect(await screen.findByRole("button", { name: "Sign in again" })).toBeInTheDocument();
	expect(within(activeRow).queryByText("Signed in")).not.toBeInTheDocument();
	expect(screen.getByText("active@example.com · Login expired.")).toBeInTheDocument();
	expect(getMock.mock.calls.length).toBe(reads);
});

it("does not let an authorized inactive account mask the active account's reauthentication", async () => {
	getMock.mockResolvedValue({ data: launchFailureResponse });
	postMock.mockImplementation((path: string) => path === "/api/v1/agents/agy/accounts/ensure" ? Promise.resolve({ data: launchFailureResponse }) : Promise.resolve({ data: {} }));
	const { container } = renderSection();

	expect(await screen.findByText("active@example.com · Login expired.")).toBeInTheDocument();
	const activeRow = container.querySelector(`[data-account-id="${activeAccount.id}"]`) as HTMLElement;
	const inactiveRow = container.querySelector(`[data-account-id="${inactiveAccount.id}"]`) as HTMLElement;
	expect(within(activeRow).getByText("Login expired.")).toBeInTheDocument();
	expect(within(inactiveRow).getByText("Signed in")).toBeInTheDocument();
	expect(within(activeRow).queryByText("Signed in")).not.toBeInTheDocument();
});

it("restores the signed-in state after a successful reauthentication", async () => {
	getMock.mockResolvedValue({ data: launchFailureResponse });
	postMock.mockImplementation((path: string) => path === "/api/v1/agents/agy/accounts/ensure" ? Promise.resolve({ data: launchFailureResponse }) : Promise.resolve({ data: {} }));
	const { container, queryClient } = renderSection();
	expect(await screen.findByText("active@example.com · Login expired.")).toBeInTheDocument();
	expect(await screen.findByRole("button", { name: "Sign in again" })).toBeInTheDocument();

	act(() => { writeAgyAccounts(queryClient, accountResponse as never, "replace"); });

	const activeRow = container.querySelector(`[data-account-id="${activeAccount.id}"]`) as HTMLElement;
	const accountToggle = await within(activeRow).findByRole("button", { name: /active@example.com/i });
	fireEvent.click(accountToggle);
	expect(await within(activeRow).findByText("Signed in")).toBeInTheDocument();
	expect(screen.queryByRole("button", { name: "Sign in again" })).not.toBeInTheDocument();
	expect(screen.getByText("active@example.com · 96% remaining")).toBeInTheDocument();
});
