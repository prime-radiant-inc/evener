import type { InputItem, MutationReceipt } from "@evener/appwire-client";
import type {
	MutationAttachmentRef,
	MutationPersistenceSnapshot,
} from "@evener/appwire-client/state/mutation";

export type ConversationMutationKind = "send" | "steer" | "queue" | "interrupt";

export interface ConversationMutationRequest {
	readonly kind: ConversationMutationKind;
	readonly hubId: string;
	readonly targetRef: string;
	readonly threadId: string;
	readonly instanceId: string;
	readonly input: InputItem[];
	readonly expectedQueueRevision?: number;
}

export interface ConversationMutationSubmitter {
	submit(request: ConversationMutationRequest): Promise<MutationReceipt | undefined>;
}

// The durable pending-row seam a host wires for one conversation: the scoped
// read of this target's outbox/optimistic records, the storage-change
// subscription that follows them, and the client-ownership rule the shared
// reconciliation asks for. The host owns the durable target key (native scopes
// records by hub + conversation), so `targetRef` is the key the records carry -
// not the wire ref the model names. The store never constructs one; it follows
// whichever seam its host binds.
export interface ConversationMutationPendingPort {
	readonly targetRef: string;
	// The pending projection reads only the two record families it projects;
	// the recovery family belongs to the recovery surface, so the seam stays
	// the caller's view of the same read rather than the whole snapshot.
	read(): Promise<
		Pick<MutationPersistenceSnapshot<MutationAttachmentRef>, "outbox" | "optimistic">
	>;
	subscribe(listener: () => void): () => void;
	isOwnMutationRecord(record: { originClientId?: string }): boolean;
}

// The runtime surface the production pending-row seam adapts: the scoped
// durable read (the runtime's `NativeMutationPersistenceRead.read`) and the
// storage-change subscription it publishes. `NativeMutationRuntime` satisfies
// this structurally, so the factory needs no native import and the landed
// runtime is untouched.
export interface ConversationMutationPendingRuntime {
	read(
		targetRef: string,
	): Promise<MutationPersistenceSnapshot<MutationAttachmentRef>>;
	subscribeStorage(
		listener: (targetRefs: readonly string[]) => void,
	): () => void;
}

// Binds one conversation's durable outbox/optimistic records as the store's
// pending rows, under the operator's client-owned ruling: the native outbox is
// this app instance's own durable store (one process, one database, no
// cross-tab sharing), so every record the scoped read returns for `targetRef`
// is this client's own submission - a record a prior process stamped with an
// earlier originClientId is still ours. That is exactly why the ruling
// resolves cross-restart ownership without exposing the runtime's private
// ClientIdentity: ownership is a fact about whose outbox the row sits in, not
// about matching an identity token. `targetRef` is the composite storage key
// (hub + conversation), never the wire ref, so the storage namespace stays
// isolated from hub/ref identities.
export function createConversationMutationPendingPort(
	runtime: ConversationMutationPendingRuntime,
	targetRef: string,
): ConversationMutationPendingPort {
	return {
		targetRef,
		read: async () => {
			const snapshot = await runtime.read(targetRef);
			return { outbox: snapshot.outbox, optimistic: snapshot.optimistic };
		},
		subscribe: (listener) =>
			runtime.subscribeStorage((targetRefs) => {
				if (targetRefs.includes(targetRef)) listener();
			}),
		// Client-owned: the durable outbox holds only this client's records.
		isOwnMutationRecord: () => true,
	};
}
