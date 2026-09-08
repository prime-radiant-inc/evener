import type { UpgradeCheckpoint, UpgradeStorage } from "./hubUpgrade";
import { isValidUpgradeResponse } from "./hubUpgradeValidation";

const prefix = "evener:hub-upgrade:";
export interface SyncUpgradeStorage {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}
const parse = (hubId: string, raw: string): UpgradeCheckpoint => {
	const value: unknown = JSON.parse(raw);
	if (!value || typeof value !== "object")
		throw new Error("Invalid upgrade checkpoint");
	const candidate = value as Record<string, unknown>;
	if (
		candidate.hubId !== hubId ||
		candidate.pending !== true ||
		typeof candidate.attemptId !== "string" ||
		candidate.attemptId.trim() === "" ||
		typeof candidate.startedAt !== "number" ||
		!Number.isFinite(candidate.startedAt) ||
		(candidate.response !== undefined &&
			!isValidUpgradeResponse(candidate.response))
	)
		throw new Error("Invalid upgrade checkpoint");
	return JSON.parse(JSON.stringify(value)) as UpgradeCheckpoint;
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
	remove(hubId: string, attemptId: string) {
		const checkpoint = this.read(hubId);
		if (checkpoint?.attemptId === attemptId)
			this.storage.removeItemSync(prefix + hubId);
	}
}
