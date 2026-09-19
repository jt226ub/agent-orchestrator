import { CircleAlert, LoaderCircle } from "lucide-react";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";
import type { AgyAccount } from "../../hooks/useAgyAccountsQuery";
import { agyAccountReasonKey } from "../../hooks/agy-accounts-state";
import { Button } from "../ui/button";

export function AgyAccountDetails({ account, mutationDisabled, retryBusy, onRetry }: { account: AgyAccount; mutationDisabled: boolean; retryBusy: boolean; onRetry: () => void }) {
	const { t, i18n } = useTranslation();
	if (account.status === "signed_out") return null;
	if (account.authentication.state === "unauthorized") {
		return <div className="ml-9 mt-4 space-y-5 pb-1 text-xs"><CapacityNotice reason={t(agyAccountReasonKey(account.authentication.reasonCode))} tone="warning" /></div>;
	}
	const hasOverall = Boolean(account.capacity.overall?.primary || account.capacity.overall?.secondary);
	const additionalBuckets = account.capacity.additionalBuckets.filter((bucket) => bucket.primary || bucket.secondary);
	const hasCapacityDetails = Boolean(hasOverall || additionalBuckets.length > 0);
	const capacityNotice = capacityNoticeFor(account, t, i18n.language);
	const usageUnavailable = !hasCapacityDetails && account.capacity.freshness !== "checking";
	return (
		<div className="ml-9 mt-4 space-y-5 pb-1 text-xs">
			{capacityNotice ? <CapacityNotice {...capacityNotice} /> : null}
			{hasOverall && account.capacity.overall ? <CapacityBucketGroup bucket={account.capacity.overall} title={t("settings.agyAccounts.generalUsageLimits")} locale={i18n.language} /> : null}
			{additionalBuckets.map((bucket, index) => (
				<CapacityBucketGroup key={`${bucket.displayName ?? "additional"}-${index}`} bucket={bucket} title={bucket.displayName ? t("settings.agyAccounts.namedUsageLimits", { name: bucket.displayName }) : t("settings.agyAccounts.additionalUsageLimits")} locale={i18n.language} />
			))}
			{usageUnavailable ? <div className="flex items-center gap-2"><p className="text-muted-foreground">{t("settings.agyAccounts.usageDetailsUnavailable")}</p><Button type="button" size="sm" variant="outline" disabled={mutationDisabled || retryBusy} onClick={onRetry}>{retryBusy ? <LoaderCircle className="animate-spin" aria-label={t("settings.agyAccounts.checking")} /> : null}{t("settings.agyAccounts.tryAgain")}</Button></div> : null}
		</div>
	);
}

type CapacityBucketValue = NonNullable<AgyAccount["capacity"]["overall"]>;
type CapacityWindowValue = NonNullable<CapacityBucketValue["primary"]>;

function CapacityBucketGroup({ bucket, title, locale }: { bucket: CapacityBucketValue; title: string; locale: string }) {
	const { t } = useTranslation();
	const windows = [bucket.primary, bucket.secondary].filter((window): window is CapacityWindowValue => Boolean(window));
	return <section><h4 className="mb-2 font-medium text-foreground">{title}</h4><div className="divide-y divide-border/70 overflow-hidden rounded-md border border-border/70 bg-muted/15">{windows.map((window, index) => <CapacityWindowRow key={`${index}-${window.windowDurationMinutes ?? "unknown"}-${window.resetsAt ?? "never"}`} window={window} label={capacityWindowLabel(window.windowDurationMinutes, index, windows.length, t)} reached={window.usedPercent >= 100} locale={locale} />)}</div></section>;
}

function CapacityWindowRow({ window, label, reached, locale }: { window: CapacityWindowValue; label: string; reached: boolean; locale: string }) {
	const { t } = useTranslation();
	const remaining = Math.max(0, Math.min(100, 100 - window.usedPercent));
	const percentage = formatPercentage(remaining, locale);
	const reset = formatResetTime(window.resetsAt, locale);
	const tone = reached || remaining <= 0 ? "exhausted" : remaining <= 25 ? "near" : "available";
	const fillClass = tone === "exhausted" ? "bg-error" : tone === "near" ? "bg-warning" : "bg-foreground/80";
	const valueClass = tone === "exhausted" ? "text-error" : tone === "near" ? "text-warning" : "text-muted-foreground";
	return <div className="grid gap-2 px-3.5 py-3 sm:grid-cols-[minmax(0,1fr)_minmax(8rem,11rem)_auto] sm:items-center sm:gap-4"><div className="min-w-0"><p className="font-medium text-foreground">{label}</p>{reset ? <p className="mt-0.5 text-muted-foreground" title={reset.full}>{t("settings.agyAccounts.capacityResets", { value: reset.visible })}</p> : null}</div><div role="progressbar" aria-label={t("settings.agyAccounts.remainingForLimit", { label, value: percentage })} aria-valuemin={0} aria-valuemax={100} aria-valuenow={remaining} className="h-1.5 w-full overflow-hidden rounded-full bg-muted"><div className={`h-full rounded-full transition-[width] ${fillClass}`} style={{ width: `${remaining}%` }} /></div><p className={`whitespace-nowrap text-right tabular-nums ${valueClass}`}>{t("settings.agyAccounts.percentLeft", { value: percentage })}</p></div>;
}

