import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
	ProjectAgentsSettingsView,
	ProjectGeneralSettingsView,
	ProjectSettingsFormView,
	ProjectSettingsSection,
	ProjectWorkflowSettingsView,
	validateProjectSettings,
} from "@aoagents/product-ui";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { useEffect, useState } from "react";
import { Copy, Info, Pencil, Plus, Trash2 } from "lucide-react";
import type { components } from "../../api/schema";
import { useAgentReadinessQuery, useEnsureAgentReadiness } from "../hooks/useAgentReadinessQuery";
import { useDefaultProfilesQuery } from "../hooks/useDefaultProfilesQuery";
import { useWorkspaceQuery, workspaceQueryKey } from "../hooks/useWorkspaceQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { captureOrchestratorReplacementFailure } from "../lib/orchestrator-replacement-telemetry";
import { OrchestratorSpawnError, spawnOrchestrator } from "../lib/spawn-orchestrator";
import { captureRendererEvent } from "../lib/telemetry";
import { type OrchestratorReplacementFailure, useUiStore } from "../stores/ui-store";
import { newestActiveOrchestrator } from "../types/workspace";
import { RequiredAgentField } from "./CreateProjectAgentSheet";
import { buildIntake, deriveRepoPath, deriveRepoHost, IntakeFields, type IntakeForm } from "./IntakeFields";
import { ProductExternalLink } from "./ProductExternalLink";
import { ReviewerSelect, reviewerTrustWarning } from "./ReviewerSelect";
import { AgentModelField, PermissionModeSelect, buildRoleAgentConfig } from "./settings/AgentModelField";
import { ProfileCard, newProfileDraft, newProfileKey, nextProfileName, profileDraftsFromConfig, profilesFromDrafts, validateProfileDrafts, type ProfileDraft } from "./settings/ProfileCard";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { SettingsRow } from "./settings/SettingsRow";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Switch } from "./ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "./ui/tooltip";

type Project = components["schemas"]["Project"];
type ProjectConfig = components["schemas"]["ProjectConfig"];
type TrackerIntakeConfig = components["schemas"]["TrackerIntakeConfig"];
type WorkflowTemplate = components["schemas"]["WorkflowTemplate"];

// A template as edited on the Templates tab: which profile fills each role slot
// (the desktop edits the first reviewer; further entries are kept as saved) and
// the orchestrator's plan file.
type TemplateDraft = {
	key: string;
	name: string;
	orchestrator: string;
	worker: string;
	reviewer: string;
	extraReviewers: string[];
	orchestratorRulesFile: string;
};

const DEFAULT_BRANCH_AUTO = "auto";

const projectQueryKey = (id: string) => ["project", id] as const;

type SettingsSaveResult = {
	replacementError: string | null;
	replacementSessionId: string | null;
	replacementFailure: OrchestratorReplacementFailure | null;
	spawnError: unknown;
};

export type ProjectSettingsSection = "general" | "agents" | "profiles" | "templates" | "workflow" | "intake";
export type ProjectSettingsSaveState = {
	phase: "idle" | "pending" | "saving" | "saved" | "failed";
	error?: string;
	replacementError?: string;
};

export function ProjectSettingsForm({
	projectId,
	section = "general",
	onSaveState,
}: {
	projectId: string;
	section?: ProjectSettingsSection;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();

	const query = useQuery({
		queryKey: projectQueryKey(projectId),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			if (data?.status !== "ok") throw new Error(t("settings.project.degraded"));
			return data.project as Project;
		},
	});

	return (
		<>
			{query.isLoading ? (
				<p className="text-sm text-settings-muted">{t("settings.project.loading")}</p>
			) : query.isError || !query.data ? (
				<p className="text-sm text-error">
					{query.error instanceof Error ? query.error.message : t("settings.project.loadFailed")}
				</p>
			) : (
				<SettingsBody
					key={projectId}
					project={query.data}
					onSaved={() =>
						queryClient.invalidateQueries({ queryKey: workspaceQueryKey }).catch(() => {
							// Saving succeeds even if the cache refresh fails.
						})
					}
					projectId={projectId}
					section={section}
					onSaveState={onSaveState}
				/>
			)}
		</>
	);
}

