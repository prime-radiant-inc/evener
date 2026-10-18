// The client mutation ids a live frame proves the daemon has moved past. This
// is a fact about the wire, not a host, so both clients read it here.
import type { AnyNotification } from "../../types.gen";
import { validConsumedClientMutationIds } from "./dispatcher";

export function notificationMutationIdentities(n: AnyNotification): string[] {
  if (n.method === "thread/queueChanged") {
    // consumedClientMutationIds names what THIS push's drain took off the
    // queue (issue #1704); it settles by the same positive-evidence rule as
    // the ids still queued.
    return [
      ...(n.params.queue.clientMutationIds ?? []),
      ...validConsumedClientMutationIds(n.params.consumedClientMutationIds),
    ];
  }
  // history/updated carries the recorded form of every item a change affected,
  // replacing the five per-item and per-turn lifecycle notifications.
  if (n.method === "history/updated") {
    return (n.params.items ?? [])
      .map((item) => item.clientMutationId)
      .filter((clientMutationId): clientMutationId is string => Boolean(clientMutationId));
  }
  return [];
}
