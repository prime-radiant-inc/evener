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
// is intentionally absent, and stays absent now that ThreadModel.capabilities
// IS live: thread/status/changed carries the set that goes with the status it
// announces (kata 06t8), so the two always describe the same moment - and for
// that fresh set, this table already computes exactly what reading it would.
// The hub gates Send on "no turn in flight" and Queue on "a turn in flight"
// (server/appwire_runtime.go's appCapabilities), which is tiers 4 and 5; the
// one thing the status alone cannot say is whether the harness wired a queue
// at all, and that is tier 3. A tier 2 would restate the table, not correct
// it.
//
// Capability booleans are therefore still consulted ONLY in tier 3, which is
// also where the legacy code treated them as authoritative (the "explicitly
// known" queue-cap-false branch).
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
