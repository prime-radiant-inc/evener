import type {
	SettingsOverviewResponse,
	UpgradeResponse,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import {
	isValidRunningOverview,
	isValidUpgradeResponse,
} from "./hubUpgradeValidation";

export type UpgradeState =
	| { kind: "idle" }
	| { kind: "running" }
	| {
			kind: "installed";
			response: UpgradeResponse;
			overview?: SettingsOverviewResponse;
			message?: string;
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
	attemptId: string;
	response?: UpgradeResponse;
}
export interface UpgradeStorage {
	read(hubId: string): UpgradeCheckpoint | null;
	write(checkpoint: UpgradeCheckpoint): void;
	remove(hubId: string, attemptId: string): void;
}
export interface UpgradeReview {
	readonly attemptId: string;
}
export interface HubUpgradeController {
	getSnapshot(): UpgradeState;
	subscribe(listener: () => void): () => void;
	start(): Promise<void>;
	reconcileAfterReconnect(): Promise<void>;
	reviewAnotherUpdate(): Promise<UpgradeReview | null>;
	rearm(review: UpgradeReview | null): void;
	dispose(): void;
}

const storageMessage = "Upgrade checkpoint storage is unavailable.";
const readbackMessage = "Could not verify the hub's running version.";
const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T;

export function createHubUpgradeController(
	hubId: string,
	client: ConversationClientLike,
	storage: UpgradeStorage,
	createAttemptId: () => string,
): HubUpgradeController {
	let state: UpgradeState;
	try {
		const checkpoint = storage.read(hubId);
		state = checkpoint?.response
			? { kind: "installed", response: clone(checkpoint.response) }
			: checkpoint
				? {
						kind: "uncertain",
						message:
							"An upgrade may have been installed. Reconnect and verify.",
					}
				: { kind: "idle" };
	} catch {
		state = { kind: "storageUnavailable", message: storageMessage };
	}
	let generation = 0;
	let activeReview: UpgradeReview | undefined;
	let disposed = false;
	const listeners = new Set<() => void>();
	const publish = (next: UpgradeState) => {
		if (disposed) return;
		state = next;
		for (const listener of listeners) listener();
	};
	const readOverview = async () => {
		const overview = await client.request("evener/settings/overview", {});
		if (!isValidRunningOverview(overview)) throw new Error("invalid overview");
		return clone(overview);
	};
	const readAttempt = (attemptId: string) => {
		const checkpoint = storage.read(hubId);
		return checkpoint?.attemptId === attemptId ? checkpoint : null;
	};
	const stateForCheckpoint = (checkpoint: UpgradeCheckpoint): UpgradeState =>
		checkpoint.response
			? { kind: "installed", response: clone(checkpoint.response) }
			: {
					kind: "uncertain",
					message: "An upgrade may have been installed. Reconnect and verify.",
				};

	return {
		getSnapshot: () => state,
		subscribe: (listener) => {
			listeners.add(listener);
			return () => listeners.delete(listener);
		},
		start: async () => {
			if (disposed || state.kind !== "idle") return;
			activeReview = undefined;
			const current = ++generation;
			const attemptId = createAttemptId();
			const startedAt = Date.now();
			try {
				const existing = storage.read(hubId);
				if (existing) {
					publish(stateForCheckpoint(existing));
					return;
				}
				storage.write({ hubId, pending: true, startedAt, attemptId });
				if (!readAttempt(attemptId)) {
					publish({ kind: "storageUnavailable", message: storageMessage });
					return;
				}
			} catch {
				publish({ kind: "storageUnavailable", message: storageMessage });
				return;
			}
			publish({ kind: "running" });
			try {
				const rawResponse = await client.request("evener/upgrade", {
					requested: "",
				});
				if (!isValidUpgradeResponse(rawResponse))
					throw new Error("invalid response");
				const response = clone(rawResponse);
				try {
					if (!readAttempt(attemptId)) {
						publish({
							kind: "storageUnavailable",
							message: storageMessage,
							response,
						});
						return;
					}
					storage.write({
						hubId,
						pending: true,
						startedAt,
						attemptId,
						response: clone(response),
					});
				} catch {
					if (!disposed && current === generation)
						publish({
							kind: "storageUnavailable",
							message: storageMessage,
							response,
						});
					return;
				}
				if (disposed || current !== generation) return;
				try {
					const overview = await readOverview();
					if (!disposed && current === generation)
						publish({ kind: "installed", response: clone(response), overview });
				} catch {
					if (!disposed && current === generation)
						publish({
							kind: "installed",
							response: clone(response),
							message: readbackMessage,
						});
				}
			} catch {
				if (!disposed && current === generation)
					publish({
						kind: "uncertain",
						message:
							"Upgrade outcome is uncertain. Refresh to verify before retrying.",
					});
			}
		},
		reconcileAfterReconnect: async () => {
			if (
				disposed ||
				(state.kind !== "uncertain" && state.kind !== "installed")
			)
				return;
			activeReview = undefined;
			const current = ++generation;
			try {
				const overview = await readOverview();
				if (!disposed && current === generation)
					publish({ ...state, overview });
			} catch {
				if (!disposed && current === generation)
					publish({ ...state, message: readbackMessage } as UpgradeState);
			}
		},
		reviewAnotherUpdate: async () => {
			if (
				disposed ||
				(state.kind !== "uncertain" && state.kind !== "installed")
			)
				return null;
			activeReview = undefined;
			const current = ++generation;
			let checkpoint: UpgradeCheckpoint;
			try {
				const captured = storage.read(hubId);
				if (!captured) return null;
				checkpoint = captured;
			} catch {
				publish({ kind: "storageUnavailable", message: storageMessage });
				return null;
			}
			try {
				const overview = await readOverview();
				if (disposed || current !== generation) return null;
				let stillCurrent: UpgradeCheckpoint | null;
				try {
					stillCurrent = readAttempt(checkpoint.attemptId);
				} catch {
					publish({ kind: "storageUnavailable", message: storageMessage });
					return null;
				}
				if (!stillCurrent) return null;
				activeReview = { attemptId: checkpoint.attemptId };
				publish({ ...state, overview });
				return activeReview;
			} catch {
				if (!disposed && current === generation)
					publish({ ...state, message: readbackMessage } as UpgradeState);
				return null;
			}
		},
		rearm: (review) => {
			const attemptId = review?.attemptId;
			if (
				disposed ||
				!attemptId ||
				review !== activeReview ||
				(state.kind !== "uncertain" && state.kind !== "installed")
			)
				return;
			try {
				if (!readAttempt(attemptId)) return;
				storage.remove(hubId, attemptId);
				activeReview = undefined;
				publish({ kind: "idle" });
			} catch {
				publish({ kind: "storageUnavailable", message: storageMessage });
			}
		},
		dispose: () => {
			disposed = true;
			generation += 1;
			listeners.clear();
		},
	};
}
