import { CircleAlert, LoaderCircle, RotateCcw } from "lucide-react";
import type { TFunction } from "i18next";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { agyCapacityReasonKey } from "../../hooks/agy-capacity-state";
import { useAgyCapacityQuery, useEnsureAgyCapacity, useRefreshAgyCapacity, type AgyCapacity } from "../../hooks/useAgyCapacityQuery";
import { Button } from "../ui/button";
import { AgentProviderGroup } from "./AgentProviderGroup";
import { formatPercentage } from "./CodexAccountDetails";

type CapacityBucketValue = NonNullable<AgyCapacity["overall"]>;
type CapacityWindowValue = NonNullable<CapacityBucketValue["primary"]>;

/**
 * Antigravity plan capacity beside the Codex accounts: the signed-in Google
 * account's 5-hour and weekly limits per model group, read from the CLI's own
 * `/usage` command. Advisory only; nothing here gates a launch.
 */
export function AgyCapacitySection() {
	const { t, i18n } = useTranslation();
	const capacityQuery = useAgyCapacityQuery();
	useEnsureAgyCapacity();
	const { refresh, pending, error: refreshError } = useRefreshAgyCapacity();
	const [expanded, setExpanded] = useState(true);
	const capacity = capacityQuery.data;
	const queryError = capacityQuery.error instanceof Error ? capacityQuery.error.message : null;
	const checking = capacity?.freshness === "checking" || pending;
	const remaining = capacity?.remainingPercent;
	const summary = capacity && capacity.state !== "unsupported" && remaining != null
		? t("settings.agyCapacity.percentLeft", { value: formatPercentage(remaining, i18n.language) })
		: capacity ? t(agyCapacityReasonKey(capacity.reasonCode)) : undefined;
	const buckets = capacity ? [capacity.overall, ...capacity.additionalBuckets].filter((bucket): bucket is CapacityBucketValue => Boolean(bucket && (bucket.primary || bucket.secondary))) : [];
	const notice = capacityNoticeFor(capacity, t, i18n.language);

	return (
		<AgentProviderGroup
			provider="agy"
			name={t("settings.agyCapacity.name")}
			summary={summary}
			expanded={expanded}
			onExpandedChange={setExpanded}
			action={<Button type="button" size="sm" variant="outline" disabled={checking} onClick={() => void refresh()}>
				{checking ? <LoaderCircle className="size-3.5 animate-spin" aria-hidden="true" /> : <RotateCcw className="size-3.5" aria-hidden="true" />}
				{t("settings.agyCapacity.refresh")}
			</Button>}
		>
			<div className="space-y-5 px-4 py-4 text-xs">
				{queryError || refreshError ? <CapacityNotice reason={queryError ?? refreshError ?? ""} tone="error" /> : null}
				{notice ? <CapacityNotice {...notice} /> : null}
				{buckets.map((bucket, index) => (
					<CapacityBucketGroup key={`${bucket.displayName ?? "bucket"}-${index}`} bucket={bucket} title={bucket.displayName ? t("settings.agyCapacity.namedUsageLimits", { name: bucket.displayName }) : t("settings.agyCapacity.usageLimits")} locale={i18n.language} />
				))}
				{capacity && buckets.length === 0 && capacity.freshness !== "checking" ? <p className="text-muted-foreground">{t("settings.agyCapacity.usageDetailsUnavailable")}</p> : null}
			</div>
		</AgentProviderGroup>
	);
}