function SettingsBody({
	project,
	projectId,
	onSaved,
	section = "general",
	onSaveState,
}: {
	project: Project;
	projectId: string;
	onSaved: () => Promise<void>;
	section?: ProjectSettingsSection;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
}) {
	const { t } = useTranslation();
	const queryClient = useQueryClient();
	const navigate = useNavigate();
	const closeSettings = useUiStore((state) => state.closeSettings);
	const setOrchestratorReplacementError = useUiStore((state) => state.setOrchestratorReplacementError);
	const workspaceQuery = useWorkspaceQuery();
	const config = project.config ?? {};
	const isScratchProject = project.kind === "scratch";
	const workspace = workspaceQuery.data?.find((item) => item.id === projectId);
	const activeOrchestrator = newestActiveOrchestrator(workspace?.sessions ?? []);
	const intake: TrackerIntakeConfig = config.trackerIntake ?? {};
	const [form, setForm] = useState({
		displayName: project.name,
		defaultBranch: config.defaultBranch ?? DEFAULT_BRANCH_AUTO,
		sessionPrefix: config.sessionPrefix ?? "",
		workerAgent: config.worker?.agent ?? "",
		orchestratorAgent: config.orchestrator?.agent ?? "",
		workerProfile: config.worker?.profile ?? "",
		orchestratorProfile: config.orchestrator?.profile ?? "",
		profiles: profileDraftsFromConfig(config.profiles),
		templates: templateDraftsFromConfig(config.templates),
		template: config.template ?? "",
		workerModel: config.worker?.agentConfig?.model ?? config.agentConfig?.model ?? "",
		workerEffort: config.worker?.agentConfig?.effort ?? config.agentConfig?.effort ?? "",
		workerPermissions: config.worker?.agentConfig?.permissions ?? config.agentConfig?.permissions ?? "",
		orchestratorModel: config.orchestrator?.agentConfig?.model ?? config.agentConfig?.model ?? "",
		orchestratorEffort: config.orchestrator?.agentConfig?.effort ?? config.agentConfig?.effort ?? "",
		orchestratorPermissions: config.orchestrator?.agentConfig?.permissions ?? config.agentConfig?.permissions ?? "",
		workerMode: config.worker?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		orchestratorMode: config.orchestrator?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		reviewerHarness: config.reviewers?.[0]?.harness ?? "",
		reviewerProfile: config.reviewers?.[0]?.profile ?? "",
		reviewerModel: config.reviewers?.[0]?.agentConfig?.model ?? config.agentConfig?.model ?? "",
		reviewerMode: config.reviewers?.[0]?.agentConfig?.mode ?? config.agentConfig?.mode ?? "",
		reviewerEffort: config.reviewers?.[0]?.agentConfig?.effort ?? config.agentConfig?.effort ?? "",
		reviewerPermissions: config.reviewers?.[0]?.agentConfig?.permissions ?? config.agentConfig?.permissions ?? "",
		autoReview: config.autoReview ?? false,
		intakeEnabled: intake.enabled ?? false,
		intakeRepo: intake.repo ?? "",
		intakeAssignee: intake.assignee ?? "",
	});
	const [savedAt, setSavedAt] = useState<number | null>(null);
	const [showSaving, setShowSaving] = useState(false);
	const [replacementError, setReplacementError] = useState<string | null>(null);
	const [validationError, setValidationError] = useState<string | null>(null);
	const [tuningValidity, setTuningValidity] = useState({ worker: true, orchestrator: true, reviewer: true });
	// A role profile supplies the harness when the role names none, so a
	// profile-driven role is not "missing" an agent (the daemon folds the
	// profile the same way at spawn).
	// The user's default profiles are offered wherever a project profile is,
	// labelled as defaults; a project profile of the same name hides them.
	const defaultProfiles = useDefaultProfilesQuery().data?.profiles ?? {};
	const profileAgentFor = (name: string) => form.profiles.find((p) => p.name.trim() === name.trim())?.agent ?? defaultProfiles[name.trim()]?.agent ?? "";
	const effectiveWorkerAgent = form.workerAgent || profileAgentFor(form.workerProfile);
	const effectiveOrchestratorAgent = form.orchestratorAgent || profileAgentFor(form.orchestratorProfile);
	const initialOrchestratorAgent =
		config.orchestrator?.agent || config.profiles?.[config.orchestrator?.profile ?? ""]?.agent || "";
	const missingRequiredAgent = effectiveWorkerAgent === "" || effectiveOrchestratorAgent === "";
	const agentsQuery = useAgentReadinessQuery();
	useEnsureAgentReadiness();
	useEnsureAgentReadiness({
		agentIds: [effectiveWorkerAgent, effectiveOrchestratorAgent, form.reviewerHarness],
		enabled: effectiveWorkerAgent !== "" || effectiveOrchestratorAgent !== "" || form.reviewerHarness !== "",
	});
	const agentCatalog = agentsQuery.data;

	const intakeForm: IntakeForm = {
		enabled: form.intakeEnabled,
		repo: form.intakeRepo,
		assignee: form.intakeAssignee,
	};
	const patchIntake = (patch: Partial<IntakeForm>) =>
		setForm((f) => ({
			...f,
			intakeEnabled: patch.enabled ?? f.intakeEnabled,
			intakeRepo: patch.repo ?? f.intakeRepo,
			intakeAssignee: patch.assignee ?? f.intakeAssignee,
		}));
	const effectiveIntakeRepo = form.intakeRepo.trim() || deriveRepoPath(project.repo);
	const reviewerWarning = reviewerTrustWarning(form.reviewerHarness);
	const mutation = useMutation({
		mutationFn: async () => {
			void captureRendererEvent("ao.renderer.settings_save_requested", { project_id: projectId });
			const displayName = form.displayName.trim();
			const {
				model: _legacyModel,
				mode: _legacyMode,
				effort: _legacyEffort,
				permissions: _legacyPermissions,
				...sharedAgentConfig
			} = config.agentConfig ?? {};
			const existingReviewer = config.reviewers?.[0];
			const existingReviewerAgentConfig =
				existingReviewer?.harness === form.reviewerHarness ? existingReviewer.agentConfig : undefined;
			const next: ProjectConfig = isScratchProject
				? {
						...scratchSupportedConfig(config),
						worker: {
							...config.worker,
							agent: form.workerAgent,
							profile: form.workerProfile || undefined,
							agentConfig: buildRoleAgentConfig(config.worker?.agentConfig, form.workerModel, form.workerMode, form.workerAgent === "codex" ? form.workerEffort : "", form.workerPermissions),
						},
						orchestrator: {
							...config.orchestrator,
							agent: form.orchestratorAgent,
							profile: form.orchestratorProfile || undefined,
							agentConfig: buildRoleAgentConfig(
								config.orchestrator?.agentConfig,
								form.orchestratorModel,
								form.orchestratorMode,
								form.orchestratorAgent === "codex" ? form.orchestratorEffort : "",
								form.orchestratorPermissions,
							),
						},
						agentConfig: blankToUndefined({
							...sharedAgentConfig,
							permissions: undefined,
						}),
						profiles: profilesFromDrafts(form.profiles),
						templates: templatesFromDrafts(form.templates),
						template: form.template || undefined,
					}
				: {
						...config,
						defaultBranch:
							form.defaultBranch.trim() === DEFAULT_BRANCH_AUTO
								? undefined
								: form.defaultBranch || undefined,
						sessionPrefix: form.sessionPrefix || undefined,
						worker: {
							...config.worker,
							agent: form.workerAgent,
							profile: form.workerProfile || undefined,
							agentConfig: buildRoleAgentConfig(config.worker?.agentConfig, form.workerModel, form.workerMode, form.workerAgent === "codex" ? form.workerEffort : "", form.workerPermissions),
						},
						orchestrator: {
							...config.orchestrator,
							agent: form.orchestratorAgent,
							profile: form.orchestratorProfile || undefined,
							agentConfig: buildRoleAgentConfig(
								config.orchestrator?.agentConfig,
								form.orchestratorModel,
								form.orchestratorMode,
								form.orchestratorAgent === "codex" ? form.orchestratorEffort : "",
								form.orchestratorPermissions,
							),
						},
						agentConfig: blankToUndefined({
							...sharedAgentConfig,
							permissions: undefined,
						}),
						profiles: profilesFromDrafts(form.profiles),
						templates: templatesFromDrafts(form.templates),
						template: form.template || undefined,
						reviewers: form.reviewerHarness
							? [{
									harness: form.reviewerHarness,
									agentConfig: buildRoleAgentConfig(existingReviewerAgentConfig, form.reviewerModel, form.reviewerMode, form.reviewerHarness === "codex" ? form.reviewerEffort : "", form.reviewerPermissions),
									...(form.reviewerProfile ? { profile: form.reviewerProfile } : {}),
								}, ...(config.reviewers ?? []).slice(1)]
							: undefined,
						trackerIntake: buildIntake(intakeForm, config.trackerIntake),
						autoReview: form.autoReview,
					};
			const { error } = await apiClient.PUT("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
				body: { displayName, config: next },
			});
			if (error) throw new Error(apiErrorMessage(error));
			if (
				effectiveOrchestratorAgent !== initialOrchestratorAgent ||
				(activeOrchestrator && activeOrchestrator.provider !== effectiveOrchestratorAgent)
			) {
				try {
					const sessionId = await spawnOrchestrator(projectId, "settings", true);
					return {
						replacementError: null,
						replacementSessionId: sessionId,
						replacementFailure: null,
						spawnError: null,
					} satisfies SettingsSaveResult;
				} catch (error) {
					const replacementFailure: OrchestratorReplacementFailure = {
						message:
							error instanceof Error ? error.message : t("settings.project.replaceOrchestratorFailed"),
						...(error instanceof OrchestratorSpawnError
							? { code: error.code, requestId: error.requestId }
							: {}),
					};
					return {
						replacementError: replacementFailure.message,
						replacementSessionId: null,
						replacementFailure,
						spawnError: error,
					} satisfies SettingsSaveResult;
				}
			}
			return {
				replacementError: null,
				replacementSessionId: null,
				replacementFailure: null,
				spawnError: null,
			} satisfies SettingsSaveResult;
		},
		onSuccess: async (result) => {
			void captureRendererEvent("ao.renderer.settings_save_succeeded", { project_id: projectId });
			setSavedAt(Date.now());
			setReplacementError(result.replacementError);
			setValidationError(null);
			void queryClient.invalidateQueries({ queryKey: ["project", projectId] });
			const workspaceRefresh = onSaved();

			if (result.replacementSessionId) {
				await workspaceRefresh;
				closeSettings();
				void navigate({
					to: "/projects/$projectId/sessions/$sessionId",
					params: { projectId, sessionId: result.replacementSessionId },
				});
				return;
			}

			if (result.replacementFailure) {
				closeSettings();
				setOrchestratorReplacementError(projectId, result.replacementFailure);
				if (result.spawnError) {
					captureOrchestratorReplacementFailure(result.spawnError, projectId);
				}
			}
		},
		onError: () => {
			void captureRendererEvent("ao.renderer.settings_save_failed", { project_id: projectId });
		},
	});

	useEffect(() => {
		if (!mutation.isPending) {
			setShowSaving(false);
			return;
		}
		const timeout = window.setTimeout(() => setShowSaving(true), 200);
		return () => window.clearTimeout(timeout);
	}, [mutation.isPending]);

	useEffect(() => {
		const mutationError = mutation.isError
			? mutation.error instanceof Error
				? mutation.error.message
				: t("settings.project.saveFailed")
			: undefined;
		onSaveState?.({
			phase: validationError || mutationError
				? "failed"
				: mutation.isPending
					? showSaving
						? "saving"
						: "pending"
					: savedAt !== null
						? "saved"
						: "idle",
			error: validationError ?? mutationError,
			replacementError: !mutation.isPending && !mutation.isError ? replacementError ?? undefined : undefined,
		});
	}, [
		mutation.error,
		mutation.isError,
		mutation.isPending,
		onSaveState,
		replacementError,
		savedAt,
		showSaving,
		t,
		validationError,
	]);

	useEffect(() => {
		if (savedAt === null) return;
		const timeout = window.setTimeout(() => setSavedAt(null), 1800);
		return () => window.clearTimeout(timeout);
	}, [savedAt]);

	return (
		<ProjectSettingsFormView
			id="project-settings-form"
				onSubmit={() => {
				setSavedAt(null);
				setReplacementError(null);
				const validation = validateProjectSettings(
					{ ...form, workerAgent: effectiveWorkerAgent, orchestratorAgent: effectiveOrchestratorAgent },
					{ validateIntake: !isScratchProject },
				);
				if (validation) {
					setValidationError(
						validation === "agents_required"
							? t("settings.project.agentsRequired")
							: validation === "name_required"
								? t("settings.project.nameRequired")
								: t("settings.project.intakeAssigneeRequired"),
					);
					return;
				}
				if (!tuningValidity.worker || !tuningValidity.orchestrator || !tuningValidity.reviewer) {
					setValidationError(t("settings.project.tuningInvalid"));
					return;
				}
				const profileError = validateProfileDrafts(form.profiles, t);
				if (profileError) {
					setValidationError(profileError);
					return;
				}
				const templateError = validateTemplateDrafts(form.templates, form.profiles, t);
				if (templateError) {
					setValidationError(templateError);
					return;
				}
				setValidationError(null);
				mutation.mutate();
			}}
		>
			{section === "general" && (
				<>
					<ProjectGeneralSettingsView
						displayName={form.displayName}
						externalLink={ProductExternalLink}
						icons={{
							edit: <Pencil className="settings-inline-edit-icon" aria-hidden="true" />,
						}}
						onDisplayNameChange={(displayName) => setForm((f) => ({ ...f, displayName }))}
						labels={{
							title: t("settings.project.identity"),
							name: t("settings.project.name"),
							id: t("settings.project.id"),
							kind: t("settings.project.kind"),
							path: t("settings.project.path"),
							repo: t("settings.project.repo"),
							workspaceRepos: t("settings.project.workspaceRepos"),
							workspaceReposEmpty: t("settings.project.childReposEmpty"),
							editName: t("settings.field.edit", { label: t("settings.project.name") }),
						}}
						project={{
							id: project.id,
							kindLabel: projectKindLabel(project.kind, t),
							path: project.path,
							pathHref: `file://${encodeURI(project.path)}`,
							repo: project.repo,
							repoHref: project.repo ? repositoryHref(project.repo) : undefined,
							workspaceRepos: project.kind === "workspace" ? project.workspaceRepos ?? [] : undefined,
						}}
					/>
				</>
			)}

			{section === "agents" && (
				<>
					<ProjectAgentsSettingsView
						title={t("settings.project.agents")}
						workerArea={
							<>
								<ProfileRoleRow
									label={t("settings.project.workerProfile")}
									value={form.workerProfile}
									explicitAgent={form.workerAgent}
									profiles={form.profiles}
									defaultProfiles={defaultProfiles}
									agents={agentCatalog?.agents}
									onChange={(workerProfile) => setForm((f) => ({ ...f, workerProfile }))}
								/>
							<RequiredAgentField
								id="workerAgent"
								variant="settings-row"
								value={form.workerAgent}
								placeholder={t("settings.project.selectWorker")}
								label={t("settings.project.defaultWorker")}
								agents={agentCatalog?.agents}
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
								invalid={validationError !== null && effectiveWorkerAgent === ""}
								onChange={(v) =>
									setForm((f) => ({ ...f, workerAgent: v, workerModel: "", workerMode: "", workerEffort: "" }))
								}
							/>
							</>
						}
						workerModelArea={
							<AgentModelField
								role="worker"
								agentId={form.workerAgent}
								projectId={projectId}
								model={form.workerModel}
								mode={form.workerMode}
								effort={form.workerEffort}
								onModelChange={(workerModel) => setForm((f) => ({ ...f, workerModel }))}
								onModeChange={(workerMode) => setForm((f) => ({ ...f, workerMode }))}
								onEffortChange={(workerEffort) => setForm((f) => ({ ...f, workerEffort }))}
								onValidityChange={(valid) => setTuningValidity((value) => ({ ...value, worker: valid }))}
							/>
						}
						orchestratorArea={
							<>
								<ProfileRoleRow
									label={t("settings.project.orchestratorProfile")}
									value={form.orchestratorProfile}
									explicitAgent={form.orchestratorAgent}
									profiles={form.profiles}
									defaultProfiles={defaultProfiles}
									agents={agentCatalog?.agents}
									onChange={(orchestratorProfile) => setForm((f) => ({ ...f, orchestratorProfile }))}
								/>
							<RequiredAgentField
								id="orchestratorAgent"
								variant="settings-row"
								value={form.orchestratorAgent}
								placeholder={t("settings.project.selectOrchestrator")}
								label={t("settings.project.defaultOrchestrator")}
								agents={agentCatalog?.agents}
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
								invalid={validationError !== null && effectiveOrchestratorAgent === ""}
								onChange={(v) =>
									setForm((f) => ({
										...f,
										orchestratorAgent: v,
										orchestratorModel: "",
										orchestratorMode: "",
										orchestratorEffort: "",
									}))
								}
							/>
							</>
						}
						orchestratorModelArea={
							<AgentModelField
								role="orchestrator"
								agentId={form.orchestratorAgent}
								projectId={projectId}
								model={form.orchestratorModel}
								mode={form.orchestratorMode}
								effort={form.orchestratorEffort}
								onModelChange={(orchestratorModel) => setForm((f) => ({ ...f, orchestratorModel }))}
								onModeChange={(orchestratorMode) => setForm((f) => ({ ...f, orchestratorMode }))}
								onEffortChange={(orchestratorEffort) => setForm((f) => ({ ...f, orchestratorEffort }))}
								onValidityChange={(valid) => setTuningValidity((value) => ({ ...value, orchestrator: valid }))}
							/>
						}
						permissions={{
							control: (
								<PermissionModeSelect
									ariaLabel={t("settings.project.roleApproval", { role: t("settings.models.workerRole") })}
									value={form.workerPermissions}
									onChange={(v) => setForm((f) => ({ ...f, workerPermissions: v }))}
								/>
							),
							label: t("settings.project.roleApproval", { role: t("settings.models.workerRole") }),
						}}
						orchestratorPermissions={{
							control: <PermissionModeSelect ariaLabel={t("settings.project.roleApproval", { role: t("settings.models.orchestratorRole") })} value={form.orchestratorPermissions} onChange={(v) => setForm((f) => ({ ...f, orchestratorPermissions: v }))} />,
							label: t("settings.project.roleApproval", { role: t("settings.models.orchestratorRole") }),
						}}
						missingRequiredMessage={
							missingRequiredAgent ? t("settings.project.agentsRequired") : null
						}
					/>
				{!isScratchProject && (
					<ProjectSettingsSection title={t("settings.project.reviewer")} grouped>
						<SettingsRow label={t("settings.project.defaultReviewer")}>
							<ReviewerSelect
								value={form.reviewerHarness}
								model={form.reviewerModel}
								mode={form.reviewerMode}
								projectId={projectId}
								onConfigChange={(_harness, config) => setForm((f) => ({
									...f,
									reviewerModel: config.model ?? "",
									reviewerMode: config.mode ?? "",
								}))}
								onChange={(v) =>
								setForm((f) => ({
									...f,
									reviewerHarness: v,
									...(v !== f.reviewerHarness ? {
										reviewerModel: "", reviewerMode: "", reviewerEffort: "",
										reviewerPermissions: "", reviewerProfile: "",
									} : {}),
									}))
								}
								ariaLabel={t("settings.project.defaultReviewer")}
								agents={agentCatalog?.agents}
								defaultOptionLabel={t("settings.project.default")}
								defaultTriggerLabel={t("settings.project.default")}
								disabled={agentsQuery.isFetching && agentCatalog === undefined}
							/>
						</SettingsRow>
						{form.reviewerHarness ? (
							<AgentModelField
								role="reviewer"
								agentId={form.reviewerHarness}
								projectId={projectId}
								model={form.reviewerModel}
								mode={form.reviewerMode}
								effort={form.reviewerEffort}
								onModelChange={(reviewerModel) => setForm((f) => ({ ...f, reviewerModel }))}
								onModeChange={(reviewerMode) => setForm((f) => ({ ...f, reviewerMode }))}
								onEffortChange={(reviewerEffort) => setForm((f) => ({ ...f, reviewerEffort }))}
								onValidityChange={(valid) => setTuningValidity((value) => ({ ...value, reviewer: valid }))}
							/>
						) : null}
						<SettingsRow label={t("settings.project.roleApproval", { role: t("settings.models.reviewerRole") })}>
							<PermissionModeSelect
								ariaLabel={t("settings.project.roleApproval", { role: t("settings.models.reviewerRole") })}
								value={form.reviewerPermissions}
								onChange={(reviewerPermissions) => setForm((f) => ({ ...f, reviewerPermissions }))}
							/>
						</SettingsRow>
						{reviewerWarning && (
							<p className="px-1 text-xs leading-row text-warning" role="status">
								{reviewerWarning}
							</p>
						)}
						<div className="settings-row-bar">
							<div className="flex shrink-0 items-center gap-1.5">
								<span className="whitespace-nowrap text-sm leading-5 text-settings-label">
									{t("settings.project.autoReviewToggle")}
								</span>
								<Tooltip>
									<TooltipTrigger asChild>
										<button
											type="button"
											className="inline-flex size-5 items-center justify-center rounded-md text-settings-muted transition-colors hover:bg-settings-menu-selected hover:text-settings-label focus-visible:ring-1 focus-visible:ring-ring focus-visible:outline-none"
											aria-label={t("settings.project.autoReviewDescription")}
										>
											<Info className="size-icon-sm" aria-hidden="true" />
										</button>
									</TooltipTrigger>
									<TooltipContent className="max-w-72 leading-normal" side="top">
										{t("settings.project.autoReviewDescription")}
									</TooltipContent>
								</Tooltip>
							</div>
							<div className="flex min-w-0 flex-1 items-center justify-end">
								<Switch
									aria-label={t("settings.project.autoReviewToggle")}
									checked={form.autoReview}
									id="project-auto-review"
									onCheckedChange={(checked) => setForm((f) => ({ ...f, autoReview: checked }))}
								/>
							</div>
						</div>
					</ProjectSettingsSection>
				)}
				</>
			)}

			{section === "profiles" && (
				<ProjectSettingsSection title={t("settings.project.profiles")} titleHidden grouped>
					<p className="px-1 text-xs leading-row text-settings-muted">{t("settings.project.profilesDescription")}</p>
					{form.profiles.length === 0 ? (
						<p className="px-1 text-xs leading-row text-settings-muted">{t("settings.project.profilesEmpty")}</p>
					) : null}
					{form.profiles.map((draft) => (
						<ProfileCard
							key={draft.key}
							draft={draft}
							profiles={form.profiles}
							projectId={projectId}
							agents={agentCatalog?.agents}
							agentsLoading={agentsQuery.isFetching && agentCatalog === undefined}
							onChange={(patch) =>
								setForm((f) => ({
									...f,
									profiles: f.profiles.map((p) => (p.key === draft.key ? { ...p, ...patch } : p)),
								}))
							}
							onDuplicate={() =>
								setForm((f) => {
									const at = f.profiles.findIndex((p) => p.key === draft.key);
									const copy = { ...draft, key: newProfileKey(), name: nextProfileName(draft.name, f.profiles) };
									return { ...f, profiles: [...f.profiles.slice(0, at + 1), copy, ...f.profiles.slice(at + 1)] };
								})
							}
							onDelete={() =>
								setForm((f) => ({
									...f,
									workerProfile: f.workerProfile === draft.name.trim() ? "" : f.workerProfile,
									orchestratorProfile: f.orchestratorProfile === draft.name.trim() ? "" : f.orchestratorProfile,
									reviewerProfile: f.reviewerProfile === draft.name.trim() ? "" : f.reviewerProfile,
									templates: f.templates.map((tpl) => unbindProfile(tpl, draft.name.trim())),
									profiles: f.profiles
										.filter((p) => p.key !== draft.key)
										.map((p) => (p.fallbackProfile.trim() === draft.name.trim() ? { ...p, fallbackProfile: "" } : p)),
								}))
							}
						/>
					))}
					<div className="flex justify-end px-1">
						<Button
							type="button"
							size="sm"
							variant="outline"
							onClick={() =>
								setForm((f) => ({
									...f,
									profiles: [...f.profiles, newProfileDraft(nextProfileName("profile", f.profiles))],
								}))
							}
						>
							<Plus className="size-3.5" aria-hidden="true" />
							{t("settings.project.addProfile")}
						</Button>
					</div>
				</ProjectSettingsSection>
			)}

			{section === "templates" && (
				<ProjectSettingsSection title={t("settings.project.templates")} titleHidden grouped>
					<p className="px-1 text-xs leading-row text-settings-muted">{t("settings.project.templatesDescription")}</p>
					{form.templates.length === 0 ? (
						<p className="px-1 text-xs leading-row text-settings-muted">{t("settings.project.templatesEmpty")}</p>
					) : null}
					{form.templates.map((draft) => (
						<TemplateCard
							key={draft.key}
							draft={draft}
							active={form.template !== "" && form.template === draft.name.trim()}
							profiles={form.profiles}
							defaultProfiles={defaultProfiles}
							withReviewer={!isScratchProject}
							onChange={(patch) =>
								setForm((f) => ({
									...f,
									templates: f.templates.map((tpl) => (tpl.key === draft.key ? { ...tpl, ...patch } : tpl)),
									// Renaming the active template keeps it active.
									template: patch.name !== undefined && f.template === draft.name.trim() ? patch.name.trim() : f.template,
								}))
							}
							onApply={() =>
								setForm((f) => {
									const reviewer = isScratchProject ? "" : draft.reviewer.trim();
									const reviewerAgent = f.profiles.find((p) => p.name.trim() === reviewer)?.agent ?? defaultProfiles[reviewer]?.agent ?? "";
									return {
										...f,
										workerProfile: draft.worker.trim(),
										orchestratorProfile: draft.orchestrator.trim(),
										...(isScratchProject
											? {}
											: {
													reviewerHarness: reviewerAgent,
													reviewerProfile: reviewerAgent ? reviewer : "",
													reviewerModel: "",
													reviewerMode: "",
													reviewerEffort: "",
													reviewerPermissions: "",
												}),
										template: draft.name.trim(),
									};
								})
							}
							onDuplicate={() =>
								setForm((f) => {
									const at = f.templates.findIndex((tpl) => tpl.key === draft.key);
									const copy = { ...draft, key: newProfileKey(), name: nextTemplateName(draft.name, f.templates) };
									return { ...f, templates: [...f.templates.slice(0, at + 1), copy, ...f.templates.slice(at + 1)] };
								})
							}
							onDelete={() =>
								setForm((f) => ({
									...f,
									templates: f.templates.filter((tpl) => tpl.key !== draft.key),
									template: f.template === draft.name.trim() ? "" : f.template,
								}))
							}
						/>
					))}
					<div className="flex justify-end px-1">
						<Button
							type="button"
							size="sm"
							variant="outline"
							onClick={() =>
								setForm((f) => ({
									...f,
									templates: [...f.templates, { key: newProfileKey(), name: nextTemplateName("template", f.templates), orchestrator: "", worker: "", reviewer: "", extraReviewers: [], orchestratorRulesFile: "" }],
								}))
							}
						>
							<Plus className="size-3.5" aria-hidden="true" />
							{t("settings.project.addTemplate")}
						</Button>
					</div>
				</ProjectSettingsSection>
			)}

			{section === "workflow" && (
				<>
					{!isScratchProject ? (
						<>
							<ProjectWorkflowSettingsView
								branch={form.defaultBranch}
								icons={{
									edit: <Pencil className="settings-inline-edit-icon" aria-hidden="true" />,
								}}
								prefix={form.sessionPrefix}
								onBranchChange={(defaultBranch) => setForm((f) => ({ ...f, defaultBranch }))}
								onPrefixChange={(sessionPrefix) => setForm((f) => ({ ...f, sessionPrefix }))}
								labels={{
									worktrees: t("settings.project.worktrees"),
									defaultBranch: t("settings.project.defaultBranch"),
									sessionPrefix: t("settings.project.sessionPrefix"),
									reviewers: t("settings.project.reviewers"),
									defaultReviewer: t("settings.project.defaultReviewer"),
									editDefaultBranch: t("settings.field.edit", {
										label: t("settings.project.defaultBranch"),
									}),
									editSessionPrefix: t("settings.field.edit", {
										label: t("settings.project.sessionPrefix"),
									}),
								}}
							/>
						</>
					) : (
						<p className="px-1 text-xs text-settings-muted">{t("settings.project.workflow")}</p>
					)}
				</>
			)}

			{section === "intake" && (
				<>
					{!isScratchProject ? (
						<ProjectSettingsSection title={t("settings.project.trackerIntake")} grouped>
							<IntakeFields
								variant="settings"
								form={intakeForm}
								onChange={patchIntake}
								repoPreview={{ value: effectiveIntakeRepo, host: deriveRepoHost(project.repo) }}
							/>
						</ProjectSettingsSection>
					) : (
						<p className="px-1 text-xs text-settings-muted">{t("settings.project.trackerIntake")}</p>
					)}
				</>
			)}
		</ProjectSettingsFormView>
	);
}

