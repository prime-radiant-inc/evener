import type {
	ArchiveParams,
	NavigationMutation,
	PinSectionDeleteParams,
	PinSectionRenameParams,
	SessionDeleteParams,
	SessionPinAssignParams,
	SessionPinUnpinParams,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import type {
	NavigationActionCheckpoint,
	NavigationActionStorage,
	NavigationOperation,
} from "./navigationActionRepository";
import {
	type SessionDeletionResult,
	sessionDeletionResult,
} from "./sessionDeletionResult";

const unresolved =
	"A previous organization change needs to be checked. Refresh before making another change.";
const storageError =
	"Could not read or save organization recovery on this device. Refresh to retry; no change will be sent until recovery is available.";

export class NavigationActions {
	private state = {
		pending: false,
		uncertain: false,
		storageUnavailable: false,
		recovery: null as NavigationActionCheckpoint | null,
		error: null as string | null,
	};
	private listeners = new Set<() => void>();
	private disposed = false;
	constructor(
		private client: ConversationClientLike,
		private refresh: (
			receipt: NavigationMutation,
			checkpoint?: NavigationActionCheckpoint,
		) => Promise<void>,
		private current: () => boolean,
		private reconcileCurrent: (
			checkpoint?: NavigationActionCheckpoint,
			acceptCurrent?: boolean,
		) => Promise<void>,
		private storage?: NavigationActionStorage,
		private confirmCurrent: () => void = () => {},
	) {
		this.restore();
	}
	getSnapshot = () => this.state;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	dispose() {
		this.disposed = true;
		this.listeners.clear();
	}
	private publish(state: typeof this.state) {
		this.state = state;
		for (const listener of this.listeners) listener();
	}
	private restore() {
		if (!this.storage) return true;
		try {
			const recovery = this.storage.load();
			this.publish({
				...this.state,
				recovery,
				storageUnavailable: false,
				uncertain: recovery !== null,
				error: recovery ? unresolved : null,
			});
			return true;
		} catch {
			this.publish({
				...this.state,
				storageUnavailable: true,
				uncertain: true,
				error: storageError,
			});
			return false;
		}
	}
	archive(target: Omit<ArchiveParams, "archived">, archived: boolean) {
		const params = { ...target, archived };
		return this.run({ kind: "archive", params }, () =>
			this.client.request("evener/archive/set", params),
		);
	}
	favorite(id: string, favorited: boolean) {
		const params = { kind: "project", id, favorited };
		return this.run({ kind: "favorite", params }, () =>
			this.client.request("evener/favorite/set", params),
		);
	}
	assignPin(target: SessionPinAssignParams) {
		const params = { ...target };
		return this.run({ kind: "assignPin", params }, () =>
			this.client.request("evener/session-pin/assign", params),
		);
	}
	unpin(target: SessionPinUnpinParams) {
		const params = { ...target };
		return this.run({ kind: "unpin", params }, () =>
			this.client.request("evener/session-pin/unpin", params),
		);
	}
	renamePinSection(target: PinSectionRenameParams) {
		const params = { ...target };
		return this.run({ kind: "renamePinSection", params }, () =>
			this.client.request("evener/pin-section/rename", params),
		);
	}
	deletePinSection(target: PinSectionDeleteParams) {
		const params = { ...target };
		return this.run({ kind: "deletePinSection", params }, () =>
			this.client.request("evener/pin-section/delete", params),
		);
	}
	deleteSession(target: SessionDeleteParams) {
		const params = { ...target };
		return this.run({ kind: "deleteSession", params }, async () => {
			const response = await this.client.request(
				"evener/session/delete",
				params,
			);
			return {
				ok: true,
				navigation: response.navigation,
				deletion: sessionDeletionResult(params.ref, response),
			};
		});
	}
	allowDeletionRetry(expected: NavigationActionCheckpoint) {
		if (
			this.disposed ||
			!this.current() ||
			this.state.pending ||
			expected.operation.kind !== "deleteSession" ||
			expected.receipt ||
			!this.storage
		)
			return false;
		try {
			if (!this.storage.finish(expected)) return false;
			return this.restore();
		} catch {
			this.publish({
				...this.state,
				storageUnavailable: true,
				error: storageError,
			});
			return false;
		}
	}
	keepOrganizationState(expected: NavigationActionCheckpoint) {
		return this.reconcileRecovery(expected);
	}
	reconcile() {
		return this.reconcileRecovery();
	}
	private async reconcileRecovery(accepted?: NavigationActionCheckpoint) {
		if (this.disposed || !this.current() || this.state.pending) return;
		if (!this.restore()) return;
		const checkpoint = this.state.recovery;
		if (
			accepted &&
			(!this.storage ||
				!["archive", "favorite"].includes(accepted.operation.kind) ||
				JSON.stringify(checkpoint) !== JSON.stringify(accepted))
		)
			return;
		this.publish({ ...this.state, pending: true });
		try {
			await this.reconcileCurrent(
				checkpoint ?? undefined,
				accepted !== undefined,
			);
			if (this.disposed) return;
			if (!this.current()) throw Error("scope changed");
			this.confirmCurrent();
			if (checkpoint && this.storage && !this.storage.finish(checkpoint))
				throw Error("recovery changed");
			if (!checkpoint && this.storage?.load())
				throw Error("another organization change started");
			this.publish({
				...this.state,
				pending: false,
				uncertain: false,
				storageUnavailable: false,
				recovery: null,
				error: null,
			});
		} catch {
			if (this.disposed) return;
			this.publish({
				...this.state,
				pending: false,
				uncertain: true,
				error:
					"Could not confirm current navigation for the previous change. Refresh before trying again.",
			});
		}
	}
	private async run(
		operation: NavigationOperation,
		request: () => Promise<{
			ok: boolean;
			navigation: NavigationMutation;
			deletion?: SessionDeletionResult;
		}>,
	) {
		if (
			this.disposed ||
			!this.current() ||
			this.state.pending ||
			this.state.uncertain ||
			this.state.storageUnavailable
		)
			return;
		// Other screens share the same hub journal; a model created earlier must not
		// overwrite a newer screen's unresolved operation.
		if (!this.restore() || this.state.uncertain) return;
		let checkpoint: NavigationActionCheckpoint | null = null;
		try {
			checkpoint = this.storage?.begin(operation) ?? null;
		} catch {
			this.restore();
			this.publish({
				...this.state,
				pending: false,
				storageUnavailable: true,
				error: storageError,
			});
			return;
		}
		this.publish({
			...this.state,
			pending: true,
			uncertain: false,
			recovery: checkpoint,
			error: null,
		});
		let accepted = false;
		let savingRecovery = false;
		try {
			const response = await request();
			if (response.ok !== true) throw Error("not accepted");
			accepted = true;
			if (checkpoint && this.storage) {
				savingRecovery = true;
				// Preserve a known result even when its screen has gone away. Exact
				// checkpoint matching prevents a late reply overwriting another action.
				checkpoint = this.storage.acknowledge(
					checkpoint,
					response.navigation,
					response.deletion,
				);
				savingRecovery = false;
				if (!this.disposed)
					this.publish({ ...this.state, recovery: checkpoint });
			}
			if (this.disposed) return;
			if (!this.current()) throw Error("scope changed");
			await this.refresh(response.navigation, checkpoint ?? undefined);
			if (this.disposed) return;
			if (!this.current()) throw Error("scope changed");
			this.confirmCurrent();
			if (checkpoint && this.storage) {
				savingRecovery = true;
				if (!this.storage.finish(checkpoint)) throw Error("recovery changed");
				savingRecovery = false;
			}
			this.publish({
				...this.state,
				pending: false,
				uncertain: false,
				storageUnavailable: false,
				recovery: null,
				error: null,
			});
		} catch {
			if (this.disposed) return;
			this.publish({
				...this.state,
				pending: false,
				uncertain: true,
				storageUnavailable: savingRecovery,
				error: savingRecovery
					? "The change was accepted, but its recovery record could not be updated. Refresh to confirm the current state before trying again."
					: accepted
						? "The change was accepted, but refreshed navigation could not be confirmed. Refresh before trying again."
						: "Could not confirm the change. Refresh before trying again; it may have been applied.",
			});
		}
	}
}
