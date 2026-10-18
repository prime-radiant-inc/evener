// The client mutation ids a live frame proves the daemon has moved past.
//
// Every client that holds a durable record for a send needs the same answer to
// "which of my records did this frame settle", and the answer is a fact about
// the wire, not about a host: a `history/updated` frame carries the recorded
// form of every item it mentions, a user message's `clientMutationId` among
// them, and a `thread/queueChanged` frame names both the ids still queued and
// the ids this push's own transition consumed. One definition here keeps the
// browser and the phone from disagreeing about what a frame settles.
import type { AnyNotification } from "../../types.gen";
import { validConsumedClientMutationIds } from "./dispatcher";

export function notificationMutationIdentities(n: AnyNotification): string[] {
  if (n.method === "thread/queueChanged") {
    // consumedClientMutationIds names entries THIS push's own transition (a
    // drain) just took out of the queue (issue #1704): the daemon knows
    // exactly which ids it consumed, so those settle by the same positive-
    // evidence rule as the remaining, still-queued ids below. Validated the
    // same way the receipt path validates it: anything not an array of
    // non-empty strings settles nothing, never guessed at by spreading it.
    return [
      ...(n.params.queue.clientMutationIds ?? []),
      ...validConsumedClientMutationIds(n.params.consumedClientMutationIds),
    ];
  }
  // history/updated is the read-model replacement for evener/steering/injected,
  // item/started, item/completed, turn/started and turn/completed alike: it
  // carries the recorded form of every item a change affected (a steering
  // item included), so one pass over its items covers what all five used to
  // carry across their own separate shapes.
  if (n.method === "history/updated") {
    return (n.params.items ?? [])
      .map((item) => item.clientMutationId)
      .filter((clientMutationId): clientMutationId is string => Boolean(clientMutationId));
  }
  return [];
}
