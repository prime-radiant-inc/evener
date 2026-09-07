import type {
	SettingsOverviewResponse,
	UpgradeResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

export type UpgradeState =
	| { kind: "idle" }
	| { kind: "running" }
	| {
			kind: "installed";
			response: UpgradeResponse;
			overview?: SettingsOverviewResponse;
	  }
	| {
			kind: "uncertain";
			message: string;
			response?: UpgradeResponse;
			overview?: SettingsOverviewResponse;
	  }
	| { kind: "storageUnavailable"; message: string; response?: UpgradeResponse };
export interface UpgradeCheckpoint {
	hubId: string;
	pending: true;
	startedAt: number;
	response?: UpgradeResponse;
}
export interface UpgradeStorage {
	read(hubId: string): UpgradeCheckpoint | null;
	write(checkpoint: UpgradeCheckpoint): void;
}
export interface HubUpgradeController {
	getSnapshot(): UpgradeState;
	subscribe(listener: () => void): () => void;
	start(): Promise<void>;
	reconcileAfterReconnect(): Promise<void>;
	dispose(): void;
}
const errorText = (error: unknown) =>
	error instanceof Error ? error.message : String(error);
export function createHubUpgradeController(
	hubId: string,
	client: ConversationClientLike,
	storage: UpgradeStorage,
): HubUpgradeController {
	let state: UpgradeState;
	try {
		const checkpoint = storage.read(hubId);
		state = checkpoint?.response
			? { kind: "installed", response: checkpoint.response }
			: checkpoint
				? {
						kind: "uncertain",
						message:
							"An upgrade may have been installed. Reconnect and verify.",
					}
				: { kind: "idle" };
	} catch (error) {
		state = {
			kind: "storageUnavailable",
			message: "Upgrade checkpoint storage is unavailable: " + errorText(error),
		};
	}
	let generation = 0;
	let disposed = false;
	const listeners = new Set<() => void>();
	const publish = (next: UpgradeState) => {
		if (disposed) return;
		state = next;
		for (const listener of listeners) listener();
	};
	const readOverview = () => client.request("evener/settings/overview", {});
	return {
		getSnapshot: () => state,
		subscribe: (listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		start: async () => {
			if (disposed || state.kind !== "idle") return;
			const current = ++generation;
			const startedAt = Date.now();
			try {
				storage.write({ hubId, pending: true, startedAt });
			} catch (error) {
				publish({
					kind: "storageUnavailable",
					message: "Could not save upgrade checkpoint: " + errorText(error),
				});
				return;
			}
			publish({ kind: "running" });
			try {
				const response = await client.request("evener/upgrade", {
					requested: "",
				});
				try {
					storage.write({ hubId, pending: true, startedAt, response });
				} catch (error) {
					publish({
						kind: "storageUnavailable",
						message: "Could not retain upgrade result: " + errorText(error),
						response,
					});
					return;
				}
				if (disposed || current !== generation) return;
				try {
					const overview = await readOverview();
					if (!disposed && current === generation)
						publish({ kind: "installed", response, overview });
				} catch {
					if (!disposed && current === generation)
						publish({ kind: "installed", response });
				}
			} catch (error) {
				if (!disposed && current === generation)
					publish({
						kind: "uncertain",
						message:
							"Upgrade outcome is uncertain; do not retry automatically: " +
							errorText(error),
					});
			}
		},
		reconcileAfterReconnect: async () => {
			if (
				disposed ||
				(state.kind !== "uncertain" && state.kind !== "installed")
			)
				return;
			const current = ++generation;
			try {
				const overview = await readOverview();
				if (!disposed && current === generation)
					publish({ ...state, overview });
			} catch (error) {
				if (!disposed && current === generation)
					publish({
						...state,
						message:
							"Upgrade readback remains unavailable: " + errorText(error),
					} as UpgradeState);
			}
		},
		dispose: () => {
			disposed = true;
			generation += 1;
			listeners.clear();
		},
	};
}
