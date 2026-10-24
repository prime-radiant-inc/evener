// One Retry eligibility decision for both clients (client-state cleanup
// phase 3). Retry is the latest canonical turn's own failure, with no
// unresolved composer input of this client's and a loaded pending projection.
//
// The caller supplies the clicked turn's failure fact. The two clients learn
// it from different shapes: the browser's activation recheck reads the
// canonical turn's error, and native's row check reads the clicked turn's
// projected failure row. The two existing predicates own their own rules:
// isLatestRetryTurn owns canonical tail identity and ownPendingTurnInput owns
// "this client already has input in flight".
import { ownPendingTurnInput, type PendingTurnEntry } from "./state/mutation/pendingEntries";
import { isLatestRetryTurn, type RetryTurnThread } from "./turnContinuation";

/** Why a Retry press is or is not allowed. The denial reasons are distinct
 * because each surface renders a different answer for them: not-latest and
 * not-failed hide the action, the pending reasons and no-live-service disable
 * it or refuse activation. */
export type RetryEligibilityReason =
  | "allowed"
  | "not-latest-turn"
  | "clicked-turn-not-failed"
  | "pending-input-owned"
  | "pending-not-ready"
  | "no-live-service";

export type RetryEligibilityDenialReason = Exclude<RetryEligibilityReason, "allowed">;

export interface RetryEligibilityInput {
  /** The turn whose failure row offered the action. */
  readonly clickedTurnId: string;
  /** The canonical owner's identity and live state, never a viewport's rows.
   * `isLatestRetryTurn` owns the latest-turn rule over it. */
  readonly thread: RetryTurnThread;
  /** Whether the clicked turn currently carries the failure that offered the
   * action. The caller re-reads this from its own authority at activation. */
  readonly clickedTurnFailed: boolean;
  /** This client's reconciled pending send/queue rows. Null or undefined means
   * the durable read has not answered yet; ownPendingTurnInput treats it as
   * "no own input", so `pendingMutationsReady` must decide readiness. */
  readonly pendingMutations: readonly PendingTurnEntry[] | null | undefined;
  /** Whether `pendingMutations` is a verified current durable projection. */
  readonly pendingMutationsReady: boolean;
  /** Whether a live service is bound to submit the continuation. */
  readonly serviceReady: boolean;
}

export type RetryEligibility =
  | { readonly allowed: true; readonly reason: "allowed" }
  | { readonly allowed: false; readonly reason: RetryEligibilityDenialReason };

/** The one Retry decision. Precedence is part of the contract: the first
 * failing fact names the answer, in this order. A missing live service comes
 * before the pending answers because a client whose Send cannot act hides the
 * action outright; readiness and own input only decide whether an available
 * action is disabled. */
export function retryEligibility(input: RetryEligibilityInput): RetryEligibility {
  if (!isLatestRetryTurn(input.thread, input.clickedTurnId)) {
    return { allowed: false, reason: "not-latest-turn" };
  }
  if (!input.clickedTurnFailed) {
    return { allowed: false, reason: "clicked-turn-not-failed" };
  }
  if (!input.serviceReady) {
    return { allowed: false, reason: "no-live-service" };
  }
  if (ownPendingTurnInput(input.pendingMutations)) {
    return { allowed: false, reason: "pending-input-owned" };
  }
  if (!input.pendingMutationsReady) {
    return { allowed: false, reason: "pending-not-ready" };
  }
  return { allowed: true, reason: "allowed" };
}
