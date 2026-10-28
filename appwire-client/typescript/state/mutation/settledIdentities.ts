// The client mutation ids a live frame proves the daemon has moved past. This
// is a fact about the wire, not a host, so both clients read it here.
import type { AnyNotification } from "../../types.gen";
import { validConsumedClientMutationIds } from "./dispatcher";

export function notificationMutationIdentities(n: AnyNotification): string[] {
  if (n.method === "thread/queueChanged") {
    // consumedClientMutationIds names what THIS push's drain took off the
    // queue; it settles by the same positive-evidence rule as
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

// The ids a frame proves the daemon has APPLIED, not merely received. A
// `thread/queueChanged` frame names the ids still in its queue - proof the
// daemon holds them, which settles a transport question - and the ids this
// push's transition consumed, which have left the queue. A composer row that
// also draws the message itself must wait for the recorded item: settling it
// when the queue row is confirmed would make the message blink out between
// the drain and the item that replaces it.
export function appliedMutationIdentities(n: AnyNotification): string[] {
  if (n.method === "thread/queueChanged") return validConsumedClientMutationIds(n.params.consumedClientMutationIds);
  return notificationMutationIdentities(n);
}