function CapacityNotice({ reason, tone, checking }: { reason: string; tone: "warning" | "error" | "muted"; checking?: boolean }) {
	const { t } = useTranslation();
	const color = tone === "error" ? "border-error/30 bg-error/8 text-error" : tone === "warning" ? "border-warning/30 bg-warning/10 text-warning" : "border-border bg-muted/20 text-muted-foreground";
	return <p className={`flex items-start gap-2 rounded-md border px-3 py-2.5 leading-5 ${color}`} role={tone === "error" ? "alert" : "status"}>{checking ? <LoaderCircle className="mt-0.5 size-3.5 shrink-0 animate-spin" aria-label={t("settings.agyAccounts.checking")} /> : <CircleAlert className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />}<span>{reason}</span></p>;
}

function capacityNoticeFor(account: AgyAccount, t: TFunction, locale: string): { reason: string; tone: "warning" | "error" | "muted"; checking?: boolean } | null {
	if (account.status === "broken") return { reason: t(agyAccountReasonKey(account.reasonCode)), tone: "error" };
	if (account.capacity.freshness === "checking") return { reason: t(agyAccountReasonKey(account.capacity.reasonCode)), tone: "muted", checking: true };
	// Credential changes intentionally invalidate the old capacity snapshot. It
	// is an internal transition, not a user-actionable failure; the section
	// refreshes it quietly after login/switch completion.
	if (account.capacity.reasonCode === "capacity_invalidated") return null;
	if (account.capacity.freshness === "stale") {
		const checked = account.capacity.checkedAt ? formatObservedTime(account.capacity.checkedAt, locale) : null;
		if (checked && (account.capacity.overall || account.capacity.additionalBuckets.length > 0)) {
			return { reason: t("settings.agyAccounts.capacityStaleReasonChecked", { value: checked }), tone: "muted" };
		}
		return null;
	}
	if (account.capacity.state === "unknown" || account.capacity.state === "unsupported") return null;
	return null;
}

function capacityWindowLabel(minutes: number | null | undefined, index: number, count: number, t: TFunction): string {
	if (minutes === 300) return t("settings.agyAccounts.fiveHourUsageLimit");
	if (minutes === 10080) return t("settings.agyAccounts.weeklyUsageLimit");
	if (minutes && minutes > 0 && minutes % 1440 === 0) return t("settings.agyAccounts.dayUsageLimit", { count: minutes / 1440 });
	if (minutes && minutes > 0 && minutes % 60 === 0) return t("settings.agyAccounts.hourUsageLimit", { count: minutes / 60 });
	if (minutes && minutes > 0) return t("settings.agyAccounts.minuteUsageLimit", { count: minutes });
	if (count === 1) return t("settings.agyAccounts.usageLimit");
	return index === 0 ? t("settings.agyAccounts.primaryUsageLimit") : t("settings.agyAccounts.secondaryUsageLimit");
}

export function formatAuthMethod(method: AgyAccount["authMethod"]): string | null { return method === "google" ? "Google" : method === "other" ? "Antigravity" : null; }
export function formatPercentage(value: number, locale?: string): string { return `${new Intl.NumberFormat(locale, { maximumFractionDigits: 1 }).format(value)}%`; }
function formatResetTime(value: string | null | undefined, locale: string): { visible: string; full: string } | null { if (!value) return null; const date = new Date(value); if (Number.isNaN(date.getTime())) return null; const now = new Date(); const sameDay = date.getFullYear() === now.getFullYear() && date.getMonth() === now.getMonth() && date.getDate() === now.getDate(); return { visible: new Intl.DateTimeFormat(locale, sameDay ? { hour: "2-digit", minute: "2-digit" } : { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }).format(date), full: new Intl.DateTimeFormat(locale, { dateStyle: "full", timeStyle: "long" }).format(date) }; }
function formatObservedTime(value: string, locale: string): string { const date = new Date(value); return Number.isNaN(date.getTime()) ? "" : new Intl.DateTimeFormat(locale, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }).format(date); }
