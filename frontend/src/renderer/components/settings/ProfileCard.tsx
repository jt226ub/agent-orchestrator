import { Copy, Trash2 } from "lucide-react";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import type { components } from "../../../api/schema";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { AgentModelField, PermissionModeSelect, buildRoleAgentConfig } from "./AgentModelField";
import { SettingsOptionMenu } from "./SettingsOptionMenu";
import { SettingsRow } from "./SettingsRow";

type RoleProfile = components["schemas"]["RoleProfile"];

// A profile as edited on the Profiles tab: one card per entry, env as KEY=VALUE lines.
export type ProfileDraft = {
	key: string;
	name: string;
	agent: string;
	model: string;
	permissions: string;
	rulesFile: string;
	env: string;
	// Quota admission (agy profiles): percentages as typed, "" for unset.
	warnBelowPercent: string;
	refuseBelowPercent: string;
	fallbackProfile: string;
};

/** A blank profile draft with the given name, as the Add profile buttons create. */
export function newProfileDraft(name: string): ProfileDraft {
	return { key: newProfileKey(), name, agent: "", model: "", permissions: "", rulesFile: "", env: "", warnBelowPercent: "", refuseBelowPercent: "", fallbackProfile: "" };
}

export function ProfileCard({
	draft,
	profiles,
	projectId,
	agents,
	agentsLoading,
	rulesFileHint,
	rulesFilePlaceholder,
	onChange,
	onDuplicate,
	onDelete,
}: {
	draft: ProfileDraft;
	profiles: ProfileDraft[];
	projectId: string;
	agents?: components["schemas"]["AgentReadinessSnapshot"][];
	agentsLoading: boolean;
	/** Replaces the repo-relative wording when the profile's rules file lives elsewhere. */
	rulesFileHint?: string;
	rulesFilePlaceholder?: string;
	onChange: (patch: Partial<ProfileDraft>) => void;
	onDuplicate: () => void;
	onDelete: () => void;
}) {
	const { t } = useTranslation();
	const name = draft.name.trim() || t("settings.project.profileUnnamed");
	return (
		<div className="flex flex-col gap-1 rounded-md border border-border/70 px-2 py-2" data-testid="profile-card">
			<div className="flex items-center justify-between gap-2 px-1">
				<span className="text-sm font-medium text-foreground">{name}</span>
				<div className="flex items-center gap-1">
					<Button type="button" size="sm" variant="ghost" aria-label={t("settings.project.duplicateProfile", { name })} onClick={onDuplicate}>
						<Copy className="size-3.5" aria-hidden="true" />
					</Button>
					<Button type="button" size="sm" variant="ghost" aria-label={t("settings.project.deleteProfile", { name })} onClick={onDelete}>
						<Trash2 className="size-3.5" aria-hidden="true" />
					</Button>
				</div>
			</div>
			<SettingsRow label={t("settings.project.profileName")}>
				<Input
					aria-label={t("settings.project.profileNameFor", { name })}
					className="max-w-64 text-right"
					value={draft.name}
					onChange={(event) => onChange({ name: event.target.value })}
				/>
			</SettingsRow>
			<SettingsRow label={t("settings.project.profileAgent")}>
				<SettingsOptionMenu
					aria-label={t("settings.project.profileAgentFor", { name })}
					value={draft.agent || "__inherit__"}
					options={[
						{ value: "__inherit__", label: t("settings.project.profileAgentInherit") },
						...(agents ?? []).map((agent) => ({ value: agent.id, label: agent.label })),
						...(draft.agent && !(agents ?? []).some((agent) => agent.id === draft.agent) ? [{ value: draft.agent, label: draft.agent }] : []),
					]}
					disabled={agentsLoading}
					onChange={(agent) => onChange({ agent: agent === "__inherit__" ? "" : agent, model: "" })}
				/>
			</SettingsRow>
			{draft.agent ? (
				<AgentModelField
					role="worker"
					agentId={draft.agent}
					projectId={projectId}
					model={draft.model}
					mode=""
					effort=""
					labelOverride={t("settings.project.profileModel")}
					onModelChange={(model) => onChange({ model })}
					onModeChange={() => undefined}
					onEffortChange={() => undefined}
					onValidityChange={() => undefined}
				/>
			) : null}
			<SettingsRow label={t("settings.project.profileApproval")}>
				<PermissionModeSelect
					ariaLabel={t("settings.project.profileApprovalFor", { name })}
					value={draft.permissions}
					onChange={(permissions) => onChange({ permissions })}
				/>
			</SettingsRow>
			<SettingsRow label={t("settings.project.profileRulesFile")} description={rulesFileHint ?? t("settings.project.profileRulesFileHint")}>
				<Input
					aria-label={t("settings.project.profileRulesFileFor", { name })}
					className="max-w-64 text-right"
					placeholder={rulesFilePlaceholder ?? "rules/flash-coder.md"}
					value={draft.rulesFile}
					onChange={(event) => onChange({ rulesFile: event.target.value })}
				/>
			</SettingsRow>
			<SettingsRow label={t("settings.project.profileEnv")} description={t("settings.project.profileEnvHint")}>
				<textarea
					aria-label={t("settings.project.profileEnvFor", { name })}
					className="min-h-16 w-full max-w-64 rounded-md border border-transparent bg-input/50 px-3 py-1 font-mono text-xs text-foreground outline-none placeholder:text-muted-foreground focus-visible:ring-1 focus-visible:ring-ring"
					placeholder="KEY=VALUE"
					value={draft.env}
					onChange={(event) => onChange({ env: event.target.value })}
				/>
			</SettingsRow>
			{draft.agent === "agy" ? (
				<>
					<SettingsRow label={t("settings.project.profileQuotaWarn")} description={t("settings.project.profileQuotaHint")}>
						<Input
							aria-label={t("settings.project.profileQuotaWarnFor", { name })}
							className="max-w-24 text-right"
							inputMode="numeric"
							placeholder="20"
							value={draft.warnBelowPercent}
							onChange={(event) => onChange({ warnBelowPercent: event.target.value })}
						/>
					</SettingsRow>
					<SettingsRow label={t("settings.project.profileQuotaRefuse")}>
						<Input
							aria-label={t("settings.project.profileQuotaRefuseFor", { name })}
							className="max-w-24 text-right"
							inputMode="numeric"
							placeholder="0"
							value={draft.refuseBelowPercent}
							onChange={(event) => onChange({ refuseBelowPercent: event.target.value })}
						/>
					</SettingsRow>
					<SettingsRow label={t("settings.project.profileFallback")} description={t("settings.project.profileFallbackHint")}>
						<SettingsOptionMenu
							aria-label={t("settings.project.profileFallbackFor", { name })}
							value={draft.fallbackProfile || "__none__"}
							options={[
								{ value: "__none__", label: t("settings.project.profileNone") },
								...profiles
									.map((p) => p.name.trim())
									.filter((other) => other !== "" && other !== draft.name.trim())
									.map((other) => ({ value: other, label: other })),
							]}
							onChange={(fallbackProfile) => onChange({ fallbackProfile: fallbackProfile === "__none__" ? "" : fallbackProfile })}
						/>
					</SettingsRow>
				</>
			) : null}
		</div>
	);
}

