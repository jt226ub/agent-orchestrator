import type { MessageKey } from "../i18n/messages";

export const agyCapacityQueryKey = ["agy-capacity"] as const;

export type AgyCapacityMessageKey = Extract<MessageKey, `settings.agyCapacity.${string}`>;

const reasonKeys = {
	capacity_not_checked: "settings.agyCapacity.reason.capacityNotChecked",
	capacity_checking: "settings.agyCapacity.reason.capacityChecking",
	capacity_available: "settings.agyCapacity.reason.capacityAvailable",
	capacity_near_limit: "settings.agyCapacity.reason.capacityNearLimit",
	capacity_exhausted: "settings.agyCapacity.reason.capacityExhausted",
	capacity_unsupported: "settings.agyCapacity.reason.capacityUnsupported",
	capacity_not_installed: "settings.agyCapacity.reason.capacityNotInstalled",
	capacity_skipped_signed_out: "settings.agyCapacity.reason.capacitySkippedSignedOut",
	capacity_check_inconclusive: "settings.agyCapacity.reason.capacityCheckInconclusive",
	capacity_check_timeout: "settings.agyCapacity.reason.capacityCheckTimeout",
	capacity_check_failed: "settings.agyCapacity.reason.capacityCheckFailed",
	capacity_provider_rejected: "settings.agyCapacity.reason.capacityProviderRejected",
	capacity_check_stopped: "settings.agyCapacity.reason.capacityCheckStopped",
} satisfies Record<string, AgyCapacityMessageKey>;

export function agyCapacityReasonKey(reasonCode: string | null | undefined): AgyCapacityMessageKey {
	return reasonKeys[reasonCode as keyof typeof reasonKeys] ?? "settings.agyCapacity.reason.unknown";
}
