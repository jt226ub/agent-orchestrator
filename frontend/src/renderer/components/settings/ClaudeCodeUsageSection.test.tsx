import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { TooltipProvider } from "../ui/tooltip";
import { ClaudeCodeUsageSection } from "./ClaudeCodeUsageSection";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed",
}));

const usage = {
	state: "available", freshness: "fresh", plan: "max",
	identity: { emailAddress: "dev@example.com", displayName: "Dev", organizationName: "Dev's Organization" },
	promotion: { percentIncrease: 50, endsOn: "2026-09-30" },
	remainingPercent: 58,
	windows: [
		{ id: "five_hour", displayName: "5-hour limit", usedPercent: 42, resetsAt: "2026-09-21T17:00:00Z" },
		{ id: "seven_day", displayName: "Weekly — all models", usedPercent: 12, resetsAt: "2026-09-28T00:00:00Z" },
		{ id: "seven_day_opus", displayName: "Weekly — Opus", usedPercent: 80, resetsAt: null },
	],
	observedAt: "2026-09-21T15:45:00Z", checkedAt: "2026-09-21T15:45:00Z", attemptedAt: "2026-09-21T15:45:00Z",
	reasonCode: "plan_usage_available", reason: "available",
};

function renderSection() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return { queryClient, ...render(<QueryClientProvider client={queryClient}><TooltipProvider><ClaudeCodeUsageSection /></TooltipProvider></QueryClientProvider>) };
}

beforeEach(() => {
	getMock.mockReset().mockResolvedValue({ data: usage });
	postMock.mockReset().mockResolvedValue({ data: usage });
});

it("renders the signed-in account, plan, promotion and every limit", async () => {
	renderSection();
	expect(await screen.findByText("Max plan")).toBeTruthy();
	expect(screen.getByText("dev@example.com · Dev's Organization")).toBeTruthy();
	expect(screen.getByText(/^\+50% weekly limits through /)).toBeTruthy();
	expect(screen.getByText("General usage limits")).toBeTruthy();
	expect(screen.getByText("Model usage limits")).toBeTruthy();
	expect(screen.getByRole("progressbar", { name: "5-hour limit, 58% left" })).toBeTruthy();
	expect(screen.getByRole("progressbar", { name: "Weekly — Opus, 20% left" })).toBeTruthy();
	// The provider summary carries the identity and the worst general window.
	expect(screen.getByText("dev@example.com · 58% left")).toBeTruthy();
	await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/agents/claude-code/usage/ensure", { body: {} }));
});

it("refreshes with force and shows the refreshed snapshot", async () => {
	renderSection();
	await screen.findByText("Max plan");
	postMock.mockResolvedValueOnce({ data: { ...usage, freshness: "stale", reasonCode: "plan_usage_rate_limited", reason: "rate limited" } });
	fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
	await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/agents/claude-code/usage/ensure", { body: { force: true } }));
	expect(await screen.findByText(/Usage information may be out of date\. Last checked/)).toBeTruthy();
});

it("explains a signed-out CLI instead of drawing bars", async () => {
	const signedOut = { ...usage, state: "unknown", freshness: "stale", plan: null, identity: null, promotion: null, remainingPercent: null, windows: [], checkedAt: null, reasonCode: "plan_usage_signed_out", reason: "signed out" };
	getMock.mockResolvedValue({ data: signedOut });
	postMock.mockResolvedValue({ data: signedOut });
	renderSection();
	// The reason shows twice: as the provider summary and as the notice.
	expect((await screen.findAllByText("Sign in to Claude Code to see plan usage.")).length).toBeGreaterThan(0);
	expect(screen.getByRole("status").textContent).toContain("Sign in to Claude Code to see plan usage.");
	expect(screen.queryByRole("progressbar")).toBeNull();
	expect(screen.getByText("Plan unavailable")).toBeTruthy();
});

it("hides the plan card when usage is unsupported on this device", async () => {
	const unsupported = { ...usage, state: "unsupported", freshness: "fresh", plan: null, identity: null, promotion: null, remainingPercent: null, windows: [], reasonCode: "plan_usage_unsupported", reason: "unsupported" };
	getMock.mockResolvedValue({ data: unsupported });
	postMock.mockResolvedValue({ data: unsupported });
	renderSection();
	expect((await screen.findAllByText("Plan usage is not available on this device.")).length).toBeGreaterThan(0);
	expect(screen.queryByText("Your plan")).toBeNull();
});