let profileKeyCounter = 0;
export function newProfileKey(): string {
	profileKeyCounter += 1;
	return `profile-${Date.now().toString(36)}-${profileKeyCounter}`;
}

export function nextProfileName(base: string, existing: ProfileDraft[]): string {
	const taken = new Set(existing.map((p) => p.name.trim()));
	const stem = base.trim() || "profile";
	if (!taken.has(stem)) return stem;
	for (let n = 2; n < 1000; n += 1) {
		const candidate = `${stem}-${n}`;
		if (!taken.has(candidate)) return candidate;
	}
	return `${stem}-${Date.now()}`;
}

export function profileDraftsFromConfig(profiles: Record<string, RoleProfile> | undefined): ProfileDraft[] {
	return Object.entries(profiles ?? {})
		.sort(([a], [b]) => a.localeCompare(b))
		.map(([name, profile]) => ({
			key: newProfileKey(),
			name,
			agent: profile.agent ?? "",
			model: profile.agentConfig?.model ?? "",
			permissions: profile.agentConfig?.permissions ?? "",
			rulesFile: profile.rulesFile ?? "",
			env: Object.entries(profile.env ?? {})
				.sort(([a], [b]) => a.localeCompare(b))
				.map(([k, v]) => `${k}=${v}`)
				.join("\n"),
			warnBelowPercent: profile.quota?.warnBelowPercent !== undefined ? String(profile.quota.warnBelowPercent) : "",
			refuseBelowPercent: profile.quota ? String(profile.quota.refuseBelowPercent ?? 0) : "",
			fallbackProfile: profile.fallback?.profile ?? "",
		}));
}

