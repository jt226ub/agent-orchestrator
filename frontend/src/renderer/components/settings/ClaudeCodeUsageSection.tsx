import { CircleAlert, LoaderCircle, RotateCcw } from "lucide-react";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { claudeCodeUsageReasonKey, formatClaudeCodePlanName } from "../../hooks/claude-code-usage-state";
import { useClaudeCodeUsageQuery, useEnsureClaudeCodeUsage, useRefreshClaudeCodeUsage, type ClaudeCodeUsage } from "../../hooks/useClaudeCodeUsageQuery";
import { Button } from "../ui/button";
import { AgentProviderGroup } from "./AgentProviderGroup";
import { formatPercentage } from "./CodexAccountDetails";

type UsageWindow = ClaudeCodeUsage["windows"][number];

const GENERAL_WINDOW_IDS = new Set(["five_hour", "seven_day"]);

/**
 * The device's Claude Code subscription beside the Codex and Antigravity
 * accounts: who is signed in, the plan, and its 5-hour, weekly and per-model
 * limits, read the way the CLI's own `/usage` screen reads them. Advisory
 * only; nothing here gates a launch.
 */
export function ClaudeCodeUsageSection() {
	const { t, i18n } = useTranslation();
	const usageQuery = useClaudeCodeUsageQuery();
	useEnsureClaudeCodeUsage();
	const { refresh, pending, error: refreshError } = useRefreshClaudeCodeUsage();
	const [expanded, setExpanded] = useState(true);
	const usage = usageQuery.data;
	const queryError = usageQuery.error instanceof Error ? usageQuery.error.message : null;
	const checking = usage?.freshness === "checking" || pending;
	const identity = usage?.identity?.emailAddress || usage?.identity?.displayName || null;
	const summaryParts = usage && usage.state !== "unsupported" ? [identity, usage.remainingPercent != null ? t("settings.claudeCodeUsage.percentLeft", { value: formatPercentage(usage.remainingPercent, i18n.language) }) : null].filter(Boolean) : [];
	const summary = summaryParts.length > 0 ? summaryParts.join(" · ") : usage ? t(claudeCodeUsageReasonKey(usage.reasonCode)) : undefined;
	const generalWindows = usage?.windows.filter((window) => GENERAL_WINDOW_IDS.has(window.id)) ?? [];
	const modelWindows = usage?.windows.filter((window) => !GENERAL_WINDOW_IDS.has(window.id)) ?? [];
	const notice = usageNoticeFor(usage, t, i18n.language);

	return (
		<AgentProviderGroup
			provider="claude-code"
			name={t("settings.claudeCodeUsage.name")}
			summary={summary}
			expanded={expanded}
			onExpandedChange={setExpanded}
			action={<Button type="button" size="sm" variant="outline" disabled={checking} onClick={() => void refresh()}>
				{checking ? <LoaderCircle className="size-3.5 animate-spin" aria-hidden="true" /> : <RotateCcw className="size-3.5" aria-hidden="true" />}
				{t("settings.claudeCodeUsage.refresh")}
			</Button>}
		>
			<div className="space-y-5 px-4 py-4 text-xs">
				{queryError || refreshError ? <UsageNotice reason={queryError ?? refreshError ?? ""} tone="error" /> : null}
				{notice ? <UsageNotice {...notice} /> : null}
				{usage && usage.state !== "unsupported" ? <PlanCard usage={usage} locale={i18n.language} /> : null}
				{generalWindows.length > 0 ? <UsageWindowGroup title={t("settings.claudeCodeUsage.generalUsageLimits")} windows={generalWindows} locale={i18n.language} /> : null}
				{modelWindows.length > 0 ? <UsageWindowGroup title={t("settings.claudeCodeUsage.modelUsageLimits")} windows={modelWindows} locale={i18n.language} /> : null}
				{usage && usage.state !== "unsupported" && usage.windows.length === 0 && usage.freshness !== "checking" ? <p className="text-muted-foreground">{t("settings.claudeCodeUsage.usageDetailsUnavailable")}</p> : null}
			</div>
		</AgentProviderGroup>
	);
}

function usageNoticeFor(usage: ClaudeCodeUsage | undefined, t: TFunction, locale: string): { reason: string; tone: "warning" | "error" | "muted"; checking?: boolean } | null {
	if (!usage) return null;
	if (usage.freshness === "checking") return { reason: t(claudeCodeUsageReasonKey(usage.reasonCode)), tone: "muted", checking: true };
	if (usage.state === "unsupported" || usage.reasonCode === "plan_usage_signed_out") {
		return { reason: t(claudeCodeUsageReasonKey(usage.reasonCode)), tone: "warning" };
	}
	if (usage.freshness === "stale") {
		const checked = usage.checkedAt ? formatObservedTime(usage.checkedAt, locale) : "";
		return { reason: checked ? t("settings.claudeCodeUsage.usageStaleChecked", { value: checked }) : t("settings.claudeCodeUsage.usageStale"), tone: usage.checkedAt ? "muted" : "warning" };
	}
	return null;
}

