import { useMutation, useQuery } from "@tanstack/react-query";
import { ProjectSettingsFormView, ProjectSettingsSection } from "@aoagents/product-ui";
import type { TFunction } from "i18next";
import { Copy, Plus, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { components } from "../../api/schema";
import { useAgentReadinessQuery } from "../hooks/useAgentReadinessQuery";
import { useDefaultProfilesQuery } from "../hooks/useDefaultProfilesQuery";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import { clientForHost } from "../lib/host-clients";
import type { ProjectSettingsSaveState } from "./ProjectSettingsForm";
import { ProfileCard, newProfileDraft, newProfileKey, nextProfileName, profileDraftsFromConfig, profilesFromDrafts, validateProfileDrafts, type ProfileDraft } from "./settings/ProfileCard";
import { SettingsOptionMenu } from "./settings/SettingsOptionMenu";
import { SettingsRow } from "./settings/SettingsRow";
import { Button } from "./ui/button";
import { Input } from "./ui/input";

type Project = components["schemas"]["Project"];
type ProjectConfig = components["schemas"]["ProjectConfig"];
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

type ProfilesDraft = {
	workerProfile: string;
	orchestratorProfile: string;
	reviewerProfile: string;
	reviewerHarness: string;
	profiles: ProfileDraft[];
	templates: TemplateDraft[];
	template: string;
};

// The fork's Profiles and Templates tabs. Upstream's ProjectSettingsEditor owns
// General and Agents; these tabs edit only the profile fields of the project
// config (role profile slots, profiles, templates, the active template) and
// save the rest of the config untouched, with the same autosave and save-state
// protocol as the editor.
export function ProjectProfilesSettings({ project, projectId, hostId, section, onSaved, onSaveState }: {
	project: Project;
	projectId: string;
	hostId?: string;
	section: "profiles" | "templates";
	onSaved: () => Promise<void> | void;
	onSaveState?: (state: ProjectSettingsSaveState) => void;
}) {
	const { t } = useTranslation();
	const config = project.config ?? {};
	const isScratchProject = project.kind === "scratch";
	const initial: ProfilesDraft = {
		workerProfile: config.worker?.profile ?? "",
		orchestratorProfile: config.orchestrator?.profile ?? "",
		reviewerProfile: config.reviewers?.[0]?.profile ?? "",
		reviewerHarness: config.reviewers?.[0]?.harness ?? "",
		profiles: profileDraftsFromConfig(config.profiles),
		templates: templateDraftsFromConfig(config.templates),
		template: config.template ?? "",
	};
	const [form, setForm] = useState(initial);
	const saved = useRef(JSON.stringify(initial));
	const failedKey = useRef<string | undefined>(undefined);
	const [error, setError] = useState<string>();
	const [didSave, setDidSave] = useState(false);
	const defaultProfiles = useDefaultProfilesQuery().data?.profiles ?? {};
	const agentsQuery = useAgentReadinessQuery(true, hostId);
	const agentCatalog = agentsQuery.data;
	// Save on top of the latest saved config, so an edit made on another tab in
	// the meantime is not overwritten.
	const latest = useQuery({
		queryKey: hostId ? ["project-config", hostId, projectId] : ["project-config", projectId],
		queryFn: async () => {
			const { data, error } = await (hostId ? clientForHost(hostId) : apiClient).GET("/api/v1/projects/{id}", { params: { path: { id: projectId } } });
			if (error) throw new Error(apiErrorMessage(error));
			return (data?.project as Project | undefined)?.config ?? {};
		},
	});
	const mutation = useMutation({
		mutationFn: async (values: ProfilesDraft) => {
			const base: ProjectConfig = latest.data ?? config;
			const reviewers = base.reviewers ?? [];
			const next: ProjectConfig = {
				...base,
				worker: { ...base.worker, profile: values.workerProfile || undefined },
				orchestrator: { ...base.orchestrator, profile: values.orchestratorProfile || undefined },
				profiles: profilesFromDrafts(values.profiles),
				templates: templatesFromDrafts(values.templates),
				template: values.template || undefined,
				...(isScratchProject || (!values.reviewerHarness && reviewers.length === 0)
					? {}
					: {
							reviewers: values.reviewerHarness
								? [
										{
											...(reviewers[0]?.harness === values.reviewerHarness ? reviewers[0] : {}),
											harness: values.reviewerHarness,
											profile: values.reviewerProfile || undefined,
										},
										...reviewers.slice(1),
									]
								: reviewers,
						}),
			};
			const { error } = await (hostId ? clientForHost(hostId) : apiClient).PUT("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
				body: { displayName: project.name, config: next },
			});
			if (error) throw new Error(apiErrorMessage(error));
		},
		onSuccess: (_result, values) => {
			saved.current = JSON.stringify(values);
			failedKey.current = undefined;
			setError(undefined);
			setDidSave(true);
			void latest.refetch();
			void onSaved();
		},
		onError: (cause, values) => {
			failedKey.current = JSON.stringify(values);
			setError(cause instanceof Error ? cause.message : t("settings.project.saveFailed"));
		},
	});
	const dirty = JSON.stringify(form) !== saved.current;
	// Validation runs on every submit, explicit or autosave, so a saved config
	// that is already invalid (a template naming a deleted profile) is reported.
	const submit = () => {
		if (mutation.isPending) return;
		const problem = validateProfileDrafts(form.profiles, t) ?? validateTemplateDrafts(form.templates, form.profiles, t);
		setError(problem ?? undefined);
		if (problem || !dirty) return;
		setDidSave(false);
		mutation.mutate(form);
	};
	useEffect(() => {
		if (!dirty || mutation.isPending || JSON.stringify(form) === failedKey.current) return;
		const timeout = window.setTimeout(submit, 650);
		return () => window.clearTimeout(timeout);
	}, [dirty, form, mutation.isPending]);
	useEffect(() => {
		onSaveState?.({ phase: error ? "failed" : mutation.isPending ? "saving" : dirty ? "pending" : didSave ? "saved" : "idle", dirty, requestPending: mutation.isPending, error });
	}, [didSave, dirty, error, mutation.isPending, onSaveState]);
	useEffect(() => {
		if (!didSave) return;
		const timeout = window.setTimeout(() => setDidSave(false), 1800);
		return () => window.clearTimeout(timeout);
	}, [didSave]);

	return (
		<ProjectSettingsFormView id="project-settings-form" className="project-settings-form gap-5" onSubmit={submit}>
			{section === "profiles" && (
				<ProjectSettingsSection title={t("settings.project.roleProfiles")} grouped>
					<ProfileRoleRow
						label={t("settings.project.workerProfile")}
						value={form.workerProfile}
						explicitAgent={config.worker?.agent ?? ""}
						profiles={form.profiles}
						defaultProfiles={defaultProfiles}
						agents={agentCatalog?.agents}
						onChange={(workerProfile) => setForm((f) => ({ ...f, workerProfile }))}
					/>
					<ProfileRoleRow
						label={t("settings.project.orchestratorProfile")}
						value={form.orchestratorProfile}
						explicitAgent={config.orchestrator?.agent ?? ""}
						profiles={form.profiles}
						defaultProfiles={defaultProfiles}
						agents={agentCatalog?.agents}
						onChange={(orchestratorProfile) => setForm((f) => ({ ...f, orchestratorProfile }))}
					/>
				</ProjectSettingsSection>
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
			{error && !onSaveState ? <p role="alert" className="px-1 text-sm text-error">{error}</p> : null}
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