// parseEnvLines turns KEY=VALUE lines into a map; blank lines are skipped and a
// line without "=" keeps the whole text as the key with an empty value so the
// daemon's validation, not silent dropping, reports it.
function parseEnvLines(text: string): Record<string, string> | undefined {
	const env: Record<string, string> = {};
	for (const raw of text.split("\n")) {
		const line = raw.trim();
		if (line === "") continue;
		const at = line.indexOf("=");
		if (at < 0) env[line] = "";
		else env[line.slice(0, at).trim()] = line.slice(at + 1);
	}
	return Object.keys(env).length > 0 ? env : undefined;
}

export function profilesFromDrafts(drafts: ProfileDraft[]): Record<string, RoleProfile> | undefined {
	if (drafts.length === 0) return undefined;
	const profiles: Record<string, RoleProfile> = {};
	for (const draft of drafts) {
		const name = draft.name.trim();
		if (name === "") continue;
		const agentConfig = buildRoleAgentConfig(undefined, draft.model.trim(), "", "", draft.permissions);
		const profile: RoleProfile = {};
		if (draft.agent) profile.agent = draft.agent as RoleProfile["agent"];
		if (agentConfig) profile.agentConfig = agentConfig;
		if (draft.rulesFile.trim()) profile.rulesFile = draft.rulesFile.trim();
		const env = parseEnvLines(draft.env);
		if (env) profile.env = env;
		const warn = draft.warnBelowPercent.trim();
		const refuse = draft.refuseBelowPercent.trim();
		if (warn !== "" || refuse !== "") {
			profile.quota = {
				...(warn !== "" ? { warnBelowPercent: Number(warn) } : {}),
				...(refuse !== "" ? { refuseBelowPercent: Number(refuse) } : {}),
			};
		}
		if (draft.fallbackProfile.trim()) profile.fallback = { profile: draft.fallbackProfile.trim() };
		profiles[name] = profile;
	}
	return Object.keys(profiles).length > 0 ? profiles : undefined;
}

export function validateProfileDrafts(drafts: ProfileDraft[], t: TFunction): string | null {
	const seen = new Set<string>();
	for (const draft of drafts) {
		const name = draft.name.trim();
		if (name === "") return t("settings.project.profileNameRequired");
		if (/[\/\\]/.test(name) || name === "." || name === "..") return t("settings.project.profileNameInvalid", { name });
		if (seen.has(name)) return t("settings.project.profileNameDuplicate", { name });
		seen.add(name);
		for (const value of [draft.warnBelowPercent, draft.refuseBelowPercent]) {
			const text = value.trim();
			if (text === "") continue;
			const n = Number(text);
			if (!Number.isFinite(n) || n < 0 || n > 100) return t("settings.project.profileQuotaInvalid", { name });
		}
		if (draft.fallbackProfile.trim() !== "" && draft.warnBelowPercent.trim() === "" && draft.refuseBelowPercent.trim() === "") {
			return t("settings.project.profileFallbackNeedsQuota", { name });
		}
	}
	return null;
}
