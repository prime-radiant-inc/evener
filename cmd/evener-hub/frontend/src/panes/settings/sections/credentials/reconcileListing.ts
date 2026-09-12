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
    const applied = await credentialsStore.getState().fetch();
    if (applied) return matches(credentialsStore.getState().instances);
  }
  return false;
}
