// The decisions the Composer's submit and the QueueStrip's drain share, so the
// two surfaces cannot drift: whether the target can take the draft's
// selections, how a draft reaches the threads store, and what the user hears
// when a recovery row was changed or consumed elsewhere.

import type { ComposerMention } from "@evener/appwire-client";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import type { DisplayedRecoveryStopBarrier } from "../../../stores/mutationOutboxIndexedDB";
import {
  captureRecoveryStopBarrier,
  type InputAttachment,
  readRecoveryWithStopEpoch,
  threadsStore,
} from "../../../stores/threads";
import { acquireUnreadThreadClaim } from "../../../stores/useThreadModel";
import { pushToast } from "../../../widgets/toast/store";

export type DraftSelections = {
  skillNames?: readonly string[];
  commandNames?: readonly string[];
};

export type SelectionRefusal = "skill-unsupported" | "command-unsupported";

// What the target advertises. Absent capabilities (a thread model that has not
// loaded) support nothing, matching the store's own composerMutationIntent gate.
export type SelectionCapabilities = { skillInput?: boolean; commandInput?: boolean } | undefined;

export function selectionRefusal(
  selections: DraftSelections,
  capabilities: SelectionCapabilities,
): SelectionRefusal | undefined {
  if ((selections.skillNames?.length ?? 0) > 0 && capabilities?.skillInput !== true) return "skill-unsupported";
  if ((selections.commandNames?.length ?? 0) > 0 && capabilities?.commandInput !== true) return "command-unsupported";
  return undefined;
}

// Reports a refused selection; the draft is kept by the caller returning
// before anything durable is written.
export function pushSelectionRefusal(refusal: SelectionRefusal): void {
  if (refusal === "skill-unsupported")
    pushToast("error", "Skill selections aren't supported on this session yet; your draft is kept", refusal);
  else pushToast("error", "Command selections are not supported on this session; your draft is kept", refusal);
}

// A recovery row that another surface changed ("changed") or already sent
// ("consumed") under this one.
export type RecoveryRefusal = "changed" | "consumed";

export function pushRecoveryRefusal(refusal: RecoveryRefusal): void {
  if (refusal === "changed") pushToast("info", "This message changed in another tab. Refresh and try again.", refusal);
  else pushToast("info", "This message was already sent in another tab.", refusal);
}

export type DraftRoute = "send" | "queue" | "steer" | "drain";

export type DraftPayload = {
  text: string;
  attachments?: InputAttachment[];
  skillNames?: readonly string[];
  commandNames?: readonly string[];
  mentions?: readonly ComposerMention[];
};

// One store call for all four routes; "drain" is the store's drainAsSteer.
export function submitDraftToStore(
  route: DraftRoute,
  ref: string,
  draft: DraftPayload,
  compositionOwnerId?: string,
): Promise<void> {
  const store = threadsStore.getState();
  const args = [ref, draft.text, draft.attachments, draft.skillNames, draft.commandNames, draft.mentions] as const;
  const call = (method: (typeof store)["send"] | (typeof store)["drainAsSteer"]) => method(...args, compositionOwnerId);
  return route === "drain" ? call(store.drainAsSteer) : call(store[route]);
}

/** The recovery Send's synchronous intent: the two press-time captures a
 * recovery resend fences on, both requested in the press's own synchronous
 * prefix - before any await - so a Stop the user presses while the Send waits
 * on persistence or destination hydration stays in the fence's future.
 *
 * - displayedBarrier: the displayed ref's Stop fence, captured from the ref
 *   this pane renders, INDEPENDENTLY of the row's durable destination. After
 *   a peer retarget followed by an autosave reconciliation the two differ,
 *   and a Stop on the still-displayed session names the displayed ref, so
 *   deriving this capture from the destination would leave that Stop's epoch
 *   fenced by nothing.
 * - durableSnapshot: the row and its current destination's Stop epoch from
 *   one readonly storage snapshot (readRecoveryWithStopEpoch), which serves
 *   as both the send's destination and the destination fence's baseline - a
 *   peer retarget that lands inside the send's own waits cannot move the
 *   destination under the press, and one that lands between the press and
 *   the resend is caught by the caller's target comparison. */
export interface PressedRecoverySendIntent {
  readonly displayedBarrier: Promise<DisplayedRecoveryStopBarrier>;
  readonly durableSnapshot: Promise<{ record: MutationRecoveryRecord | undefined; stopEpoch: number }>;
}

export function captureRecoverySendIntent(displayedRef: string, recoveryId: string): PressedRecoverySendIntent {
  const displayedBarrier: Promise<DisplayedRecoveryStopBarrier> = captureRecoveryStopBarrier(displayedRef).then(
    (barrier) => ({ ...barrier, targetRef: displayedRef }),
  );
  // The capture can fail before the Send reaches its resend; keep that
  // rejection for the sender without an unhandled interim promise, the same
  // guard captureRecoveryStopBarrier gives its own capture.
  void displayedBarrier.catch(() => undefined);
  return {
    displayedBarrier,
    durableSnapshot: readRecoveryWithStopEpoch(recoveryId),
  };
}

/** Performs the resend holding the destination claim lease: a destination
 * this tab is not already reading is hydrated under a foreground claim - the
 * same ensureThread/releaseThread pair useThreadClaim holds a pane's
 * subscription with - acquired only after ensureThread succeeds, so a failed
 * acquisition (whose rollback ensureThread already performed) releases
 * nothing, and the lease is released exactly once, after the resend. A
 * destination another reader already tracks needs no claim and takes none. */
export async function withRecoveryDestinationClaim<T>(destinationRef: string, perform: () => Promise<T>): Promise<T> {
  const lease = await acquireUnreadThreadClaim(destinationRef);
  if (lease === null) return perform();
  try {
    return await perform();
  } finally {
    lease.release();
  }
}
