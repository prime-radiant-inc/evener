import type { InstanceEntry } from "../../../../protocol/types.gen";
import { credentialsStore } from "../../../../stores/credentials";

// A superseded write's listing was discarded by the store's generation
// guard, so a caller steering a flow on its own write must reconcile before
// it reports success. fetch() resolves normally even when its response was
// superseded (a newer read is in flight) or failed (the error landed in the
// store), so a resolved promise is never confirmation: retry while the read
// keeps losing the race, and confirm the expected row state only against a
// listing the store actually applied. Never fall back to whatever state is
// left after failed or superseded reads - that leftover is not a listing the
// caller's write produced, and matching it would confirm a mutation the host
// may never have reflected. The bounded retry matters - under concurrent
// refreshes an unbounded loop would spin.
export async function confirmListingState(matches: (instances: InstanceEntry[]) => boolean): Promise<boolean> {
  for (let attempt = 0; attempt < 3; attempt += 1) {
    // A rejected read never applied: fetch() rejects when there is no client
    // (requireClient throws outside readListing's own error handling), and
    // that is one more unapplied attempt, not a failure for the caller to
    // handle. This helper's contract is a boolean, so the retry loop owns the
    // outcome either way.
    const applied = await credentialsStore
      .getState()
      .fetch()
      .catch(() => false);
    if (applied) return matches(credentialsStore.getState().instances);
  }
  return false;
}

/**
 * Lands the store's listing update in a caller whose mutation just succeeded.
 *
 * This is deliberately not a confirmation gate, and its outcome is
 * deliberately not reported: the mutation's own success is already
 * established by its RPC, the store schedules its own post-mutation refresh
 * (see stores/credentials.ts's scheduleRefetch), and a caller that must
 * confirm the listing actually moved before advancing uses
 * confirmListingState (or the guided flow's own refreshAndCheck). A read that
 * is superseded is that newer listing arriving, and a read that rejects (the
 * connection dropped: requireClient throws before readListing's own error
 * handling) is a stale view that the connection banner and a later reconnect
 * read resolve - neither is a failed save, a failed sign-in, or a failed
 * clear, which is what leaving the rejection to the caller's mutation error
 * path reports.
 */
export async function refreshListingAfterMutation(): Promise<void> {
  await credentialsStore
    .getState()
    .fetch()
    .catch(() => {});
}