function capacityNoticeFor(capacity: AgyCapacity | undefined, t: TFunction, locale: string): { reason: string; tone: "warning" | "error" | "muted"; checking?: boolean } | null {
	if (!capacity) return null;
	if (capacity.freshness === "checking") return { reason: t(agyCapacityReasonKey(capacity.reasonCode)), tone: "muted", checking: true };
	if (capacity.state === "unsupported" || capacity.reasonCode === "capacity_not_installed" || capacity.reasonCode === "capacity_skipped_signed_out") {
		return { reason: t(agyCapacityReasonKey(capacity.reasonCode)), tone: "warning" };
	}
	if (capacity.freshness === "stale") {
		const checked = capacity.checkedAt ? formatObservedTime(capacity.checkedAt, locale) : "";
		return { reason: checked ? t("settings.agyCapacity.capacityStaleChecked", { value: checked }) : t("settings.agyCapacity.capacityStale"), tone: capacity.checkedAt ? "muted" : "warning" };
	}
	if (capacity.state === "exhausted") return { reason: t(agyCapacityReasonKey(capacity.reasonCode)), tone: "error" };
	if (capacity.state === "near_limit") return { reason: t(agyCapacityReasonKey(capacity.reasonCode)), tone: "warning" };
	return null;
}

function CapacityNotice({ reason, tone, checking }: { reason: string; tone: "warning" | "error" | "muted"; checking?: boolean }) {
	const { t } = useTranslation();
	const color = tone === "error" ? "border-error/30 bg-error/8 text-error" : tone === "warning" ? "border-warning/30 bg-warning/10 text-warning" : "border-border bg-muted/20 text-muted-foreground";
	return <p className={`flex items-start gap-2 rounded-md border px-3 py-2.5 leading-5 ${color}`} role={tone === "error" ? "alert" : "status"}>{checking ? <LoaderCircle className="mt-0.5 size-3.5 shrink-0 animate-spin" aria-label={t("settings.agyCapacity.checking")} /> : <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />}<span>{reason}</span></p>;
}

function CapacityBucketGroup({ bucket, title, locale }: { bucket: CapacityBucketValue; title: string; locale: string }) {
	const { t } = useTranslation();
	const windows = [bucket.primary, bucket.secondary].filter((window): window is CapacityWindowValue => Boolean(window));
	return <section><h4 className="mb-2 font-medium text-foreground">{title}</h4><div className="divide-y divide-border/70 overflow-hidden rounded-md border border-border/70 bg-muted/15">{windows.map((window, index) => <CapacityWindowRow key={`${index}-${window.windowDurationMinutes ?? "window"}`} window={window} label={windowLabel(window, t)} locale={locale} />)}</div></section>;
}

function windowLabel(window: CapacityWindowValue, t: TFunction): string {
	if (window.windowDurationMinutes === 300) return t("settings.agyCapacity.fiveHourUsageLimit");
	if (window.windowDurationMinutes === 10080) return t("settings.agyCapacity.weeklyUsageLimit");
	return t("settings.agyCapacity.usageLimit");
}

function CapacityWindowRow({ window, label, locale }: { window: CapacityWindowValue; label: string; locale: string }) {
	const { t } = useTranslation();
	const remaining = Math.max(0, Math.min(100, 100 - window.usedPercent));
	const percentage = formatPercentage(remaining, locale);
	const reset = formatResetTime(window.resetsAt, locale);
	const tone = remaining <= 0 ? "exhausted" : remaining <= 25 ? "near" : "available";
	const fillClass = tone === "exhausted" ? "bg-error" : tone === "near" ? "bg-warning" : "bg-foreground/80";
	const valueClass = tone === "exhausted" ? "text-error" : tone === "near" ? "text-warning" : "text-muted-foreground";
	return <div className="grid gap-2 px-3.5 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(8rem,11rem)_auto] sm:items-center sm:gap-4"><div className="min-w-0"><p className="font-medium text-foreground">{label}</p>{reset ? <p className="mt-0.5 text-muted-foreground" title={reset.full}>{t("settings.agyCapacity.capacityResets", { value: reset.visible })}</p> : null}</div><div role="progressbar" aria-label={t("settings.agyCapacity.remainingForLimit", { label, value: percentage })} aria-valuemin={0} aria-valuemax={100} aria-valuenow={remaining} className="h-1.5 w-full overflow-hidden rounded-full bg-muted"><div className={`h-full rounded-full transition-[width] ${fillClass}`} style={{ width: `${remaining}%` }} /></div><p className={`whitespace-nowrap text-right tabular-nums ${valueClass}`}>{t("settings.agyCapacity.percentLeft", { value: percentage })}</p></div>;
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
