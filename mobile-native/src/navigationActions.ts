import type {
	ArchiveParams,
	NavigationMutation,
	PinSectionDeleteParams,
	PinSectionRenameParams,
	SessionPinAssignParams,
	SessionPinUnpinParams,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

export class NavigationActions {
	private state = {
		pending: false,
		uncertain: false,
		error: null as string | null,
	};
	private listeners = new Set<() => void>();
	private disposed = false;
	constructor(
		private client: ConversationClientLike,
		private refresh: (receipt: NavigationMutation) => Promise<void>,
		private current: () => boolean,
		private reconcileCurrent: () => Promise<void>,
	) {}
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
	archive(target: Omit<ArchiveParams, "archived">, archived: boolean) {
		return this.run(() =>
			this.client.request("evener/archive/set", { ...target, archived }),
		);
	}
	favorite(id: string, favorited: boolean) {
		return this.run(() =>
			this.client.request("evener/favorite/set", {
				kind: "project",
				id,
				favorited,
			}),
		);
	}
	assignPin(target: SessionPinAssignParams) {
		return this.run(() =>
			this.client.request("evener/session-pin/assign", target),
		);
	}
	unpin(target: SessionPinUnpinParams) {
		return this.run(() =>
			this.client.request("evener/session-pin/unpin", target),
		);
	}
	renamePinSection(target: PinSectionRenameParams) {
		return this.run(() =>
			this.client.request("evener/pin-section/rename", target),
		);
	}
	deletePinSection(target: PinSectionDeleteParams) {
		return this.run(() =>
			this.client.request("evener/pin-section/delete", target),
		);
	}
	async reconcile() {
		if (this.disposed || !this.current() || this.state.pending) return;
		this.publish({ ...this.state, pending: true });
		try {
			await this.reconcileCurrent();
			if (this.disposed) return;
			const stillCurrent = this.current();
			this.publish({
				...this.state,
				pending: false,
				uncertain: !stillCurrent,
				error: stillCurrent
					? null
					: "The hub changed while confirming this change. Refresh before trying again.",
			});
		} catch {
			if (!this.disposed)
				this.publish({
					...this.state,
					pending: false,
					error:
						"Could not refresh the current navigation; the previous change remains unresolved.",
				});
		}
	}
	private async run(
		request: () => Promise<{ ok: boolean; navigation: NavigationMutation }>,
	) {
		if (
			this.disposed ||
			!this.current() ||
			this.state.pending ||
			this.state.uncertain
		)
			return;
		this.publish({ pending: true, uncertain: false, error: null });
		let accepted = false;
		try {
			const response = await request();
			if (this.disposed) return;
			if (!this.current()) throw new Error("Connection changed.");
			if (!response.ok) throw new Error("The hub did not accept this change.");
			accepted = true;
			await this.refresh(response.navigation);
			if (this.disposed) return;
			if (!this.current()) {
				this.publish({
					pending: false,
					uncertain: true,
					error:
						"The hub changed while confirming this change. Refresh before trying again.",
				});
				return;
			}
			this.publish({ pending: false, uncertain: false, error: null });
		} catch {
			if (this.disposed) return;
			this.publish({
				pending: false,
				uncertain: true,
				error: accepted
					? "The change may have been applied, but the refreshed navigation could not be confirmed. Refresh before trying again."
					: "Could not confirm the change. Refresh the list before trying again; it may have been applied.",
			});
		}
	}
}
