import { formatElapsed } from "@evener/appwire-client";
import type { PendingMessage } from "@evener/appwire-client/state/mutation";

// The status line in a pending row's meta slot. It replaces the delivered
// timestamp; the bubble itself stays the delivered bubble.
export function pendingMessageCaption(
  row: PendingMessage,
  turnActive: boolean,
  now: number,
  hubRefused = false,
): string {
  switch (row.state) {
    case "waiting":
      return hubRefused ? "Retrying" : "Waiting to send";
    case "sending":
      return hubRefused ? "Retrying" : "Sending";
    case "queued":
      return "Queued, sends when this turn ends";
    case "held":
      return "Held after Stop";
    case "delivering":
      return "Delivering";
    case "uncertain":
      return "Delivery uncertain";
    case "canceled":
      return "Canceled by Stop";
    case "refused":
      if (row.controlFailure === "cancel") {
        return row.reason ? `Couldn't remove from queue: ${row.reason}` : "Couldn't remove from queue";
      }
      return row.reason ? `Couldn't send: ${row.reason}` : "Couldn't send";
    case "orphaned":
      return "Destination deleted";
    case "steering": {
      let stage: string;
      if (hubRefused) stage = "Waiting for the hub";
      else if (turnActive) stage = "Delivers when this step finishes";
      else stage = "Delivers with the next turn";
      const elapsed = row.createdAt === undefined ? undefined : formatElapsed(now - row.createdAt);
      if (elapsed === undefined) return stage;
      return row.optimistic !== undefined ? `${stage} · held ${elapsed}` : `${stage} · ${elapsed}`;
    }
  }
}
