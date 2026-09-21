import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import {
	agentModelsQueryKey,
	agentModelsQueryOptions,
	refreshAgentModels,
	revalidateAgentModels,
	type AgentModelCatalog,
} from "../../hooks/useAgentModelsQuery";
import { AgentModelCombobox } from "./AgentModelCombobox";
import { SettingsOptionMenu } from "./SettingsOptionMenu";
import { SettingsRow } from "./SettingsRow";

const PERMISSION_MODE_VALUES = ["default", "accept-edits", "auto", "bypass-permissions"] as const;

export function AgentModelField({
	role,
	agentId,
	projectId,
	model,
	mode,
	effort,
	onModelChange,
	onModeChange,
	onEffortChange,
	onValidityChange,
	labelOverride,
}: {
	role: "worker" | "orchestrator" | "reviewer";
	agentId: string;
	projectId: string;
	model: string;
	mode: string;
	effort: string;
	onModelChange: (value: string) => void;
	onModeChange: (value: string) => void;
	onEffortChange: (value: string) => void;
	onValidityChange: (valid: boolean) => void;
	/** Replaces the role-derived row label, e.g. for a profile card. */
	labelOverride?: string;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const query = useQuery(agentModelsQueryOptions(agentId, projectId));
	const catalog: AgentModelCatalog | undefined = query.data;
	const revalidationQuery = useQuery({
		queryKey: ["agent-model-revalidation", agentId, projectId, catalog?.validatedAt ?? ""],
		queryFn: () => revalidateAgentModels(agentId, projectId),
		enabled: agentId !== "" && catalog?.refreshRecommended === true,
		staleTime: Number.POSITIVE_INFINITY,
		retry: false,
	});
	useEffect(() => {
		if (revalidationQuery.data) {
			queryClient.setQueryData(agentModelsQueryKey(agentId, projectId), revalidationQuery.data);
		}
	}, [agentId, projectId, queryClient, revalidationQuery.data]);
	const isMode = catalog?.selectionMode === "mode";
	const label = labelOverride ?? t(`settings.models.${role}${isMode ? "Mode" : "Model"}`);
	const warning =
		(revalidationQuery.isError
			? revalidationQuery.error instanceof Error
				? revalidationQuery.error.message
				: t("settings.models.validateFailed")
			: undefined) ??
		catalog?.warning ??
		(query.isError ? (query.error instanceof Error ? query.error.message : t("settings.models.loadFailed")) : undefined);

	if (agentId !== "" && query.isFetching && catalog === undefined) {
		return (
			<SettingsRow label={label}>
				<span className="text-xs text-settings-muted" role="status" aria-label={t("settings.models.loading")}>
					{t("settings.models.loading")}
				</span>
			</SettingsRow>
		);
	}

	if (isMode) {
		const options = [
			{ value: "__default__", label: t("settings.models.agentDefault") },
			...(catalog.models ?? []).map((item) => ({ value: item.id, label: item.label })),
		];
		return (
			<>
				<SettingsRow label={label}>
					<div className="flex min-w-0 items-center gap-2">
						<SettingsOptionMenu
							aria-label={label}
							value={mode || "__default__"}
							options={options}
							triggerClassName="justify-end"
							onChange={(value) => {
								onModeChange(value === "__default__" ? "" : value);
								onModelChange("");
							}}
						/>
					</div>
				</SettingsRow>
				{warning && <p className="px-1 text-xs leading-row text-warning">{warning}</p>}
			</>
		);
	}

	const customModelEntry = catalog?.customModelEntry ?? (catalog?.allowCustom ? "direct" : "none");
	const refreshCatalog = async () => {
		const refreshed = await refreshAgentModels(agentId, projectId);
		queryClient.setQueryData(agentModelsQueryKey(agentId, projectId), refreshed);
	};
	const selectCatalogModel = (value: string) => {
		onModelChange(value);
		onModeChange("");
	};
	const selectCustomModel = (value: string) => {
		onModelChange(value);
		onModeChange("");
	};
	return (
		<>
			<SettingsRow label={label}>
				<div className="flex min-w-0 items-center gap-2">
					<AgentModelCombobox
						aria-label={label}
						value={model}
						models={catalog?.models ?? []}
						allowCustom={catalog?.allowCustom}
						customModelEntry={customModelEntry}
						agentLabel={agentId}
						onRefresh={refreshCatalog}
						disabled={query.isFetching || agentId === ""}
						onChange={selectCatalogModel}
						onCustom={selectCustomModel}
						triggerClassName="justify-end"
						compact={agentId === "codex"}
						tuning={agentId === "codex" ? {
							effort,
							onEffortChange,
							onValidityChange,
							roleLabel: t(`settings.models.${role}Role`),
						} : undefined}
					/>
				</div>
			</SettingsRow>
			{warning && <p className="px-1 text-xs leading-row text-warning">{warning}</p>}
		</>
	);
}

export function PermissionModeSelect({ ariaLabel, value, onChange }: { ariaLabel: string; value: string; onChange: (value: string) => void }) {
	const { t } = useTranslation();
	const options = [
		{ value: "__default__", label: `${t("settings.project.permissionAuto")} (${t("settings.project.default")})` },
		...PERMISSION_MODE_VALUES.map((value) => ({
			value,
			label:
				value === "default"
					? t("settings.project.permissionDefault")
					: value === "accept-edits"
						? t("settings.project.permissionAcceptEdits")
						: value === "auto"
							? t("settings.project.permissionAuto")
							: t("settings.project.permissionBypass"),
		})),
	];

	return (
		<SettingsOptionMenu
			aria-label={ariaLabel}
			value={value || "__default__"}
			options={options}
			onChange={(v) => onChange(v === "__default__" ? "" : v)}
		/>
	);
}

export function buildRoleAgentConfig(
	existing: components["schemas"]["AgentConfig"] | undefined,
	model: string,
	mode: string,
	effort: string,
	permissions: string,
): components["schemas"]["AgentConfig"] | undefined {
	const next = { ...existing };
	if (model) next.model = model;
	else delete next.model;
	if (mode) next.mode = mode;
	else delete next.mode;
	if (effort) next.effort = effort;
	else delete next.effort;
	if (permissions) next.permissions = permissions as components["schemas"]["AgentConfig"]["permissions"];
	else delete next.permissions;
	return Object.keys(next).length > 0 ? next : undefined;
}
