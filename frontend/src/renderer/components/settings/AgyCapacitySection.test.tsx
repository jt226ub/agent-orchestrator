import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { TooltipProvider } from "../ui/tooltip";
import { AgyCapacitySection } from "./AgyCapacitySection";

const { getMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, POST: postMock },
	apiErrorMessage: (error: unknown) => error instanceof Error ? error.message : "request failed",
}));

const window5h = { usedPercent: 1.7, windowDurationMinutes: 300, resetsAt: "2026-09-17T17:43:35Z" };
const windowWeekly = { usedPercent: 24.1, windowDurationMinutes: 10080, resetsAt: "2026-09-23T14:16:19Z" };
const capacity = {
	state: "available", freshness: "fresh", usedPercent: 24.1, remainingPercent: 75.9, resetsAt: "2026-09-23T14:16:19Z",
	observedAt: "2026-09-17T15:45:00Z", checkedAt: "2026-09-17T15:45:00Z", attemptedAt: "2026-09-17T15:45:00Z",
	reasonCode: "capacity_available", reason: "available",
	overall: { displayName: "Gemini Models", primary: window5h, secondary: windowWeekly },
	additionalBuckets: [{ displayName: "Claude and GPT models", primary: { usedPercent: 0, windowDurationMinutes: 300, resetsAt: null }, secondary: { usedPercent: 0, windowDurationMinutes: 10080, resetsAt: null } }],
};

function renderSection() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return { queryClient, ...render(<QueryClientProvider client={queryClient}><TooltipProvider><AgyCapacitySection /></TooltipProvider></QueryClientProvider>) };
}

beforeEach(() => {
	getMock.mockReset().mockResolvedValue({ data: capacity });
	postMock.mockReset().mockResolvedValue({ data: capacity });
});

it("renders every model group's windows and the remaining percentage", async () => {
	renderSection();
	expect(await screen.findByText("Gemini Models usage limits")).toBeTruthy();
	expect(screen.getByText("Claude and GPT models usage limits")).toBeTruthy();
	expect(screen.getAllByText("5-hour usage limit")).toHaveLength(2);
	expect(screen.getAllByText("Weekly usage limit")).toHaveLength(2);
	expect(screen.getByRole("progressbar", { name: "Weekly usage limit, 75.9% left" })).toBeTruthy();
	expect(screen.getAllByText("75.9% left").length).toBeGreaterThan(0);
	await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/agents/agy/capacity/ensure", { body: {} }));
});

it("refreshes with force and shows the refreshed snapshot", async () => {
	renderSection();
	await screen.findByText("Gemini Models usage limits");
	postMock.mockResolvedValueOnce({ data: { ...capacity, state: "near_limit", reasonCode: "capacity_near_limit", remainingPercent: 20, overall: { ...capacity.overall, secondary: { ...windowWeekly, usedPercent: 80 } } } });
	fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
	await waitFor(() => expect(postMock).toHaveBeenCalledWith("/api/v1/agents/agy/capacity/ensure", { body: { force: true } }));
	expect(await screen.findByText("Plan capacity is near its limit.")).toBeTruthy();
});

it("explains a signed-out CLI instead of drawing bars", async () => {
	const signedOut = { ...capacity, state: "unknown", freshness: "stale", usedPercent: null, remainingPercent: null, resetsAt: null, checkedAt: null, reasonCode: "capacity_skipped_signed_out", reason: "signed out", overall: null, additionalBuckets: [] };
	getMock.mockResolvedValue({ data: signedOut });
	postMock.mockResolvedValue({ data: signedOut });
	renderSection();
	// The reason shows twice: as the provider summary and as the notice.
	expect((await screen.findAllByText("Sign in to Antigravity to see plan capacity.")).length).toBeGreaterThan(0);
	expect(screen.getByRole("status").textContent).toContain("Sign in to Antigravity to see plan capacity.");
	expect(screen.queryByRole("progressbar")).toBeNull();
});
