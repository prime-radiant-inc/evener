// deriveSendQueueAvailability computes whether the composer's Send and
// Queue actions are currently available, ported from the legacy composer's
// send-vs-queue capability precedence (parity-m5-composer.md §A, lines
// 64-71, citing cmd/serf-hub/assets/renderer.js:479-513):
//
//   1. ended/closed              -> send=false, queue=false
//   2. [DROPPED - see below]
//   3. active && capabilities.queue === false explicitly -> both false
//   4. active                    -> send=false, queue=true (queue-mode default)
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
// The active tier checks `statusType === "active"` ALONE - verified directly
// against the cited renderer.js:479-513 (updateThreadState's sendBtn
// branches), which key only on the `state` string and `this.liveSendCap`/
// `this.liveQueueCap`; nowhere in that chain does it read activeTurnId. The
// stronger `state === "active" && !!activeTurnId` formula lives in a
// DIFFERENT, deliberately-shared predicate (thread-state.js:16-18,
// `SerfThreadState.isBusy`) that gates interrupt/steer/model-switch, not
// send/queue - that file's own header comment lists exactly those three
// call sites and none of them is the composer's send/queue capability
// chain. Folding isBusy's activeTurnId check into THIS gate would also add
// a real race this store must not have: thread/status/changed (which flips
// ThreadModel.status.type to "active") and turn/started (which populates
// ThreadModel.activeTurnId) are two separate notifications, so there is a
// window where status already reads "active" but activeTurnId hasn't
// arrived yet. The verbatim table queues in that window; requiring
// activeTurnId would instead fall through to the plain-send default and
// let a legitimate queue attempt bounce off the daemon as a ConflictError.
// This helper therefore does not take activeTurnId as an input at all.

import type { ThreadCapabilities } from "./types.gen";

export interface SendQueueAvailabilityInput {
  statusType: string;
  capabilities: ThreadCapabilities;
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
}: SendQueueAvailabilityInput): SendQueueAvailability {
  if (statusType === "ended" || statusType === "closed") return BOTH_UNAVAILABLE;
  if (statusType !== "active") return PLAIN_SEND_MODE;

  if (capabilities.queue === false) return BOTH_UNAVAILABLE;
  return QUEUE_MODE;
}