function ProfileRoleRow({
	label,
	value,
	explicitAgent,
	profiles,
	defaultProfiles,
	agents,
	onChange,
}: {
	label: string;
	value: string;
	explicitAgent: string;
	profiles: ProfileDraft[];
	defaultProfiles?: Record<string, components["schemas"]["RoleProfile"]>;
	agents?: components["schemas"]["AgentReadinessSnapshot"][];
	onChange: (value: string) => void;
}) {
	const { t } = useTranslation();
	const names = profiles.map((p) => p.name.trim()).filter((name) => name !== "");
	const defaultNames = Object.keys(defaultProfiles ?? {}).filter((name) => !names.includes(name)).sort((a, b) => a.localeCompare(b));
	const options = [
		{ value: "__none__", label: t("settings.project.profileNone") },
		...names.map((name) => ({ value: name, label: name })),
		...defaultNames.map((name) => ({ value: name, label: t("settings.project.profileDefaultOption", { name }) })),
		...(value && !names.includes(value) && !defaultNames.includes(value) ? [{ value, label: value }] : []),
	];
	const draft = profiles.find((p) => p.name.trim() === value.trim());
	const fallback = defaultProfiles?.[value.trim()];
	const selected = draft ?? (fallback ? { agent: fallback.agent ?? "", model: fallback.agentConfig?.model ?? "" } : undefined);
	const agentLabel = selected?.agent ? (agents?.find((a) => a.id === selected.agent)?.label ?? selected.agent) : "";
	const hint =
		selected && explicitAgent === "" && (agentLabel || selected.model)
			? selected.model
				? t("settings.project.profileEffective", { agent: agentLabel || t("settings.project.default"), model: selected.model })
				: t("settings.project.profileEffectiveAgent", { agent: agentLabel })
			: null;
	return (
		<>
			<SettingsRow label={label}>
				<SettingsOptionMenu
					aria-label={label}
					value={value || "__none__"}
					options={options}
					onChange={(v) => onChange(v === "__none__" ? "" : v)}
				/>
			</SettingsRow>
			{hint ? <p className="px-1 text-xs leading-row text-settings-muted">{hint}</p> : null}
		</>
	);
}

