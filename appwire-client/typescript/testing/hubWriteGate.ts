// Test doubles for the extensions write gate, shared by the suites that need
// the store's own revision fence rather than its serialization: the ordering
// cases issue two overlapping writes, which the real gate refuses by design.
import type { HubWriteGate } from "../state/extensions/hubWriteGate";

/** A HubWriteGate that never refuses and never reports busy: two overlapping
 * writes both reach the wire, so the revision fence can be exercised in
 * isolation from the shared gate. */
export function permissiveHubWriteGate(): HubWriteGate {
  return {
    isBusy: () => false,
    run: async (action) => {
      await action();
      return true;
    },
    subscribe: () => () => {},
    reset: () => {},
  };
}
