import { Pencil, Plus, Trash2 } from "lucide-react";
import type { TFunction } from "i18next";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useAgentReadinessQuery } from "../../hooks/useAgentReadinessQuery";
import {
	useDefaultProfilesQuery,
	useDeleteRulesFile,
	useRulesFileQuery,
	useUpdateDefaultProfiles,
	useUpdateRulesFile,
	type DefaultProfiles,
} from "../../hooks/useDefaultProfilesQuery";
import { Button } from "../ui/button";
import { newProfileDraft, nextProfileName, ProfileCard, profileDraftsFromConfig, profilesFromDrafts, validateProfileDrafts, type ProfileDraft } from "./ProfileCard";
import { SettingsSection } from "./SettingsSection";

// The three rules files every session may read, in the order they are
// layered: the contract first, then the role's rules. Profile rules follow.
const FIXED_RULES_FILES = ["contract.md", "orchestrator.md", "worker.md"] as const;
const RULES_FILE_NAME = /^[A-Za-z0-9][A-Za-z0-9._-]*\.md$/;

/**
 * User-level default profiles and the daemon-wide rules files, under the
 * global Settings. Defaults sit under every project's own profiles (a
 * project profile of the same name wins); their rules files live in the
 * data dir's rules folder and are edited here in place.
 */
export function DefaultProfilesSection({ titleHidden }: { titleHidden?: boolean }) {
	const { t } = useTranslation();
	const query = useDefaultProfilesQuery();
	const update = useUpdateDefaultProfiles();
	const agentsQuery = useAgentReadinessQuery();
	const [drafts, setDrafts] = useState<ProfileDraft[] | null>(null);
	const [dirty, setDirty] = useState(false);
	const [validationError, setValidationError] = useState<string | null>(null);
	const [editing, setEditing] = useState<string | null>(null);

	// Drafts follow the daemon until the user edits; a save re-syncs them.
	useEffect(() => {
		if (query.data && !dirty) setDrafts(profileDraftsFromConfig(query.data.profiles ?? undefined));
	}, [query.data, dirty]);

	const list = drafts ?? [];
	const patch = (next: ProfileDraft[]) => { setDrafts(next); setDirty(true); setValidationError(null); };
	const save = () => {
		const error = validateProfileDrafts(list, t);
		if (error) { setValidationError(error); return; }
		update.mutate(profilesFromDrafts(list) ?? {}, { onSuccess: () => { setDirty(false); setValidationError(null); } });
	};
	const saveError = validationError ?? (update.error instanceof Error ? update.error.message : null);
	const loadError = query.error instanceof Error ? query.error.message : null;
	const rulesFiles = rulesFileRows(query.data, list, t);

	return (
		<SettingsSection title={t("settings.defaultProfiles.title")} sectionId="default-profiles" titleHidden={titleHidden}>
			<div className="flex flex-col gap-3">
				<p className="px-1 text-xs leading-row text-settings-muted">{t("settings.defaultProfiles.description")}</p>
				{loadError ? <p role="alert" className="px-1 text-xs leading-row text-error">{loadError}</p> : null}
				{query.isLoading ? <p className="px-1 text-xs leading-row text-settings-muted">{t("settings.defaultProfiles.loading")}</p> : null}
				{drafts && list.length === 0 ? <p className="px-1 text-xs leading-row text-settings-muted">{t("settings.defaultProfiles.empty")}</p> : null}
				{list.map((draft) => (
					<ProfileCard
						key={draft.key}
						draft={draft}
						profiles={list}
						projectId=""
						agents={agentsQuery.data?.agents}
						agentsLoading={agentsQuery.isFetching && agentsQuery.data === undefined}
						rulesFileHint={t("settings.defaultProfiles.rulesFileHint")}
						rulesFilePlaceholder="flash-coder.md"
						onChange={(next) => patch(list.map((p) => (p.key === draft.key ? { ...p, ...next } : p)))}
						onDuplicate={() => {
							const at = list.findIndex((p) => p.key === draft.key);
							const copy = { ...draft, key: newProfileDraft("").key, name: nextProfileName(draft.name, list) };
							patch([...list.slice(0, at + 1), copy, ...list.slice(at + 1)]);
						}}
						onDelete={() => patch(list.filter((p) => p.key !== draft.key).map((p) => (p.fallbackProfile.trim() === draft.name.trim() ? { ...p, fallbackProfile: "" } : p)))}
					/>
				))}
				<div className="flex items-center justify-between gap-2 px-1">
					<Button type="button" size="sm" variant="outline" disabled={!drafts} onClick={() => patch([...list, newProfileDraft(nextProfileName("profile", list))])}>
						<Plus className="size-3.5" aria-hidden="true" />
						{t("settings.defaultProfiles.addProfile")}
					</Button>
					<div className="flex items-center gap-2">
						{saveError ? <p role="alert" className="text-xs leading-row text-error">{saveError}</p> : null}
						{!saveError && !dirty && update.isSuccess ? <p role="status" className="text-xs leading-row text-settings-muted">{t("settings.defaultProfiles.saved")}</p> : null}
						<Button type="button" size="sm" disabled={!dirty || update.isPending} onClick={save}>
							{update.isPending ? t("settings.defaultProfiles.saving") : t("settings.defaultProfiles.save")}
						</Button>
					</div>
				</div>

				<h3 className="mt-2 px-1 text-sm font-medium text-foreground">{t("settings.defaultProfiles.rulesTitle")}</h3>
				<p className="px-1 text-xs leading-row text-settings-muted">{t("settings.defaultProfiles.rulesDescription")}</p>
				<div className="flex flex-col gap-1">
					{rulesFiles.map((file) => (
						<RulesFileRow
							key={file.name}
							name={file.name}
							label={file.label}
							exists={file.exists}
							sizeBytes={file.sizeBytes}
							editing={editing === file.name}
							onEdit={() => setEditing(file.name)}
							onClose={() => setEditing(null)}
						/>
					))}
				</div>
			</div>
		</SettingsSection>
	);
}

