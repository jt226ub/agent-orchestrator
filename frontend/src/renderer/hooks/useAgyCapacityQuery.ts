import { useCallback, useEffect, useState } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { agyCapacityQueryKey } from "./agy-capacity-state";

export { agyCapacityQueryKey } from "./agy-capacity-state";

export type AgyCapacity = components["schemas"]["AgyCapacityResponse"];

export async function fetchAgyCapacity(): Promise<AgyCapacity> {
	const { data, error } = await apiClient.GET("/api/v1/agents/agy/capacity");
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyCapacity;
}

export async function ensureAgyCapacity(force = false): Promise<AgyCapacity> {
	const { data, error } = await apiClient.POST("/api/v1/agents/agy/capacity/ensure", { body: force ? { force: true } : {} });
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyCapacity;
}

export function writeAgyCapacity(queryClient: QueryClient, incoming: AgyCapacity): void {
	queryClient.setQueryData<AgyCapacity>(agyCapacityQueryKey, incoming);
}

export const agyCapacityQueryOptions = {
	queryKey: agyCapacityQueryKey,
	queryFn: fetchAgyCapacity,
	retry: 1,
	staleTime: Number.POSITIVE_INFINITY,
};
export function useAgyCapacityQuery(enabled = true) { return useQuery({ ...agyCapacityQueryOptions, enabled }); }

/**
 * Keeps the Antigravity capacity snapshot display-fresh while the caller is
 * mounted: one ensure on mount, then on window focus and visibility, the same
 * backup cadence the Codex accounts section uses.
 */
export function useEnsureAgyCapacity(): void {
	const queryClient = useQueryClient();
	useEffect(() => {
		let active = true;
		const ensure = () => {
			void ensureAgyCapacity().then((next) => { if (active) writeAgyCapacity(queryClient, next); }).catch(() => undefined);
		};
		ensure();
		const onFocus = () => ensure();
		const onVisibility = () => { if (document.visibilityState === "visible") ensure(); };
		window.addEventListener("focus", onFocus); document.addEventListener("visibilitychange", onVisibility);
		return () => { active = false; window.removeEventListener("focus", onFocus); document.removeEventListener("visibilitychange", onVisibility); };
	}, [queryClient]);
}

export function useRefreshAgyCapacity(): { refresh: () => Promise<void>; pending: boolean; error: string | null } {
	const queryClient = useQueryClient();
	const [pending, setPending] = useState(false);
	const [error, setError] = useState<string | null>(null);
	const refresh = useCallback(async () => {
		setPending(true);
		setError(null);
		try {
			writeAgyCapacity(queryClient, await ensureAgyCapacity(true));
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : String(cause));
		} finally {
			setPending(false);
		}
	}, [queryClient]);
	return { refresh, pending, error };
}
