import { useEffect } from "react";
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { agyAccountsQueryKey, mergeAgyAccounts, writeAgyAccounts } from "./agy-accounts-state";

export { agyAccountsQueryKey } from "./agy-accounts-state";

export type AgyAccountsResponse = components["schemas"]["AgyAccountsResponse"];
export type AgyAccount = components["schemas"]["AgyAccountResponse"];
export type AgyAccountLoginOperation = components["schemas"]["AgyAccountLoginResponse"];
export type AgyAccountLoginTerminalStart = components["schemas"]["OpenAgyAccountLoginTerminalResponse"];
export type AgyActiveLogin = components["schemas"]["AgyActiveLoginResponse"];
export type AgyAccountSwitch = components["schemas"]["AgyAccountSwitchResponse"];

export async function fetchAgyAccounts(): Promise<AgyAccountsResponse> {
	const { data, error } = await apiClient.GET("/api/v1/agents/agy/accounts");
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyAccountsResponse;
}

export type EnsureAgyAccountsOptions = {
	forceAuthentication?: boolean;
	forceDeviceReconciliation?: boolean;
};

export async function ensureAgyAccounts(accountIds: string[] = [], options: EnsureAgyAccountsOptions = {}): Promise<AgyAccountsResponse> {
	const body = {
		accountIds,
		...(options.forceAuthentication ? { forceAuthentication: true } : {}),
		...(options.forceDeviceReconciliation ? { forceDeviceReconciliation: true } : {}),
	};
	const { data, error } = await apiClient.POST("/api/v1/agents/agy/accounts/ensure", { body });
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyAccountsResponse;
}

export async function openAgyAccountLoginTerminal(): Promise<AgyAccountLoginTerminalStart> {
	const { data, error } = await apiClient.POST("/api/v1/agents/agy/accounts/login-terminal");
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyAccountLoginTerminalStart;
}

export async function openAgyAccountReauthenticationTerminal(accountId: string): Promise<AgyAccountLoginTerminalStart> {
	const { data, error } = await apiClient.POST("/api/v1/agents/agy/accounts/{accountId}/login-terminal", {
		params: { path: { accountId } },
	});
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyAccountLoginTerminalStart;
}

export async function logoutAgyAccount(accountId: string): Promise<AgyAccountsResponse> {
	const { data, error } = await apiClient.POST("/api/v1/agents/agy/accounts/{accountId}/logout", {
		params: { path: { accountId } },
	});
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyAccountsResponse;
}

export async function deleteAgyAccount(accountId: string): Promise<AgyAccountsResponse> {
	const { data, error } = await apiClient.DELETE("/api/v1/agents/agy/accounts/{accountId}", {
		params: { path: { accountId } },
	});
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyAccountsResponse;
}

export async function verifyAgyAccountLogin(operationId: string): Promise<AgyAccountLoginOperation> {
	const { data, error } = await apiClient.POST("/api/v1/agents/agy/accounts/login-operations/{operationId}/verify", { params: { path: { operationId } } });
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyAccountLoginOperation;
}

export async function cancelAgyAccountLogin(operationId: string): Promise<AgyAccountLoginOperation> {
	const { data, error } = await apiClient.POST("/api/v1/agents/agy/accounts/login-operations/{operationId}/cancel", { params: { path: { operationId } } });
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyAccountLoginOperation;
}

export async function startAgyAccountSwitch(targetAccountId: string, idempotencyKey: string): Promise<AgyAccountSwitch> {
	const { data, error } = await apiClient.POST("/api/v1/agents/agy/account-switches", { body: { targetAccountId, idempotencyKey } });
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyAccountSwitch;
}

export async function fetchAgyAccountSwitch(switchId: string): Promise<AgyAccountSwitch> {
	const { data, error } = await apiClient.GET("/api/v1/agents/agy/account-switches/{switchId}", {
		params: { path: { switchId } },
	});
	if (error) throw new Error(apiErrorMessage(error));
	return data as AgyAccountSwitch;
}

export const agyAccountsQueryOptions = {
	queryKey: agyAccountsQueryKey,
	queryFn: async ({ client }: { client: QueryClient }) => {
		const incoming = await fetchAgyAccounts();
		return mergeAgyAccounts(client.getQueryData<AgyAccountsResponse>(agyAccountsQueryKey), incoming, "replace");
	},
	retry: 1,
	staleTime: Number.POSITIVE_INFINITY,
};
export function useAgyAccountsQuery(enabled = true) { return useQuery({ ...agyAccountsQueryOptions, enabled }); }

export function useEnsureAgyAccounts(): void {
	const queryClient = useQueryClient();
	useEffect(() => {
		let active = true;
		const ensure = () => {
			const cached = queryClient.getQueryData(agyAccountsQueryKey);
			const ready = cached ? Promise.resolve() : queryClient.fetchQuery(agyAccountsQueryOptions).then(() => undefined).catch(() => undefined);
			void ready.then(() => ensureAgyAccounts()).then((next) => { if (active) writeAgyAccounts(queryClient, next, "replace"); }).catch(() => undefined);
		};
		const onFocus = () => ensure();
		const onVisibility = () => { if (document.visibilityState === "visible") ensure(); };
		window.addEventListener("focus", onFocus); document.addEventListener("visibilitychange", onVisibility);
		return () => { active = false; window.removeEventListener("focus", onFocus); document.removeEventListener("visibilitychange", onVisibility); };
	}, [queryClient]);
}