function TemplateCard({
	draft,
	active,
	profiles,
	defaultProfiles,
	withReviewer,
	onChange,
	onApply,
	onDuplicate,
	onDelete,
}: {
	draft: TemplateDraft;
	active: boolean;
	profiles: ProfileDraft[];
	defaultProfiles?: Record<string, components["schemas"]["RoleProfile"]>;
	withReviewer: boolean;
	onChange: (patch: Partial<TemplateDraft>) => void;
	onApply: () => void;
	onDuplicate: () => void;
	onDelete: () => void;
}) {
	const { t } = useTranslation();
	const name = draft.name.trim() || t("settings.project.templateUnnamed");
	const names = profiles.map((p) => p.name.trim()).filter((n) => n !== "");
	const defaultNames = Object.keys(defaultProfiles ?? {}).filter((n) => !names.includes(n)).sort((a, b) => a.localeCompare(b));
	const slotOptions = (value: string) => [
		{ value: "__none__", label: t("settings.project.profileNone") },
		...names.map((n) => ({ value: n, label: n })),
		...defaultNames.map((n) => ({ value: n, label: t("settings.project.profileDefaultOption", { name: n }) })),
		...(value && !names.includes(value) && !defaultNames.includes(value) ? [{ value, label: value }] : []),
	];
	const slot = (label: string, ariaLabel: string, value: string, patch: (v: string) => Partial<TemplateDraft>) => (
		<SettingsRow label={label}>
			<SettingsOptionMenu
				aria-label={ariaLabel}
				value={value || "__none__"}
				options={slotOptions(value)}
				onChange={(v) => onChange(patch(v === "__none__" ? "" : v))}
			/>
		</SettingsRow>
	);
	return (
		<div className="flex flex-col gap-1 rounded-md border border-border/70 px-2 py-2" data-testid="template-card" data-active={active ? "true" : undefined}>
			<div className="flex items-center justify-between gap-2 px-1">
				<span className="flex items-center gap-2 text-sm font-medium text-foreground">
					{name}
					{active ? (
						<span className="rounded-sm bg-primary/15 px-1.5 py-0.5 text-2xs font-medium text-primary">{t("settings.project.templateActive")}</span>
					) : null}
				</span>
				<div className="flex items-center gap-1">
					<Button type="button" size="sm" variant={active ? "ghost" : "outline"} disabled={active} aria-label={t("settings.project.applyTemplate", { name })} onClick={onApply}>
						{t("settings.project.applyTemplateLabel")}
					</Button>
					<Button type="button" size="sm" variant="ghost" aria-label={t("settings.project.duplicateTemplate", { name })} onClick={onDuplicate}>
						<Copy className="size-3.5" aria-hidden="true" />
					</Button>
					<Button type="button" size="sm" variant="ghost" aria-label={t("settings.project.deleteTemplate", { name })} onClick={onDelete}>
						<Trash2 className="size-3.5" aria-hidden="true" />
					</Button>
				</div>
			</div>
			<SettingsRow label={t("settings.project.profileName")}>
				<Input
					aria-label={t("settings.project.templateNameFor", { name })}
					className="max-w-64 text-right"
					value={draft.name}
					onChange={(event) => onChange({ name: event.target.value })}
				/>
			</SettingsRow>
			{slot(t("settings.project.orchestratorProfile"), t("settings.project.templateOrchestratorFor", { name }), draft.orchestrator, (orchestrator) => ({ orchestrator }))}
			{slot(t("settings.project.workerProfile"), t("settings.project.templateWorkerFor", { name }), draft.worker, (worker) => ({ worker }))}
			{withReviewer ? slot(t("settings.project.templateReviewer"), t("settings.project.templateReviewerFor", { name }), draft.reviewer, (reviewer) => ({ reviewer })) : null}
			<SettingsRow label={t("settings.project.templatePlan")} description={t("settings.project.templatePlanHint")}>
				<Input
					aria-label={t("settings.project.templatePlanFor", { name })}
					className="max-w-64 text-right"
					placeholder="rules/plan-flash-first.md"
					value={draft.orchestratorRulesFile}
					onChange={(event) => onChange({ orchestratorRulesFile: event.target.value })}
				/>
			</SettingsRow>
		</div>
	);
}

