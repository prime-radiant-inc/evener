// One Send/Queue action decision for every client (client-state cleanup
// phase 3). The package's send/queue table (deriveSendQueueAvailability) owns
// every tier; this gate owns the composition the clients used to duplicate:
// restart, the client's own recovery face, connectivity, shut-down
// conversion, the send-capability policy, and the queue-first mapping to an
// action.
//
// The first matching row wins. The order is the API contract, not an
// implementation detail:
//
//   1. restartRequired                        -> none("restart-required")
//   2. recovery blocked                       -> none("recovery-fenced")
//   3. recovery send-resumes                  -> send("recovery-send")
//   4. recovery folded-resume                 -> pending send ? none("pending-send-in-flight")
//                                                : send("resume-folded")
//   5. recovery explicit-resume               -> pending send ? table(hasPendingSend: true),
//                                                queue when it can, else none("queue-unavailable")
//                                                : resume("resume")
//   6. offline                                -> shut-down first message ? capabilities.send
//                                                ? resume("resume") : none("send-unavailable")
//                                                : table(hasPendingSend: true),
//                                                  queue when it can, else none("offline-no-queue")
//   7. online table with the real pending bit -> queue("queue") when it can queue;
//                                                shut-down and capabilities.send ?
//                                                  policy.shutDownAction === "resume"
//                                                    ? resume("resume") : send("shut-down-send")
//                                                : table can send and
//                                                  (!policy.sendRequiresCapability || capabilities.send)
//                                                    ? send("plain-send")
//                                                : table can send ? none("send-unavailable")
//                                                : none("no-action")
//
// Restart is first because every reachable current path already answers none
// for it: native checks restart before its resume and offline branches, and
// the browser's restart obligation makes its recovery fence return blocked.
// The gate also answers none for the synthetic restartRequired + folded-resume
// and restartRequired + send-resumes pairs, which the old browser would have
// answered send. Neither pair is reachable: the hub never stamps
// ResumeOnlyFoldable for a restart-required daemon, and the browser's
// isSendResumes explicitly excludes restartRequired.
//
// The recovery face is an input, never re-derived here. It is the one new
// client-to-shared seam: the gate knows what a client's fence means, not the
// fence's signals. The two policy facts are honest client behavior
// differences, not client identity, and keep them from being silently
// unified.
import { deriveSendQueueAvailability, type SendQueueAvailability } from "./sendQueueAvailability";
import { SHUT_DOWN_STATUSES } from "./submitRouting";
import type { ThreadCapabilities } from "./types.gen";

export type SendQueueAction = "send" | "queue" | "resume" | "none";

export type SendQueueReason =
  // allowed
  | "plain-send" // table tier 5: open status, no pending send
  | "shut-down-send" // browser: first message to a shut-down session
  | "resume-folded" // browser: hub-admitted folded resume
  | "recovery-send" // browser: Send drives this client's resume
  | "queue"
  | "resume" // native: explicit resume before the send
  // refused
  | "restart-required"
  | "recovery-fenced"
  | "pending-send-in-flight"
  | "send-unavailable"
  | "queue-unavailable"
  | "offline-no-queue"
  | "no-action";

export type SendQueueDecision =
  | {
      readonly action: "send";
      readonly reason: "plain-send" | "shut-down-send" | "resume-folded" | "recovery-send";
    }
  | { readonly action: "queue"; readonly reason: "queue" }
  | { readonly action: "resume"; readonly reason: "resume" }
  | {
      readonly action: "none";
      readonly reason: Exclude<
        SendQueueReason,
        "plain-send" | "shut-down-send" | "resume-folded" | "recovery-send" | "queue" | "resume"
      >;
    };

/** The recovery face a client already computed. The gate never computes a
 *  fence, reads a store, or infers a resume from a status. */
export type SendQueueRecovery =
  | { readonly kind: "none" }
  | { readonly kind: "folded-resume" } // browser resumeOnly
  | { readonly kind: "send-resumes" } // browser sendResumes
  | { readonly kind: "explicit-resume" } // native resumeRequired
  | { readonly kind: "blocked" }; // browser stillFenced

/** Two honest client policy facts, not client identity. */
export interface SendQueuePolicy {
  /** Whether a table plain-send requires capabilities.send. Native true,
   *  browser false (the browser table's tier 5 ignores it). Recovery and
   *  shut-down branches keep their own capability rules. */
  readonly sendRequiresCapability: boolean;
  /** What a shut-down session's first message is called. Browser "send"
   *  (turn/start folds the resume); native "resume" (explicit resume first). */
  readonly shutDownAction: "send" | "resume";
}

