import { useCallback, useEffect, useState } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { claudeCodeUsageQueryKey } from "./claude-code-usage-state";

export { claudeCodeUsageQueryKey } from "./claude-code-usage-state";

export type ClaudeCodeUsage = components["schemas"]["ClaudeCodeUsageResponse"];

export async function fetchClaudeCodeUsage(): Promise<ClaudeCodeUsage> {
	const { data, error } = await apiClient.GET("/api/v1/agents/claude-code/usage");
	if (error) throw new Error(apiErrorMessage(error));
	return data as ClaudeCodeUsage;
}

export async function ensureClaudeCodeUsage(force = false): Promise<ClaudeCodeUsage> {
	const { data, error } = await apiClient.POST("/api/v1/agents/claude-code/usage/ensure", { body: force ? { force: true } : {} });
	if (error) throw new Error(apiErrorMessage(error));
	return data as ClaudeCodeUsage;
}

export function writeClaudeCodeUsage(queryClient: QueryClient, incoming: ClaudeCodeUsage): void {
	queryClient.setQueryData<ClaudeCodeUsage>(claudeCodeUsageQueryKey, incoming);
}

export const claudeCodeUsageQueryOptions = {
	queryKey: claudeCodeUsageQueryKey,
	queryFn: fetchClaudeCodeUsage,
	retry: 1,
	staleTime: Number.POSITIVE_INFINITY,
};
export function useClaudeCodeUsageQuery(enabled = true) { return useQuery({ ...claudeCodeUsageQueryOptions, enabled }); }

/**
 * Keeps the Claude Code usage snapshot display-fresh while the caller is
 * mounted: one ensure on mount, then on window focus and visibility, the same
 * backup cadence the Codex accounts section uses.
 */
export function useEnsureClaudeCodeUsage(): void {
	const queryClient = useQueryClient();
	useEffect(() => {
		let active = true;
		const ensure = () => {
			void ensureClaudeCodeUsage().then((next) => { if (active) writeClaudeCodeUsage(queryClient, next); }).catch(() => undefined);
		};
		ensure();
		const onFocus = () => ensure();
		const onVisibility = () => { if (document.visibilityState === "visible") ensure(); };
		window.addEventListener("focus", onFocus); document.addEventListener("visibilitychange", onVisibility);
		return () => { active = false; window.removeEventListener("focus", onFocus); document.removeEventListener("visibilitychange", onVisibility); };
	}, [queryClient]);
}

export function useRefreshClaudeCodeUsage(): { refresh: () => Promise<void>; pending: boolean; error: string | null } {
	const queryClient = useQueryClient();
	const [pending, setPending] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const refresh = useCallback(async () => {
		setPending(true);
		setError(null);
		try {
			writeClaudeCodeUsage(queryClient, await ensureClaudeCodeUsage(true));
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			setPending(false);
		}
	}, [queryClient]);
	return { refresh, pending, error };
}