function rulesFileRows(data: DefaultProfiles | undefined, drafts: ProfileDraft[], t: TFunction) {
	const onDisk = new Map((data?.rulesFiles ?? []).map((file) => [file.name, file.sizeBytes]));
	const rows: { name: string; label: string; exists: boolean; sizeBytes: number }[] = [];
	const seen = new Set<string>();
	const push = (name: string, label: string) => {
		if (seen.has(name)) return;
		seen.add(name);
		rows.push({ name, label, exists: onDisk.has(name), sizeBytes: onDisk.get(name) ?? 0 });
	};
	push("contract.md", t("settings.defaultProfiles.rulesFileContract"));
	push("orchestrator.md", t("settings.defaultProfiles.rulesFileOrchestrator"));
	push("worker.md", t("settings.defaultProfiles.rulesFileWorker"));
	for (const draft of drafts) {
		const file = draft.rulesFile.trim();
		if (RULES_FILE_NAME.test(file)) push(file, t("settings.defaultProfiles.rulesFileProfile", { profile: draft.name.trim() || t("settings.project.profileUnnamed") }));
	}
	for (const name of onDisk.keys()) push(name, t("settings.defaultProfiles.rulesFileOther"));
	return rows;
}

function RulesFileRow({ name, label, exists, sizeBytes, editing, onEdit, onClose }: { name: string; label: string; exists: boolean; sizeBytes: number; editing: boolean; onEdit: () => void; onClose: () => void }) {
	const { t } = useTranslation();
	const fixed = (FIXED_RULES_FILES as readonly string[]).includes(name);
	return (
		<div className="rounded-md border border-border/70 px-2 py-2" data-testid="rules-file-row" data-rules-file={name}>
			<div className="flex items-center justify-between gap-2 px-1">
				<div className="min-w-0">
					<p className="text-sm font-medium text-foreground">{label}</p>
					<p className="truncate text-xs leading-row text-settings-muted">
						{name} · {exists ? t("settings.defaultProfiles.rulesSize", { value: String(sizeBytes) }) : t("settings.defaultProfiles.rulesNotCreated")}
					</p>
				</div>
				{!editing ? (
					<Button type="button" size="sm" variant="ghost" aria-label={t("settings.defaultProfiles.rulesEdit", { name })} onClick={onEdit}>
						<Pencil className="size-3.5" aria-hidden="true" />
						{t("settings.defaultProfiles.rulesEditLabel")}
					</Button>
				) : null}
			</div>
			{editing ? <RulesFileEditor name={name} deletable={!fixed || exists} onClose={onClose} /> : null}
		</div>
	);
}

function RulesFileEditor({ name, deletable, onClose }: { name: string; deletable: boolean; onClose: () => void }) {
	const { t } = useTranslation();
	const file = useRulesFileQuery(name, true);
	const save = useUpdateRulesFile();
	const remove = useDeleteRulesFile();
	const [content, setContent] = useState<string | null>(null);
	useEffect(() => {
		if (file.isSuccess && content === null) setContent(file.data?.content ?? "");
	}, [file.isSuccess, file.data, content]);
	const error = [file.error, save.error, remove.error].find((e) => e instanceof Error) as Error | undefined;
	const value = content ?? "";
	return (
		<div className="mt-2 flex flex-col gap-2 px-1">
			{file.isLoading ? <p className="text-xs leading-row text-settings-muted">{t("settings.defaultProfiles.rulesLoading")}</p> : null}
			<textarea
				aria-label={t("settings.defaultProfiles.rulesContentFor", { name })}
				className="settings-field-control min-h-48 w-full resize-y rounded-md! py-2.5 font-mono text-xs"
				disabled={file.isLoading || save.isPending}
				value={value}
				onChange={(event) => setContent(event.target.value)}
			/>
			{error ? <p role="alert" className="text-xs leading-row text-error">{error.message}</p> : null}
			<div className="flex items-center justify-end gap-2">
				{deletable ? (
					<Button type="button" size="sm" variant="ghost" disabled={remove.isPending} aria-label={t("settings.defaultProfiles.rulesDelete", { name })} onClick={() => remove.mutate(name, { onSuccess: onClose })}>
						<Trash2 className="size-3.5" aria-hidden="true" />
						{t("settings.defaultProfiles.rulesDeleteLabel")}
					</Button>
				) : null}
				<Button type="button" size="sm" variant="outline" onClick={onClose}>{t("settings.defaultProfiles.rulesCancel")}</Button>
				<Button type="button" size="sm" disabled={file.isLoading || save.isPending || content === null} onClick={() => save.mutate({ name, content: value }, { onSuccess: onClose })}>
					{save.isPending ? t("settings.defaultProfiles.saving") : t("settings.defaultProfiles.rulesSave")}
				</Button>
			</div>
		</div>
	);
}
