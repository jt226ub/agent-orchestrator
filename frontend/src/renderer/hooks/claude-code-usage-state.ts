import type { MessageKey } from "../i18n/messages";

export const claudeCodeUsageQueryKey = ["claude-code-usage"] as const;

export type ClaudeCodeUsageMessageKey = Extract<MessageKey, `settings.claudeCodeUsage.${string}`>;

const reasonKeys = {
	plan_usage_not_checked: "settings.claudeCodeUsage.reason.planUsageNotChecked",
	plan_usage_checking: "settings.claudeCodeUsage.reason.planUsageChecking",
	plan_usage_available: "settings.claudeCodeUsage.reason.planUsageAvailable",
	plan_usage_signed_out: "settings.claudeCodeUsage.reason.planUsageSignedOut",
	plan_usage_unavailable: "settings.claudeCodeUsage.reason.planUsageUnavailable",
	plan_usage_rate_limited: "settings.claudeCodeUsage.reason.planUsageRateLimited",
	plan_usage_unsupported: "settings.claudeCodeUsage.reason.planUsageUnsupported",
	plan_usage_invalid_response: "settings.claudeCodeUsage.reason.planUsageInvalidResponse",
	plan_usage_check_timeout: "settings.claudeCodeUsage.reason.planUsageCheckTimeout",
	plan_usage_check_stopped: "settings.claudeCodeUsage.reason.planUsageCheckStopped",
} satisfies Record<string, ClaudeCodeUsageMessageKey>;

export function claudeCodeUsageReasonKey(reasonCode: string | null | undefined): ClaudeCodeUsageMessageKey {
	return reasonKeys[reasonCode as keyof typeof reasonKeys] ?? "settings.claudeCodeUsage.reason.unknown";
}

const planNames: Record<string, string> = { free: "Free", pro: "Pro", max: "Max", team: "Team", business: "Business", enterprise: "Enterprise" };

export function formatClaudeCodePlanName(plan: string | null | undefined): string | null {
	const value = plan?.trim();
	if (!value) return null;
	return planNames[value.toLowerCase()] ?? value;
}
