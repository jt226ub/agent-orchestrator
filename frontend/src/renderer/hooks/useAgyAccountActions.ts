import { useCallback, useRef, useState } from "react";
import type { QueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { shellTerminalsQueryKey } from "./useShellTerminals";
import {
	cancelAgyAccountLogin,
	deleteAgyAccount,
	ensureAgyAccounts,
	fetchAgyAccountSwitch,
	openAgyAccountLoginTerminal,
	openAgyAccountReauthenticationTerminal,
	startAgyAccountSwitch,
	verifyAgyAccountLogin,
	type AgyAccount,
	type AgyActiveLogin,
	type AgyAccountsResponse,
} from "./useAgyAccountsQuery";
import { agyAccountsQueryKey, writeAgyAccounts } from "./agy-accounts-state";

function errorMessage(cause: unknown, fallback: string): string {
	return cause instanceof Error && cause.message ? cause.message : fallback;
}

export function useAgyAccountActions(queryClient: QueryClient) {
	const { t } = useTranslation();
	const [error, setError] = useState<string | null>(null);
	const [loginPending, setLoginPending] = useState(false);
	const [loginOperationPending, setLoginOperationPending] = useState(false);
	const [authenticationRetryAccountId, setAuthenticationRetryAccountId] = useState<string | null>(null);
	const [deviceRefreshPending, setDeviceRefreshPending] = useState(false);
	const verifyingRef = useRef<string | null>(null);

	const current = useCallback(() => queryClient.getQueryData<AgyAccountsResponse>(agyAccountsQueryKey), [queryClient]);
	const writeCurrent = useCallback((update: (snapshot: AgyAccountsResponse) => AgyAccountsResponse) => {
		const snapshot = current();
		if (snapshot) writeAgyAccounts(queryClient, update(snapshot), "replace");
	}, [current, queryClient]);

	const beginLogin = useCallback(async (accountId?: string) => {
		setError(null);
		setLoginPending(true);
		try {
			const started = accountId
				? await openAgyAccountReauthenticationTerminal(accountId)
				: await openAgyAccountLoginTerminal();
			writeCurrent((snapshot) => ({
				...snapshot,
				activeLogin: {
					operationId: started.operation.operationId,
					accountId: started.operation.accountId ?? accountId,
					status: started.operation.status,
					reasonCode: started.operation.reasonCode,
					reason: started.operation.reason,
					expiresAt: started.operation.expiresAt,
					shellTerminal: started.shellTerminal,
				},
			}));
			void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
		} catch (cause) {
			setError(errorMessage(cause, t("settings.agyAccounts.loginFailed")));
			throw cause;
		} finally {
			setLoginPending(false);
		}
	}, [queryClient, t, writeCurrent]);

	const verifyLogin = useCallback(async (login: AgyActiveLogin) => {
		const key = `${login.operationId}:${login.shellTerminal.handleId}`;
		if (verifyingRef.current === key) return;
		verifyingRef.current = key;
		setError(null);
		setLoginOperationPending(true);
		writeCurrent((snapshot) => snapshot.activeLogin?.operationId === login.operationId
			? { ...snapshot, activeLogin: { ...snapshot.activeLogin, status: "verifying" } }
			: snapshot);
		try {
			const operation = await verifyAgyAccountLogin(login.operationId);
			writeCurrent((snapshot) => {
				const accounts = operation.account
					? [...snapshot.accounts.filter((account) => account.id !== operation.account?.id), operation.account]
					: snapshot.accounts;
				const activeAccountId = operation.account?.active ? operation.account.id : snapshot.activeAccountId;
				const activeLogin = operation.status === "completed" ? undefined : {
					...login,
					accountId: operation.accountId ?? login.accountId,
					status: operation.status,
					reasonCode: operation.reasonCode,
					reason: operation.reason,
					expiresAt: operation.expiresAt,
				};
				return { ...snapshot, accounts, activeAccountId, activeLogin };
			});
			if (operation.status === "completed") void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
			return operation;
		} catch (cause) {
			setError(errorMessage(cause, t("settings.agyAccounts.loginVerificationFailed")));
			writeCurrent((snapshot) => snapshot.activeLogin?.operationId === login.operationId
					? { ...snapshot, activeLogin: { ...snapshot.activeLogin, status: "retryable", reasonCode: "login_failed" } }
				: snapshot);
			throw cause;
		} finally {
			verifyingRef.current = null;
			setLoginOperationPending(false);
		}
	}, [queryClient, t, writeCurrent]);

	const closeLogin = useCallback(async (login: AgyActiveLogin) => {
		setError(null);
		setLoginOperationPending(true);
		try {
			const operation = await cancelAgyAccountLogin(login.operationId);
			writeCurrent((snapshot) => ({
				...snapshot,
				activeLogin: operation.status === "cancelled" ? undefined : {
					...login,
					status: operation.status,
					reasonCode: operation.reasonCode,
					reason: operation.reason,
					expiresAt: operation.expiresAt,
				},
			}));
			void queryClient.invalidateQueries({ queryKey: shellTerminalsQueryKey });
		} catch (cause) {
			setError(errorMessage(cause, t("settings.agyAccounts.loginCloseFailed")));
			throw cause;
		} finally {
			setLoginOperationPending(false);
		}
	}, [queryClient, t, writeCurrent]);

	const retryLogin = useCallback(async (login: AgyActiveLogin) => {
		await closeLogin(login);
		await beginLogin(login.accountId);
	}, [beginLogin, closeLogin]);

	const ensureAccount = useCallback(async (accountId: string) => {
		const next = await ensureAgyAccounts([accountId]);
		writeAgyAccounts(queryClient, next, "preserveMissing");
	}, [queryClient]);

	const retryAuthentication = useCallback(async (accountId: string) => {
		setError(null);
		setAuthenticationRetryAccountId(accountId);
		try {
			const next = await ensureAgyAccounts([accountId], { forceAuthentication: true });
			writeAgyAccounts(queryClient, next, "preserveMissing");
		} catch (cause) {
			setError(errorMessage(cause, t("settings.agyAccounts.authenticationRetryFailed")));
			throw cause;
		} finally {
			setAuthenticationRetryAccountId(null);
		}
	}, [queryClient, t]);

	const retryDeviceRefresh = useCallback(async () => {
		setError(null);
		setDeviceRefreshPending(true);
		try {
			const next = await ensureAgyAccounts([], { forceDeviceReconciliation: true });
			writeAgyAccounts(queryClient, next, "replace");
		} catch (cause) {
			setError(errorMessage(cause, t("settings.agyAccounts.deviceRefreshFailed")));
			throw cause;
		} finally {
			setDeviceRefreshPending(false);
		}
	}, [queryClient, t]);

	const switchAccount = useCallback(async (account: AgyAccount, idempotencyKey: string) => {
		setError(null);
		try {
			const nextSwitch = await startAgyAccountSwitch(account.id, idempotencyKey);
			writeCurrent((snapshot) => ({ ...snapshot, currentSwitch: nextSwitch }));
		} catch (cause) {
			setError(errorMessage(cause, t("settings.agyAccounts.switchFailed")));
			throw cause;
		}
	}, [t, writeCurrent]);

	const getAccountSwitch = useCallback((switchId: string) => fetchAgyAccountSwitch(switchId), []);

	const deleteAccount = useCallback(async (account: AgyAccount) => {
		setError(null);
		try {
			writeAgyAccounts(queryClient, await deleteAgyAccount(account.id), "replace");
		}
		catch (cause) { setError(errorMessage(cause, t("settings.agyAccounts.deleteFailed"))); throw cause; }
	}, [queryClient, t]);

	return {
		error,
		loginPending,
		loginOperationPending,
		authenticationRetryAccountId,
		deviceRefreshPending,
		beginLogin,
		verifyLogin,
		closeLogin,
		retryLogin,
		ensureAccount,
		retryAuthentication,
		retryDeviceRefresh,
		switchAccount,
		getAccountSwitch,
		deleteAccount,
	};
}
