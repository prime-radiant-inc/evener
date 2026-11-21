// The decisions the Composer's submit and its own drain route share, so the
// two surfaces cannot drift: whether the target can take the draft's
// selections, how a draft reaches the threads store, and what the user hears
// when a recovery row was changed or consumed elsewhere.

import { type ComposerMention, markerPattern } from "@evener/appwire-client";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import type { DisplayedRecoveryStopCapture } from "../../../stores/mutationOutboxIndexedDB";
import { type InputAttachment, readRecoveryWithStopEpoch, threadsStore } from "../../../stores/threads";
import { acquireUnreadThreadClaim } from "../../../stores/useThreadModel";
import { pushToast } from "../../../widgets/toast/store";

export type DraftSelections = {
  skillNames?: readonly string[];
  commandNames?: readonly string[];
};

export type SelectionRefusal = "skill-unsupported" | "command-unsupported";

/** An adopted image anchor the request would carry with no image behind it.
 * The composer states this in place beside the draft; the toast exists for the
 * routes whose press does not pass through that surface (a typed /queue or
 * /steer submits its own arguments through the same seam). */
export type UnbackedImageRefusal = "unbacked-image";

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

export function pushUnbackedImageRefusal(refusal: UnbackedImageRefusal): void {
  pushToast(
    "error",
    "This draft refers to an image this composer does not have. Remove the reference, or attach the image again, before sending.",
    refusal,
  );
}

// A recovery row that another surface changed ("changed") or already sent
// ("consumed") under this one. A resend a Stop crossed answers "sent" with a
// born-canceled row - the composition's durable copy - so no distinct fenced
// refusal exists.
export type RecoveryRefusal = "changed" | "consumed";

export function pushRecoveryRefusal(refusal: RecoveryRefusal): void {
  if (refusal === "changed") pushToast("info", "This message changed in another tab. Refresh and try again.", refusal);
  else pushToast("info", "This message was already sent in another tab.", refusal);
}

/** The marker numbers a text spells, ascending and deduplicated. */
export function spelledImageMarkers(text: string): number[] {
  const markers = Array.from(text.matchAll(markerPattern()), (match) => Number(match[1]));
  return [...new Set(markers)].sort((left, right) => left - right);
}

/**
 * The adopted image anchors a request would carry with no image behind them.
 *
 * `adopted` is the set of markers a draft's WRITER staged whose bytes this
 * composer does not hold - the provenance a persisted draft carries with its
 * revision, read on mount and on rebind alike. It is the only thing that may
 * refuse a send. A marker the person typed is prose: it never enters
 * `adopted`, so it keeps sending as the literal text it is.
 *
 * The test is a pair read at the moment of sending - the outgoing text and
 * that same adoption record - never a remembered backed/unbacked flag. What
 * the record says is which occurrences the writing side STAGED; which of them
 * are still spelled is read from the live text here, so an edit that removes
 * one stops refusing and an undo that restores it starts refusing again. A
 * marker number alone could never do that job after a renumbering merge,
 * where the same number can name a different occurrence entirely.
 *
 * The attachment store deliberately has no say. Staging writes "[image N]"
 * into the text AND appends the tile in the same gesture, so a tile can only
 * belong to the text this source wrote. Text this source adopted is by
 * definition text another writer produced, and its bytes never travelled with
 * it (localStorage holds no bytes), so a tile already in the store at that
 * number is a different image - the composition this source replaced. Letting
 * it back the adopted marker would rewrite the adopted prose into a reference
 * carrying somebody else's bytes, which this issue forbids ("do not silently
 * substitute another image by numeric marker").
 */
export function unbackedImageMarkersIn(text: string, adopted: readonly number[]): number[] {
  if (adopted.length === 0) return [];
  const spelled = new Set(spelledImageMarkers(text));
  return [...new Set(adopted)].filter((marker) => spelled.has(marker)).sort((left, right) => left - right);
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
  const args = [
    ref,
    draft.text,
    draft.attachments,
    draft.skillNames,
    draft.commandNames,
    draft.mentions,
    compositionOwnerId,
  ] as const;
  const call = (method: (typeof store)["send"] | (typeof store)["drainAsSteer"]) => method(...args);
  return route === "drain" ? call(store.drainAsSteer) : call(store[route]);
}

/** The recovery Send's synchronous intent: the press-time capture pair a
 * recovery resend fences on, requested in the press's own synchronous
 * prefix - before any await - so a Stop the user presses while the Send waits
 * on persistence or destination hydration stays in the fence's future.
 *
 * Both halves read ONE readonly snapshot (readRecoveryWithStopEpoch):
 * - displayedBarrier: the displayed ref's Stop fence, captured from the ref
 *   this pane renders, INDEPENDENTLY of the row's durable destination. After
 *   a peer retarget followed by an autosave reconciliation the two differ,
 *   and a Stop on the still-displayed session names the displayed ref, so
 *   deriving this capture from the destination would leave that Stop's epoch
 *   fenced by nothing. One snapshot means one transaction per press, and
 *   both fence halves read one consistent baseline.
 * - durableSnapshot: the row and its current destination's Stop epoch, which
 *   serves as both the send's destination and the destination fence's
 *   baseline - a peer retarget that lands inside the send's own waits cannot
 *   move the destination under the press, and one that lands between the
 *   press and the resend is caught by the caller's target comparison. */
export interface PressedRecoverySendIntent {
  readonly displayedBarrier: Promise<DisplayedRecoveryStopCapture>;
  readonly durableSnapshot: Promise<{ record: MutationRecoveryRecord | undefined; stopEpoch: number }>;
}

export function captureRecoverySendIntent(displayedRef: string, recoveryId: string): PressedRecoverySendIntent {
  const snapshot = readRecoveryWithStopEpoch(recoveryId, displayedRef);
  const displayedBarrier = snapshot.then((capture) => ({ stopEpoch: capture.displayedStopEpoch, displayedRef }));
  const durableSnapshot = snapshot.then((capture) => ({ record: capture.record, stopEpoch: capture.stopEpoch }));
  // A submission that exits through one half's rejection (the capture read
  // failed while the Send waited) leaves the other half forever unobserved,
  // and a rejected promise with no handler of its own surfaces as an unhandled
  // rejection. Give each half its own inert rejection sink - callers still
  // observe real rejections through their own await, but the unobserved half
  // can never surface as an unhandled one (round-5 L2).

  displayedBarrier.catch(() => {});
  durableSnapshot.catch(() => {});
  return { displayedBarrier, durableSnapshot };
}

/** Performs the resend while holding a refcounted destination claim lease so
 * the destination model stays pinned until `perform` settles; see
 * `acquireUnreadThreadClaim`. */
export async function withRecoveryDestinationClaim<T>(destinationRef: string, perform: () => Promise<T>): Promise<T> {
  const lease = await acquireUnreadThreadClaim(destinationRef);
  try {
    return await perform();
  } finally {
    lease.release();
  }
}
