import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { TooltipProvider } from "../ui/tooltip";
import { DefaultProfilesSection } from "./DefaultProfilesSection";

const { getMock, putMock, deleteMock, postMock } = vi.hoisted(() => ({ getMock: vi.fn(), putMock: vi.fn(), deleteMock: vi.fn(), postMock: vi.fn() }));

vi.mock("../../lib/api-client", () => ({
	apiClient: { GET: getMock, PUT: putMock, DELETE: deleteMock, POST: postMock },
	apiErrorMessage: (error: unknown) => (error instanceof Error ? error.message : typeof error === "object" && error && "message" in error ? String((error as { message: unknown }).message) : "request failed"),
}));

const agents = { agents: [{ id: "agy", label: "Antigravity", state: "ready" }, { id: "claude-code", label: "Claude Code", state: "ready" }] };
const defaults = {
	profiles: {
		"flash-coder": { agent: "agy", agentConfig: { model: "gemini-3.8-flash-high" }, rulesFile: "flash-coder.md" },
	},
	rulesFiles: [{ name: "contract.md", sizeBytes: 1200, updatedAt: "2026-09-21T12:00:00Z" }],
};

function renderSection() {
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	return { queryClient, ...render(<QueryClientProvider client={queryClient}><TooltipProvider><DefaultProfilesSection /></TooltipProvider></QueryClientProvider>) };
}

beforeEach(() => {
	getMock.mockReset().mockImplementation((path: string, init?: { params?: { path?: { name?: string } } }) => {
		if (path === "/api/v1/settings/profiles") return Promise.resolve({ data: defaults, response: { status: 200 } });
		if (path === "/api/v1/settings/rules/{name}") {
			const name = init?.params?.path?.name;
			if (name === "contract.md") return Promise.resolve({ data: { name, content: "# Drive", updatedAt: "2026-09-21T12:00:00Z" }, response: { status: 200 } });
			return Promise.resolve({ error: { message: "not found" }, response: { status: 404 } });
		}
		if (path === "/api/v1/agents/readiness") return Promise.resolve({ data: agents, response: { status: 200 } });
		return Promise.resolve({ data: {}, response: { status: 200 } });
	});
	postMock.mockReset().mockResolvedValue({ data: agents });
	putMock.mockReset().mockImplementation((path: string, init: { body: Record<string, unknown>; params?: { path?: { name?: string } } }) => {
		if (path === "/api/v1/settings/profiles") return Promise.resolve({ data: { ...defaults, profiles: init.body.profiles } });
		return Promise.resolve({ data: { name: init.params?.path?.name, content: (init.body as { content: string }).content, updatedAt: "2026-09-21T12:05:00Z" } });
	});
	deleteMock.mockReset().mockResolvedValue({ data: undefined });
});

it("lists the default profiles and the rules files, fixed ones first", async () => {
	renderSection();
	expect(await screen.findByText("flash-coder")).toBeInTheDocument();
	const rows = screen.getAllByTestId("rules-file-row").map((row) => row.getAttribute("data-rules-file"));
	expect(rows).toEqual(["contract.md", "orchestrator.md", "worker.md", "flash-coder.md"]);
	expect(screen.getByText("contract.md · 1200 bytes")).toBeInTheDocument();
	expect(screen.getByText("worker.md · Not created yet")).toBeInTheDocument();
	expect(screen.getByText("Profile rules · flash-coder")).toBeInTheDocument();
});

it("adds a profile and saves the whole document through PUT", async () => {
	renderSection();
	await screen.findByText("flash-coder");
	fireEvent.click(screen.getByRole("button", { name: "Add profile" }));
	const card = screen.getAllByTestId("profile-card")[1];
	fireEvent.change(within(card).getByRole("textbox", { name: "Name of profile profile" }), { target: { value: "pro-expert" } });
	fireEvent.click(screen.getByRole("button", { name: "Save" }));
	await waitFor(() => expect(putMock).toHaveBeenCalledWith("/api/v1/settings/profiles", {
		body: { profiles: { "flash-coder": { agent: "agy", agentConfig: { model: "gemini-3.8-flash-high" }, rulesFile: "flash-coder.md" }, "pro-expert": {} } },
	}));
	expect(await screen.findByText("Saved.")).toBeInTheDocument();
});

it("refuses duplicate names before contacting the daemon", async () => {
	renderSection();
	await screen.findByText("flash-coder");
	fireEvent.click(screen.getByRole("button", { name: "Add profile" }));
	const card = screen.getAllByTestId("profile-card")[1];
	fireEvent.change(within(card).getByRole("textbox", { name: "Name of profile profile" }), { target: { value: "flash-coder" } });
	fireEvent.click(screen.getByRole("button", { name: "Save" }));
	expect(await screen.findByRole("alert")).toHaveTextContent("flash-coder");
	expect(putMock).not.toHaveBeenCalled();
});

it("edits a rules file in place and creates a missing one", async () => {
	renderSection();
	await screen.findByText("flash-coder");
	fireEvent.click(screen.getByRole("button", { name: "Edit contract.md" }));
	const editor = await screen.findByRole("textbox", { name: "Contents of contract.md" });
	await waitFor(() => expect(editor).toHaveValue("# Drive"));
	fireEvent.change(editor, { target: { value: "# Drive\n\nFinish or surface." } });
	fireEvent.click(screen.getByRole("button", { name: "Save file" }));
	await waitFor(() => expect(putMock).toHaveBeenCalledWith("/api/v1/settings/rules/{name}", { params: { path: { name: "contract.md" } }, body: { content: "# Drive\n\nFinish or surface." } }));
	await waitFor(() => expect(screen.queryByRole("textbox", { name: "Contents of contract.md" })).not.toBeInTheDocument());

	fireEvent.click(screen.getByRole("button", { name: "Edit worker.md" }));
	const fresh = await screen.findByRole("textbox", { name: "Contents of worker.md" });
	await waitFor(() => expect(fresh).not.toBeDisabled());
	expect(fresh).toHaveValue("");
	expect(screen.queryByRole("button", { name: "Delete worker.md" })).not.toBeInTheDocument();
	fireEvent.change(fresh, { target: { value: "Quote test output verbatim." } });
	fireEvent.click(screen.getByRole("button", { name: "Save file" }));
	await waitFor(() => expect(putMock).toHaveBeenCalledWith("/api/v1/settings/rules/{name}", { params: { path: { name: "worker.md" } }, body: { content: "Quote test output verbatim." } }));
});
