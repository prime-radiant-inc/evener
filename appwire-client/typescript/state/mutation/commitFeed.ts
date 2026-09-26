import type { PendingTurnsStore } from "./pendingTurns";
import type { MutationProjectionFence } from "./projection";
import type { MutationAttachmentRef, MutationOutboxRecord } from "./records";

// A live commit this client made just now: the durable record it wrote, and
// the recovery entry it retires when the commit was a resend.
export interface MutationCommit<A extends MutationAttachmentRef = MutationAttachmentRef> {
  record: MutationOutboxRecord<A>;
  recoveryId?: string;
}

// The host's notice of durable mutation activity: which targets changed and,
// when the change was this client's own commit landing rather than something
// a refresh has yet to discover, the commit itself. The web's
// subscribeMutationPersistence already has exactly this shape.
export interface MutationCommitFeed<A extends MutationAttachmentRef = MutationAttachmentRef> {
  subscribe(listener: (targetRefs: string[], committed?: MutationCommit<A>) => void): () => void;
}

// Applies a live commit straight into the store and fence with no durable
// read at all - the fast path a submission's own write takes - then asks the
// host to refresh whichever targets the feed named, so this store also
// converges with anything that same event reports it does not yet know
// (another tab's commit, a discovery scan). The fast path runs synchronously,
// before `refresh` is ever called, so this client's own send is visible
// immediately rather than waiting on a refresh's slower durable read to
// confirm it.
export function wireMutationCommitFeed<A extends MutationAttachmentRef = MutationAttachmentRef>(
  store: PendingTurnsStore<A>,
  fence: MutationProjectionFence<A>,
  feed: MutationCommitFeed<A>,
  refresh: (ref?: string) => void,
): () => void {
  return feed.subscribe((targetRefs, committed) => {
    if (committed) {
      const { record, recoveryId } = committed;
      fence.advance(record.targetRef);
      store.recordSubmittedHere({ outbox: [record], optimistic: [] });
      store.setState((state) => {
        const recovery = new Map(state.recovery);
        if (recoveryId) recovery.delete(recoveryId);
        return { outbox: new Map(state.outbox).set(record.clientMutationId, record), recovery };
      });
    }
    if (targetRefs.length === 0) {
      refresh();
      return;
    }
    for (const ref of targetRefs) refresh(ref);
  });
}