function templateDraftsFromConfig(templates: Record<string, WorkflowTemplate> | undefined): TemplateDraft[] {
	return Object.entries(templates ?? {})
		.sort(([a], [b]) => a.localeCompare(b))
		.map(([name, tpl]) => ({
			key: newProfileKey(),
			name,
			orchestrator: tpl.orchestrator ?? "",
			worker: tpl.worker ?? "",
			reviewer: tpl.reviewers?.[0] ?? "",
			extraReviewers: (tpl.reviewers ?? []).slice(1),
			orchestratorRulesFile: tpl.orchestratorRulesFile ?? "",
		}));
}

function templatesFromDrafts(drafts: TemplateDraft[]): Record<string, WorkflowTemplate> | undefined {
	if (drafts.length === 0) return undefined;
	const templates: Record<string, WorkflowTemplate> = {};
	for (const draft of drafts) {
		const name = draft.name.trim();
		if (name === "") continue;
		const tpl: WorkflowTemplate = {};
		if (draft.orchestrator.trim()) tpl.orchestrator = draft.orchestrator.trim();
		if (draft.worker.trim()) tpl.worker = draft.worker.trim();
		const reviewers = [draft.reviewer.trim(), ...draft.extraReviewers].filter((r) => r !== "");
		if (reviewers.length > 0) tpl.reviewers = reviewers;
		if (draft.orchestratorRulesFile.trim()) tpl.orchestratorRulesFile = draft.orchestratorRulesFile.trim();
		templates[name] = tpl;
	}
	return Object.keys(templates).length > 0 ? templates : undefined;
}

