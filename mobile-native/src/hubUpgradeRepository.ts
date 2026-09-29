import type { UpgradeCheckpoint, UpgradeStorage } from "./hubUpgrade";
import { isValidUpgradeResponse } from "./hubUpgradeValidation";
import type { SyncStringStorage } from "./syncStringStorage";

const prefix = "evener:hub-upgrade:";
const parse = (hubId: string, raw: string): UpgradeCheckpoint => {
	const value: unknown = JSON.parse(raw);
	if (!value || typeof value !== "object") throw new Error("Invalid upgrade checkpoint");
	const candidate = value as Record<string, unknown>;
	if (
		candidate.hubId !== hubId ||
		candidate.pending !== true ||
		typeof candidate.attemptId !== "string" ||
		candidate.attemptId.trim() === "" ||
		typeof candidate.startedAt !== "number" ||
		!Number.isFinite(candidate.startedAt) ||
		(candidate.response !== undefined && !isValidUpgradeResponse(candidate.response))
	)
		throw new Error("Invalid upgrade checkpoint");
	return JSON.parse(JSON.stringify(value)) as UpgradeCheckpoint;
};

export class HubUpgradeRepository implements UpgradeStorage {
	constructor(private readonly storage: SyncStringStorage) {}
	read(hubId: string) {
		const raw = this.storage.getItemSync(prefix + hubId);
		return raw === null ? null : parse(hubId, raw);
	}
	write(checkpoint: UpgradeCheckpoint) {
		this.storage.setItemSync(prefix + checkpoint.hubId, JSON.stringify(checkpoint));
	}
	remove(hubId: string, attemptId: string) {
		const checkpoint = this.read(hubId);
		if (checkpoint?.attemptId === attemptId) this.storage.removeItemSync(prefix + hubId);
	}
}
