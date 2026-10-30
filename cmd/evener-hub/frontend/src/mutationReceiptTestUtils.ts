import type { MutationReceipt } from "@evener/appwire-client";

/**
 * appliedReceipt builds the mutation receipt an accepted mutation answers
 * with: disposition "applied", projectionState "reflected", the caller's
 * client mutation id, and the test world's thread id. The identical literal
 * was copy-pasted into every composer, Session and palette test's fake.on
 * handler; it lives here once. A response that needs a different disposition
 * or projection passes it in `overrides` — the call site stays explicit.
 */
export function appliedReceipt(
  clientMutationId: string,
  threadId: string,
  overrides: Partial<MutationReceipt> = {},
): MutationReceipt {
  return {
    clientMutationId,
    disposition: "applied",
    threadId,
    projectionState: "reflected",
    ...overrides,
  };
}