function unbindProfile(tpl: TemplateDraft, profile: string): TemplateDraft {
	return {
		...tpl,
		orchestrator: tpl.orchestrator.trim() === profile ? "" : tpl.orchestrator,
		worker: tpl.worker.trim() === profile ? "" : tpl.worker,
		reviewer: tpl.reviewer.trim() === profile ? "" : tpl.reviewer,
		extraReviewers: tpl.extraReviewers.filter((r) => r !== profile),
	};
}

function nextTemplateName(base: string, existing: TemplateDraft[]): string {
	const taken = new Set(existing.map((tpl) => tpl.name.trim()));
	const stem = base.trim() || "template";
	if (!taken.has(stem)) return stem;
	for (let n = 2; n < 1000; n += 1) {
		const candidate = `${stem}-${n}`;
		if (!taken.has(candidate)) return candidate;
	}
	return `${stem}-${Date.now()}`;
}

function validateTemplateDrafts(drafts: TemplateDraft[], profiles: ProfileDraft[], t: TFunction): string | null {
	const known = new Set(profiles.map((p) => p.name.trim()).filter((n) => n !== ""));
	const seen = new Set<string>();
	for (const draft of drafts) {
		const name = draft.name.trim();
		if (name === "") return t("settings.project.templateNameRequired");
		if (/[\/\\]/.test(name) || name === "." || name === "..") return t("settings.project.templateNameInvalid", { name });
		if (seen.has(name)) return t("settings.project.templateNameDuplicate", { name });
		seen.add(name);
		for (const profile of [draft.orchestrator, draft.worker, draft.reviewer, ...draft.extraReviewers]) {
			if (profile.trim() !== "" && !known.has(profile.trim())) {
				return t("settings.project.templateProfileUnknown", { name, profile: profile.trim() });
			}
		}
	}
	return null;
}

