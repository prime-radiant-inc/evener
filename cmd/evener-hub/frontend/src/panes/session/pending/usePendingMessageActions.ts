import type { PendingMessage } from "@evener/appwire-client/state/mutation";

type PendingRowKind = "recovery" | "queue" | "local";

export function pendingRowActionOwner(row: PendingMessage): PendingRowKind {
  // A live queue entry owns the actions even when an unedited recovery copy
  // still accompanies it. Activating that recovery's edit would leave the
  // daemon's queued entry behind; an independently edited recovery is its own
  // composition and keeps recovery ownership.
  const editedRecovery = (row.recovery?.compositionRevision ?? 0) > 0;
  // An unattempted, canceled or uncertain local promotion inherits its source
  // entry's queueIndex. That index names the message it would consume, not an
  // entry the daemon owns on its behalf. Retry and Remove belong to the local
  // command, so Remove leaves the original queue entry intact.
  const local = row.outbox;
  if (
    local?.method === "turn/promoteQueuedAsSteer" &&
    (local.state === "blockedUnknown" ||
      row.state === "canceled" ||
      (local.attempted === false && row.state !== "delivering"))
  )
    return "local";
  if (row.queueEntryId !== undefined && row.queueIndex !== undefined && !editedRecovery) return "queue";
  if (row.recovery) return "recovery";
  if (row.queueEntryId !== undefined && row.queueIndex !== undefined) return "queue";
  return "local";
}