export interface SendQueueGateInput {
  readonly statusType: string;
  readonly capabilities: Pick<ThreadCapabilities, "send" | "queue">;
  /** ownPendingSend over this client's reconciled pending entries. Never the
   *  daemon's session-wide pendingMutations projection. */
  readonly hasPendingSend: boolean;
  /** Native: the live connection. Browser: always true; the browser has no
   *  connectivity branch. */
  readonly connected: boolean;
  readonly recovery: SendQueueRecovery;
  readonly policy: SendQueuePolicy;
}

export const BROWSER_SEND_QUEUE_POLICY: SendQueuePolicy = {
  sendRequiresCapability: false,
  shutDownAction: "send",
};

export const NATIVE_SEND_QUEUE_POLICY: SendQueuePolicy = {
  sendRequiresCapability: true,
  shutDownAction: "resume",
};

// The table's declared input names the whole wire capability set, and its
// tiers read only send and queue (see its header: both reads are the
// harness's bits). The gate promises its callers the same two bits.
function tableAvailability(
  statusType: string,
  capabilities: Pick<ThreadCapabilities, "send" | "queue">,
  hasPendingSend: boolean,
): SendQueueAvailability {
  return deriveSendQueueAvailability({
    statusType,
    capabilities: capabilities as ThreadCapabilities,
    hasPendingSend,
  });
}

export function sendQueueGate(input: SendQueueGateInput): SendQueueDecision {
  const { statusType, capabilities, hasPendingSend, connected, recovery, policy } = input;

  // 1. Restart before every recovery face: the daemon's older process must be
  // stopped first, so neither Send nor Queue nor a resume may act.
  if (statusType === "restartRequired") return { action: "none", reason: "restart-required" };

  // 2. A fenced client has already refused both actions.
  if (recovery.kind === "blocked") return { action: "none", reason: "recovery-fenced" };

  // 3. Send drives this client's own resume, even with a send already parked.
  if (recovery.kind === "send-resumes") return { action: "send", reason: "recovery-send" };

  // 4. A hub-admitted folded resume offers Send only with no pending send.
  if (recovery.kind === "folded-resume") {
    return hasPendingSend
      ? { action: "none", reason: "pending-send-in-flight" }
      : { action: "send", reason: "resume-folded" };
  }

  // 5. Native's explicit resume: the first message resumes, and a message
  // behind one this client already holds queues where the harness can. The
  // resume itself is never gated on send: the wire fence clears that bit as
  // it sets resumeRequired.
  if (recovery.kind === "explicit-resume") {
    if (!hasPendingSend) return { action: "resume", reason: "resume" };
    const availability = tableAvailability(statusType, capabilities, true);
    return availability.canQueue
      ? { action: "queue", reason: "queue" }
      : { action: "none", reason: "queue-unavailable" };
  }

  // 6. Offline, a message waits in the phone's outbox. By the time it arrives a
  // turn may be running, so every non-resume route asks the table as if a send
  // of this phone's were already pending (tier 6).
  if (!connected) {
    if (SHUT_DOWN_STATUSES.has(statusType) && !hasPendingSend) {
      return capabilities.send
        ? { action: "resume", reason: "resume" }
        : { action: "none", reason: "send-unavailable" };
    }
    const availability = tableAvailability(statusType, capabilities, true);
    return availability.canQueue
      ? { action: "queue", reason: "queue" }
      : { action: "none", reason: "offline-no-queue" };
  }

  // 7. Online: the table applies with the real pending bit.
  const availability = tableAvailability(statusType, capabilities, hasPendingSend);
  if (availability.canQueue) return { action: "queue", reason: "queue" };
  // A shut-down session has no runtime: a first message resumes it.
  if (SHUT_DOWN_STATUSES.has(statusType) && capabilities.send) {
    return policy.shutDownAction === "resume"
      ? { action: "resume", reason: "resume" }
      : { action: "send", reason: "shut-down-send" };
  }
  if (availability.canSend) {
    // Send offers only what the client's store will take. Native requires the
    // hub's send capability whatever the status; the browser's table tier 5
    // ignores it.
    return !policy.sendRequiresCapability || capabilities.send
      ? { action: "send", reason: "plain-send" }
      : { action: "none", reason: "send-unavailable" };
  }
  return { action: "none", reason: "no-action" };
}