function UsageNotice({ reason, tone, checking }: { reason: string; tone: "warning" | "error" | "muted"; checking?: boolean }) {
	const { t } = useTranslation();
	const color = tone === "error" ? "border-error/30 bg-error/8 text-error" : tone === "warning" ? "border-warning/30 bg-warning/10 text-warning" : "border-border bg-muted/20 text-muted-foreground";
	return <p className={`flex items-start gap-2 rounded-md border px-3 py-2.5 leading-5 ${color}`} role={tone === "error" ? "alert" : "status"}>{checking ? <LoaderCircle className="mt-0.5 size-3.5 shrink-0 animate-spin" aria-label={t("settings.claudeCodeUsage.checking")} /> : <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />}<span>{reason}</span></p>;
}

function PlanCard({ usage, locale }: { usage: ClaudeCodeUsage; locale: string }) {
	const { t } = useTranslation();
	const plan = formatPlanLabel(usage.plan, t);
	const account = [usage.identity?.emailAddress, usage.identity?.organizationName].filter(Boolean).join(" · ");
	const promotion = usage.promotion ? formatPromotion(usage.promotion, locale) : null;
	return <section>
		<h4 className="mb-2 font-medium text-foreground">{t("settings.claudeCodeUsage.yourPlan")}</h4>
		<div className="flex flex-wrap items-center justify-between gap-4 rounded-md border border-border/70 bg-muted/15 px-3.5 py-3">
			<div className="min-w-0">
				<p className={`text-sm font-medium ${plan ? "text-foreground" : "text-muted-foreground"}`}>{plan ?? t("settings.claudeCodeUsage.planUnavailable")}</p>
				{account ? <p className="mt-0.5 truncate text-muted-foreground" title={account}>{account}</p> : null}
			</div>
			{promotion ? <p className="font-medium text-foreground">{t("settings.claudeCodeUsage.boostSummary", promotion)}</p> : null}
		</div>
	</section>;
}

function UsageWindowGroup({ title, windows, locale }: { title: string; windows: UsageWindow[]; locale: string }) {
	return <section><h4 className="mb-2 font-medium text-foreground">{title}</h4><div className="divide-y divide-border/70 overflow-hidden rounded-md border border-border/70 bg-muted/15">{windows.map((window) => <UsageWindowRow key={window.id} window={window} locale={locale} />)}</div></section>;
}

function UsageWindowRow({ window, locale }: { window: UsageWindow; locale: string }) {
	const { t } = useTranslation();
	const remaining = Math.max(0, Math.min(100, 100 - window.usedPercent));
	const percentage = formatPercentage(remaining, locale);
	const reset = formatResetTime(window.resetsAt, locale);
	const tone = remaining <= 0 ? "exhausted" : remaining <= 25 ? "near" : "available";
	const fillClass = tone === "exhausted" ? "bg-error" : tone === "near" ? "bg-warning" : "bg-foreground/80";
	const valueClass = tone === "exhausted" ? "text-error" : tone === "near" ? "text-warning" : "text-muted-foreground";
	return <div className="grid gap-2 px-3.5 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(8rem,11rem)_auto] sm:items-center sm:gap-4"><div className="min-w-0"><p className="font-medium text-foreground">{window.displayName}</p>{reset ? <p className="mt-0.5 text-muted-foreground" title={reset.full}>{t("settings.claudeCodeUsage.resets", { value: reset.visible })}</p> : null}</div><div role="progressbar" aria-label={t("settings.claudeCodeUsage.remainingForLimit", { label: window.displayName, value: percentage })} aria-valuemin={0} aria-valuemax={100} aria-valuenow={remaining} className="h-1.5 w-full overflow-hidden rounded-full bg-muted"><div className={`h-full rounded-full transition-[width] ${fillClass}`} style={{ width: `${remaining}%` }} /></div><p className={`whitespace-nowrap text-right tabular-nums ${valueClass}`}>{t("settings.claudeCodeUsage.percentLeft", { value: percentage })}</p></div>;
}

function formatPlanLabel(plan: string | null | undefined, t: TFunction): string | null {
	const name = formatClaudeCodePlanName(plan);
	return name ? (/\bplan$/i.test(name) ? name : t("settings.claudeCodeUsage.planLabel", { name })) : null;
}

function formatPromotion(promotion: NonNullable<ClaudeCodeUsage["promotion"]>, locale: string): { percent: string; date: string } | null {
	const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(promotion.endsOn);
	if (!match) return null;
	const date = new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]));
	if (Number.isNaN(date.getTime())) return null;
	return { percent: formatPercentage(promotion.percentIncrease, locale), date: new Intl.DateTimeFormat(locale, { day: "numeric", month: "short" }).format(date) };
}

function formatResetTime(value: string | null | undefined, locale: string): { visible: string; full: string } | null {
	if (!value) return null;
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) return null;
	const now = new Date();
	const sameDay = date.getFullYear() === now.getFullYear() && date.getMonth() === now.getMonth() && date.getDate() === now.getDate();
	return {
		visible: new Intl.DateTimeFormat(locale, sameDay ? { hour: "2-digit", minute: "2-digit" } : { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }).format(date),
		full: new Intl.DateTimeFormat(locale, { dateStyle: "full", timeStyle: "long" }).format(date),
	};
}

function formatObservedTime(value: string, locale: string): string {
	const date = new Date(value);
	return Number.isNaN(date.getTime()) ? "" : new Intl.DateTimeFormat(locale, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }).format(date);
}
