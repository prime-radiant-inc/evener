// deriveSendQueueAvailability computes whether the composer's Send and
// Queue actions are currently available, ported from the legacy composer's
// send-vs-queue capability precedence (parity-m5-composer.md §A, lines
// 64-71, citing cmd/serf-hub/assets/renderer.js:479-513):
//
//   1. ended/closed              -> send=false, queue=false
//   2. [DROPPED - see below]
//   3. busy && capabilities.queue === false explicitly -> both false
//   4. busy                      -> send=false, queue=true (queue-mode default)
//   5. else (idle/awaiting/...)  -> send=true,  queue=false (plain-send default)
//
// The legacy tier 2 ("the source already advertised live send/queue
// capabilities for the CURRENT state, `liveCapabilitiesStatus === state`")
// is intentionally absent: ThreadModel.capabilities (protocol/model.ts) is
// hydrated from the thread/read snapshot only and has NO live push on the
// wire (verified against appwire/protocol.go's Notifications catalog - no
// capabilities-changed entry exists), so this store can never honestly
// claim a capabilities snapshot was "just advertised for the current
// status" the way the legacy client's own live event stream could. Using a
// possibly-stale snapshot as a blanket override would silently reintroduce
// the exact bug class the wave-4 lesson warns about (trusting a shape the
// wire doesn't actually (re-)produce on every state change) - so capability
// booleans are consulted ONLY in tier 3, exactly where the legacy code
// itself already treats them as authoritative regardless of freshness (the
// "explicitly known" queue-cap-false branch). Live capability push is a
// wire-candidate, out of scope this wave.
//
// "busy" mirrors the legacy's own centralized predicate exactly (parity §A
// first bullet: `state === "active" && !!activeTurnId`, never redefined
// elsewhere) rather than statusType==="active" alone, so a wire
// inconsistency (a status snapshot saying "active" with no reserved turn
// id) degrades to the safe plain-send default instead of assuming
// queue-mode.

import type { ThreadCapabilities } from "./types.gen";

export interface SendQueueAvailabilityInput {
  statusType: string;
  capabilities: ThreadCapabilities;
  activeTurnId: string | undefined;
}

export interface SendQueueAvailability {
  canSend: boolean;
  canQueue: boolean;
}

const BOTH_UNAVAILABLE: SendQueueAvailability = { canSend: false, canQueue: false };
const QUEUE_MODE: SendQueueAvailability = { canSend: false, canQueue: true };
const PLAIN_SEND_MODE: SendQueueAvailability = { canSend: true, canQueue: false };

export function deriveSendQueueAvailability({
  statusType,
  capabilities,
  activeTurnId,
}: SendQueueAvailabilityInput): SendQueueAvailability {
  if (statusType === "ended" || statusType === "closed") return BOTH_UNAVAILABLE;

  const busy = statusType === "active" && activeTurnId !== undefined;
  if (!busy) return PLAIN_SEND_MODE;

  if (capabilities.queue === false) return BOTH_UNAVAILABLE;
  return QUEUE_MODE;
}