function projectKindLabel(kind: string, t: TFunction): string {
	switch (kind) {
		case "single_repo":
			return t("settings.project.kind.singleRepo");
		case "workspace":
			return t("settings.project.kind.workspace");
		case "scratch":
			return t("settings.project.kind.scratch");
		default:
			return kind || t("settings.project.kind.unknown");
	}
}

function repositoryHref(repository: string): string {
	if (/^https?:\/\//i.test(repository)) return repository;
	if (repository.startsWith("git@")) {
		const [host, path] = repository.slice(4).split(":", 2);
		return `https://${host}/${path.replace(/\.git$/, "")}`;
	}
	if (repository.startsWith("ssh://")) {
		try {
			const parsed = new URL(repository);
			return `https://${parsed.hostname}${parsed.pathname.replace(/\.git$/, "")}`;
		} catch {
			return repository;
		}
	}
	return repository;
}

function scratchSupportedConfig(config: ProjectConfig): ProjectConfig {
	const {
		defaultBranch: _defaultBranch,
		reviewers: _reviewers,
		autoReview: _legacyAutoReview,
		trackerIntake: _trackerIntake,
		...supported
	} = config as ProjectConfig;
	return supported;
}

function blankToUndefined<T extends object>(obj: T): T | undefined {
	return Object.values(obj).some((v) => v !== undefined) ? obj : undefined;
}

