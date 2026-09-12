import type { ConversationTurnForkActions } from "../../mobile/src/services/conversation";
import type { DraftLibrary } from "./draftLibrary";
import type {
	ForkCheckpoint,
	ForkCheckpointRepository,
	ForkChild,
	ForkTarget,
} from "./forkCheckpointRepository";
import type { LocationRepository } from "./location";

interface ForkState {
	checkpoint: ForkCheckpoint | null;
	pending: boolean;
	storageUnavailable: boolean;
	error: string | null;
}

/** One saved parent request retains its child independently of the active screen. */
export class ForkActions {
	private state: ForkState = {
		checkpoint: null,
		pending: false,
		storageUnavailable: false,
		error: null,
	};
	private listeners = new Set<() => void>();
	private disposed = false;
	private unwritten: { expected: ForkCheckpoint; child: ForkChild } | null =
		null;
	constructor(
		private repository: ForkCheckpointRepository,
		private service: ConversationTurnForkActions,
		private drafts: Pick<DraftLibrary, "open">,
		private hubId: string,
		private current: () => boolean,
		private sourceInstance: () => string | null,
	) {
		this.retryStorage();
	}
	getSnapshot = () => this.state;
	subscribe = (listener: () => void) => {
		this.listeners.add(listener);
		return () => {
			this.listeners.delete(listener);
		};
	};
	private publish(update: Partial<ForkState>) {
		if (this.disposed) return;
		this.state = { ...this.state, ...update };
		for (const listener of this.listeners) listener();
	}
	dispose() {
		this.disposed = true;
		this.listeners.clear();
	}
	retryStorage() {
		if (this.disposed || this.state.pending) return;
		try {
			if (this.unwritten) {
				this.repository.acknowledge(
					this.unwritten.expected,
					this.unwritten.child,
				);
				this.unwritten = null;
			}
			this.publish({
				checkpoint: this.repository.load(),
				storageUnavailable: false,
				error: null,
			});
		} catch {
			this.publish({
				storageUnavailable: true,
				error:
					"The fork recovery record could not be saved or loaded. Keep this screen open and retry storage.",
			});
		}
	}
	async create(target: ForkTarget): Promise<ForkChild | null> {
		if (this.disposed || !this.current() || this.state.pending) return null;
		if (this.sourceInstance() !== target.instanceId) {
			this.publish({
				error:
					"The source session changed. Return to it and select the message again.",
			});
			return null;
		}
		this.retryStorage();
		if (this.state.storageUnavailable || this.state.checkpoint) return null;
		this.publish({ pending: true, error: null });
		let storing = true;
		try {
			const checkpoint = this.repository.begin(target);
			this.publish({ checkpoint });
			storing = false;
			const response = await this.service.forkFromTurn(target.entryIndex);
			const child: ForkChild = {
				ref: response.thread.evener.ref,
				title: response.thread.name || response.thread.preview || "Fork",
				input: response.originalInput ?? "",
			};
			storing = true;
			this.unwritten = { expected: checkpoint, child };
			const accepted = this.repository.acknowledge(checkpoint, child);
			this.unwritten = null;
			this.publish({ checkpoint: accepted, pending: false });
			return this.disposed || !this.current() ? null : this.openChild();
		} catch {
			this.publish({
				pending: false,
				storageUnavailable: storing,
				error: storing
					? "The fork recovery record could not be saved. Keep this screen open and retry storage."
					: "The fork could not be confirmed. It may already exist; check Sessions before making another request.",
			});
			return null;
		}
	}
	openChild(): ForkChild | null {
		if (this.disposed || !this.current() || this.state.pending) return null;
		this.retryStorage();
		const checkpoint = this.state.checkpoint,
			child = checkpoint?.child;
		if (this.state.storageUnavailable || !checkpoint || !child) return null;
		try {
			const document = this.drafts.open({
				hubId: this.hubId,
				sessionRef: child.ref,
			});
			if (!document.getSnapshot().loaded || document.getSnapshot().error)
				document.retry();
			let draft = document.getSnapshot();
			if (!draft.loaded || draft.error)
				throw Error("The child draft is unavailable.");
			if (!checkpoint.draftPrepared) {
				if (
					!draft.submitting &&
					!draft.record.draft &&
					draft.record.unconfirmed === null &&
					!draft.record.images?.length &&
					!draft.record.unconfirmedImages?.length
				) {
					document.edit(child.input);
					draft = document.getSnapshot();
					if (draft.error) throw Error("The child draft could not be saved.");
				}
				const prepared = this.repository.prepareDraft(checkpoint);
				this.publish({ checkpoint: prepared });
			}
			return child;
		} catch {
			this.publish({
				error:
					"The fork exists, but its editable draft could not be prepared. Retry opening the fork; no new session will be created.",
			});
			return null;
		}
	}
	finish(
		locations: Pick<LocationRepository, "save">,
		navigate: (child: ForkChild) => void,
	) {
		const saved = this.state.checkpoint;
		if (
			this.disposed ||
			!this.current() ||
			!saved?.child ||
			!saved.draftPrepared
		)
			return false;
		try {
			// A process exit between navigation renders must reopen the child
			// before its recovery record can be removed.
			locations.save({
				hubId: this.hubId,
				conversation: { ref: saved.child.ref, title: saved.child.title },
			});
			navigate(saved.child);
		} catch {
			this.publish({
				error:
					"The fork exists, but its destination could not be opened. Retry opening it.",
			});
			return false;
		}
		try {
			const removed = this.repository.removeIf(saved);
			if (removed) this.publish({ checkpoint: null, error: null });
		} catch {
			// The child destination and draft are durable. Keeping its known
			// acknowledgement allows reopening without creating another fork.
		}
		return true;
	}
	discardUnknown(expected: ForkCheckpoint) {
		if (
			this.disposed ||
			!this.current() ||
			this.state.pending ||
			expected.child ||
			this.unwritten
		)
			return false;
		try {
			const removed = this.repository.removeIf(expected);
			if (removed)
				this.publish({
					checkpoint: null,
					error: null,
					storageUnavailable: false,
				});
			else this.retryStorage();
			return removed;
		} catch {
			this.publish({
				storageUnavailable: true,
				error:
					"The saved request could not be cleared. Retry storage before creating another fork.",
			});
			return false;
		}
	}
}
