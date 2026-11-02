import type {
	MutationAttachmentRef,
	MutationPersistenceSnapshot,
	PendingTurnEntry,
} from "@evener/appwire-client/state/mutation";
import { reconcilePendingEntries } from "@evener/appwire-client/state/mutation";
import type { MobileConversation } from "../../projectedRows";
import type { ConversationMutationPendingPort } from "./conversationMutation";

export type PendingSnapshot = {
	outbox: MutationPersistenceSnapshot<MutationAttachmentRef>["outbox"];
	optimistic: MutationPersistenceSnapshot<MutationAttachmentRef>["optimistic"];
};

export interface PendingMutationBinding {
	readonly port: ConversationMutationPendingPort | null;
	readonly snapshot: PendingSnapshot | null;
	/** Advanced by every detach; work captured under an older value is retired. */
	readonly generation: number;
	/** Advanced only by forgetProvenance. */
	readonly provenanceGeneration: number;
	readonly submittedHere: Map<string, number>;
	/** Starts following a seam and returns the binding's generation. */
	attach(port: ConversationMutationPendingPort): number;
	/** Sets the bound seam's "demand a fresh current read" operation. */
	followReads(demandRead: () => Promise<void>): void;
	/** Holds the seam's storage subscription until detach. */
	holdSubscription(unsubscribe: () => void): void;
	adoptSnapshot(snapshot: PendingSnapshot): void;
	/** The durable rows reconciled against the model (the live one when omitted). */
	reconcile(model?: MobileConversation | null): readonly PendingTurnEntry[];
	demandRead(): void;
	/** Demands a fresh read and resolves once it settles or the binding retires. */
	refresh(): Promise<void>;
	detach(): void;
	forgetProvenance(): void;
}

// Whether two reconciliations describe the same rows. The reconciliation
// always allocates a fresh array, so publishing it on every conversation
// write would re-render every subscriber on each streaming delta even when
// nothing about the rows changed; comparing the fields keeps the reference
// stable until a row actually appears, changes state, or drops out.
export function samePendingRows(
	left: readonly PendingTurnEntry[] | null | undefined,
	right: readonly PendingTurnEntry[],
): boolean {
	if (left === undefined || left === null || left.length !== right.length) {
		return false;
	}
	for (let index = 0; index < right.length; index += 1) {
		const a = left[index];
		const b = right[index];
		if (
			a.id !== b.id ||
			a.method !== b.method ||
			a.state !== b.state ||
			a.source !== b.source ||
			a.text !== b.text ||
			a.imageCount !== b.imageCount ||
			a.createdAt !== b.createdAt ||
			a.fromThisClient !== b.fromThisClient ||
			a.skillNames.length !== b.skillNames.length ||
			a.skillNames.some((name, skillIndex) => name !== b.skillNames[skillIndex])
		) {
			return false;
		}
	}
	return true;
}

// Slice 7a: the durable pending-row seam. While a host binds one, the store
// follows this conversation target's durable outbox/optimistic records and
// projects them as pending rows through the shared reconciliation (#2140's
// shape) — so an in-flight mutation survives a cold reopen (its record is the
// runtime's, not the store's memory), a rejected or reflected one drops out
// (no zombie pendings, no double rows), and a settled one clears exactly when
// its record leaves storage. The reconciliation always reads the live model:
// every durable read re-runs it, and so does every conversation write.
export function createPendingMutationBinding(
	liveModel: () => MobileConversation | null | undefined,
): PendingMutationBinding {
	let port: ConversationMutationPendingPort | null = null;
	let unsubscribe: (() => void) | null = null;
	let generation = 0;
	// The bound seam's "demand a fresh current read" operation and the waiters
	// an awaitable refresh is holding. Retirement resolves those waiters (it
	// cancels the wait, never the commit) so no caller is left awaiting a read
	// from a seam that no longer exists.
	let demandFreshRead: (() => Promise<void>) | null = null;
	const refreshWaiters = new Set<() => void>();
	function settleRefreshes(): void {
		const waiters = [...refreshWaiters];
		refreshWaiters.clear();
		for (const waiter of waiters) waiter();
	}
	// Advanced only by forgetProvenance (close/reset). A read's provenance
	// write is fenced on THIS, not on the binding generation: a read in flight
	// across a same-target rebind or a thread open still observed a durable
	// record of this client's own, and ids are unique per submission so it
	// cannot contaminate another target. Only a teardown fences it out.
	let provenanceGeneration = 0;
	let snapshot: PendingSnapshot | null = null;
	// Every durable record this client submitted, id -> createdAt, carried past
	// the record's own settle so the reconciliation's provenance rule can still
	// answer after the durable read reports the id out of storage.
	// Growth is bounded by the store's own lifetime: a store is per conversation,
	// client mutation ids are unique per submission, and close/reset forget the
	// map. The package store this mirrors deliberately never prunes it, because
	// pruning an id the daemon still reports would flip an own in-flight send to
	// `fromThisClient: false` and misroute tier-6 send/queue availability.
	const submittedHere = new Map<string, number>();

	return {
		get port() {
			return port;
		},
		get snapshot() {
			return snapshot;
		},
		get generation() {
			return generation;
		},
		get provenanceGeneration() {
			return provenanceGeneration;
		},
		submittedHere,
		attach(next) {
			port = next;
			return generation;
		},
		followReads(demandRead) {
			demandFreshRead = demandRead;
		},
		holdSubscription(release) {
			unsubscribe = release;
		},
		adoptSnapshot(next) {
			snapshot = next;
		},
		reconcile(model = liveModel()) {
			if (port === null || snapshot === null || !model) return [];
			const bound = port;
			return reconcilePendingEntries(
				bound.targetRef,
				[...snapshot.outbox, ...snapshot.optimistic],
				model,
				submittedHere,
				(record) => bound.isOwnMutationRecord(record),
			);
		},
		// Demand a fresh read of the bound durable projection for an explicit
		// lifecycle event (a connection recovery or a canonical read demand), not a
		// storage notification. No timer and no loop: a failure leaves readiness false
		// and the next explicit event tries again.
		demandRead() {
			const demand = demandFreshRead;
			if (demand === null) return;
			void demand();
		},
		refresh() {
			const demand = demandFreshRead;
			if (demand === null) return Promise.resolve();
			const completion = demand();
			return new Promise<void>((resolve) => {
				const finish = () => {
					refreshWaiters.delete(finish);
					resolve();
				};
				refreshWaiters.add(finish);
				void completion.then(finish, finish);
			});
		},
		// Stops following the bound seam and forgets its snapshot. Never sets state:
		// the callers that clear the published projection do so through their own
		// set (close/reset null it, the returned unbind publishes the clear).
		// Provenance is deliberately NOT forgotten here: the id -> createdAt map is
		// monotonic knowledge the package store treats as never pruned, so a
		// same-target rebind keeps it and a still-authoritative entry stays honest.
		detach() {
			++generation;
			demandFreshRead = null;
			settleRefreshes();
			unsubscribe?.();
			unsubscribe = null;
			port = null;
			snapshot = null;
		},
		// Forgets the carried provenance. Only a true teardown (close/reset) calls
		// this: a rebind keeps it (same target), and a thread open keeps it too - the
		// map is keyed by client mutation id, unique per submission, and it is the
		// only carrier once a record settles out of storage while the daemon still
		// reports the id.
		forgetProvenance() {
			++provenanceGeneration;
			submittedHere.clear();
		},
	};
}
