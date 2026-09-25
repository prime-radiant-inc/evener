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
