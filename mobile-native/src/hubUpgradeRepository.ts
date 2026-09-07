import type { UpgradeCheckpoint, UpgradeStorage } from "./hubUpgrade";

const prefix = "evener:hub-upgrade:";
export interface SyncUpgradeStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
}
const requiredResponse = [
	"release",
	"channel",
	"url",
	"archive",
	"prefix",
	"binDir",
	"shareBinDir",
	"restartMessage",
] as const;
const text = (value: unknown): value is string => typeof value === "string";
const validResponse = (value: unknown) => {
	if (!value || typeof value !== "object") return false;
	const candidate = value as Record<string, unknown>;
	return (
		requiredResponse.every((key) => text(candidate[key])) &&
		Array.isArray(candidate.installed) &&
		candidate.installed.every(text)
	);
};
const parse = (hubId: string, raw: string): UpgradeCheckpoint => {
	const value: unknown = JSON.parse(raw);
	if (!value || typeof value !== "object")
		throw new Error("Invalid upgrade checkpoint");
	const candidate = value as Record<string, unknown>;
	if (
		candidate.hubId !== hubId ||
		candidate.pending !== true ||
		typeof candidate.startedAt !== "number" ||
		!Number.isFinite(candidate.startedAt) ||
		(candidate.response !== undefined && !validResponse(candidate.response))
	)
		throw new Error("Invalid upgrade checkpoint");
	return value as UpgradeCheckpoint;
};

export class HubUpgradeRepository implements UpgradeStorage {
	constructor(private readonly storage: SyncUpgradeStorage) {}
	read(hubId: string) {
		const raw = this.storage.getItemSync(prefix + hubId);
		return raw === null ? null : parse(hubId, raw);
	}
	write(checkpoint: UpgradeCheckpoint) {
		this.storage.setItemSync(
			prefix + checkpoint.hubId,
			JSON.stringify(checkpoint),
		);
	}
}
