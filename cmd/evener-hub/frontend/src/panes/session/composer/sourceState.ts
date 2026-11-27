import {
  adoptedMarkerRanges,
  type ComposerMention,
  type ImageMarkerRange,
  markerNumbersOf,
  mergeDraftText,
  type TextSplice,
  validMarkerRanges,
} from "@evener/appwire-client";
import { createStore } from "zustand/vanilla";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import { createSecureUUID } from "../../../stores/secureUUID";
import type { InputAttachment } from "../../../stores/threads";
import { pushToast } from "../../../widgets/toast/store";
import {
  type AttachmentStore,
  createAttachmentOperations,
  createAttachmentStore,
  type PendingAttachment,
  type TextEditor,
  type TextEditSource,
} from "./attachments/useAttachments";
import {
  clearDraft,
  clearPersistedDraftIfRevisionMatches,
  markDraftEdited,
  readComposerDraft,
  readComposerDraftRevisionId,
  readComposerDraftSnapshot,
  readDraftRevision,
  writeComposerDraft,
} from "./draft";
import {
  discardRecoveryPendingTurn,
  preserveRecoveryPendingTurn,
  refreshPendingTurnsProjection,
  subscribeComposerSubmissionCommitted,
  updateRecoveryPendingTurn,
} from "./queue/pendingTurnsStore";
import { compositionIsEmpty, mergeRecoveryComposerDraft, recoveryComposerDraft } from "./recovery/recoveryDraft";
import { sameImageRanges, sameStrings } from "./selection";
import {
  extractMentionsInRange,
  materializeSelectionReferences,
  parseSkillDocument,
  patchSelectionText,
  type SkillEditorValue,
  serializeSkillDocument,
  stagedImageRangesAddedBy,
} from "./skillDocument";
import { pushRecoveryRefusal, spelledImageMarkers } from "./submitProtocol";

type RevisionRef<T> = { current: T };
export type RecoveryOwnerLease = { clientMutationId: string; compositionRevision: number };
type RecoveryComposition = {
  text: string;
  attachments: readonly InputAttachment[];
  skillNames: readonly string[];
  commandNames: readonly string[];
  mentions: readonly ComposerMention[] | undefined;
};
/** What the durable owner already holds, so an echo write changes nothing. */
function recoveryCompositionFingerprint(composition: RecoveryComposition): string {
  return JSON.stringify({
    text: composition.text,
    attachments: composition.attachments.map((item) => [item.marker, item.name, item.mediaType, item.data]),
    skillNames: composition.skillNames,
    commandNames: composition.commandNames,
    mentions: composition.mentions ?? [],
  });
}
type SelectionMeta = { commandNames: string[]; mentions: ComposerMention[] | undefined };

export interface ComposerSourceSnapshot {
  text: string;
  skillNames: string[];
  commandNames: string[];
  mentions?: ComposerMention[];
  /** Image anchors a draft's writer STAGED that this source holds no bytes
   * for. Provenance, not a claim about the current text: it is intersected
   * with whatever the text spells at the moment of sending (submitProtocol's
   * unbackedImageMarkersIn), so deleting an anchor clears the refusal and an
   * editor undo that restores it brings the refusal back. It never contains a
   * marker the writer typed, which is what keeps literal prose sendable. */
  adoptedImageMarkers: number[];
  /** Those same occurrences by exact UTF-16 position, so a repair deletes the
   * adopted spelling and not an identical literal beside it. Derived from the
   * editor document's own stagedAnchor marks, so it follows an edit or an undo
   * exactly; the number list above stays the render-time view. */
  adoptedImageRanges: ImageMarkerRange[];
  /** The occurrences THIS source's staging gestures wrote and its tiles still
   * back, by exact UTF-16 position. A tile's number alone cannot tell the
   * anchor it staged from an identical literal the person typed beside it, so
   * the record names the occurrence: a reloading reader then marks only that
   * one, and the repair removes only the reference whose image is gone. */
  stagedImageRanges: ImageMarkerRange[];
  activeRecoveryId: string | null;
  recoveryActivating: boolean;
  recoveryOperationBusy: boolean;
  builtinOperationBusy: boolean;
  freshRecoveryRef: string | null;
  restoreEpoch: number;
}

export interface ComposerSourceState {
  readonly ref: string;
  readonly alive: boolean;
  /** The ref an active recovery send must target: the row's durable
   * destination, which follows a peer retarget once a write of ours has
   * observed the moved row. The pane may still display this source's own ref
   * after such a retarget, so this is not necessarily `ref`. */
  readonly recoveryTargetRef: string;
  readonly attachments: AttachmentStore;
  readonly editor: TextEditor;
  readonly continuity: {
    textRef: RevisionRef<string>;
    skillNamesRef: RevisionRef<string[]>;
    selectionMetaRef: RevisionRef<SelectionMeta>;
    draftEditRevisionRef: RevisionRef<number>;
    /** The persisted draft revision id this source last wrote or read AND still
     * owns. Compared against readComposerDraftRevisionId(ref), never against
     * the tab-local counter: the draft is shared by every tab, so a counter
     * bump of ours says nothing about a write that happened in another tab. */
    ownedDraftRevisionIdRef: RevisionRef<string | undefined>;
    /** The tab-local counter at this source's last sync. Necessary, never
     * sufficient: a legacy v1 record carries no revision id, so only the
     * counter can show that another writer in THIS tab produced it. */
    ownedDraftRevisionRef: RevisionRef<number>;
    recoveryOwnerLeaseRef: RevisionRef<RecoveryOwnerLease | null>;
    recoveryWriteVersionRef: RevisionRef<number>;
    recoveryReplacementEpochRef: RevisionRef<number>;
    recoveryOwnsLocalDraftRef: RevisionRef<boolean>;
  };
  getSnapshot(): ComposerSourceSnapshot;
  subscribe(listener: () => void): () => void;
  bindEditor(editor: TextEditor): () => void;
  updateText(text: string): void;
  editText(text: string): void;
  updateSkillNames(names: string[]): void;
  editSkillNames(names: string[]): void;
  updateSelectionMeta(value: SkillEditorValue): void;
  setActiveRecoveryId(record: MutationRecoveryRecord | null): void;
  markRestore(): void;
  /** Replaces the exact adopted occurrences. Always sent together with the
   * marker list derived from them: the editor's own marks are what both
   * describe, and a caller that moved one without the other would leave the
   * render and the repair disagreeing about which spelling is the anchor. */
  setAdoptedImageRanges(ranges: readonly ImageMarkerRange[], markers?: readonly number[]): void;
  /** Retires the adoption record AND its reservations, because the whole draft
   * this source carried is being replaced: nothing left in the editor can put
   * one of its occurrences back with an undo. */
  clearAdoptedImageMarkers(): void;
  /** Replaces the exact staged occurrences a save carries. Always taken from
   * the editor document's own stagedImage marks, so it agrees with the text it
   * describes. */
  setStagedImageRanges(ranges: readonly ImageMarkerRange[]): void;
  /** Whether this source still owns the draft currently persisted under `ref`.
   * Ownership is the PERSISTED revision identity, so another tab's write - or
   * a background rewrite of ours - ends it. Never a tab-local counter: two
   * tabs share one draft, and a counter cannot see the other tab's write. */
  ownsDraftRevision(): boolean;
  /** Adopt the draft currently persisted under `ref` (or its absence) as this
   * source's own. Both ownership signals are snapshotted together, so callers
   * that change the persisted record out of band never move only one of them. */
  claimDraftRevision(): void;
  persistDraft(text: string): void;
  placeShare(id: string, text: string): boolean;
  readonly pendingShareId: string | undefined;
  clearPendingShare(id: string): boolean;
  /** `splice` is the exact span the caller's own edit replaced, when it knows
   * it. Passing it keeps a marker the edit did not touch from being read as
   * spliced through (see TextEditor.write). */
  writeDraft(
    text: string,
    mayPersist: boolean,
    source?: TextEditSource,
    splice?: TextSplice,
    restoredMentions?: readonly ComposerMention[],
  ): void;
  refreshRecovery(): void;
  readonly compositionOwnerId: string;
  runRecoveryOperation<T>(operation: () => Promise<T>): Promise<T>;
  flushRecoveryWrites(): Promise<void>;
  tryAcquireRecoveryOperation(): (() => void) | null;
  tryAcquireBuiltinOperation(): (() => void) | null;
  activateRecovery(record: MutationRecoveryRecord): Promise<boolean>;
  queueRecoveryPersistence(
    id: string,
    text: string,
    attachments: InputAttachment[],
    names: readonly string[],
    commands?: readonly string[],
    mentions?: readonly ComposerMention[],
    composerText?: string,
    adoptedImageMarkers?: readonly number[],
    adoptedImageRanges?: readonly ImageMarkerRange[],
  ): Promise<boolean>;
  clearIfUnchanged(
    text: string,
    revision: number,
    draftRevision: number,
    names: readonly string[],
    commands?: readonly string[],
  ): boolean;
  clearSubmittedAttachments(items: PendingAttachment[]): void;
  dispose(): void;
}

export function restoredSkillNames(value: SkillEditorValue): string[] {
  return serializeSkillDocument(parseSkillDocument(value)).skillNames;
}

/** The anchors a persisted record says its writer staged whose bytes cannot
 * have travelled with it. A marker is staged with a revision only when a
 * staging gesture wrote it AND appended the tile holding its bytes; typed or
 * pasted marker text never enters that list, and a record from an older build
 * carries none at all. So this is the one place a reader can tell an anchor
 * whose image is missing from literal prose that merely looks like one - at
 * BOTH entry points, since which writer produced the record does not change
 * the answer once the bytes are gone.
 *
 * The exact ranges win when the record carries them: they say WHICH spelling
 * of a number the writer staged, which is what keeps a literal beside an
 * adopted "[image 1]" sendable after a reload. A record from before ranges
 * existed carries only numbers, and every occurrence of one is adopted - the
 * reading those builds implemented. */
function adoptedAnchorsIn(draft: {
  text: string;
  stagedImageMarkers?: number[];
  stagedImageRanges?: ImageMarkerRange[];
}): { markers: number[]; ranges: ImageMarkerRange[] } {
  const ranges = adoptedMarkerRanges(draft.text, draft.stagedImageMarkers, draft.stagedImageRanges);
  return { markers: markerNumbersOf(ranges), ranges };
}

function recoveryCompositionRevision(record: MutationRecoveryRecord): number {
  const revision = (record as MutationRecoveryRecord & { compositionRevision?: unknown }).compositionRevision;
  return typeof revision === "number" && Number.isSafeInteger(revision) && revision >= 0 ? revision : 0;
}

/** Original conversation work belongs to its pane, not to an editor mount. */
const defaultCompositionOwners = new Map<string, string>();

/** The per-ref default composition owner: the owner a composer source minted
 * without an explicit owner carries. Seeded recovery rows use it so a test's
 * default-seeded row belongs to the same seat a default mount restores, the
 * way a real pane's row belongs to that pane. */
export function defaultCompositionOwner(ref: string): string {
  let owner = defaultCompositionOwners.get(ref);
  if (owner === undefined) {
    owner = createSecureUUID();
    defaultCompositionOwners.set(ref, owner);
  }
  return owner;
}

/** A recovery whose submission has started is no longer available to an Edit
 * press from a remounted composer. The submission already owns the captured
 * composition; restoring it through a fresh source would write a new revision
 * and race the send. */
const recoverySubmissionsInFlight = new Set<string>();

export function beginRecoverySubmission(clientMutationId: string): () => void {
  recoverySubmissionsInFlight.add(clientMutationId);
  return () => {
    recoverySubmissionsInFlight.delete(clientMutationId);
  };
}

export function recoverySubmissionInFlight(clientMutationId: string): boolean {
  return recoverySubmissionsInFlight.has(clientMutationId);
}

/** A pane passes its lifetime token; standalone mounts fall back to one token
 * per ref so a remount outside the pane shell still reuses ownership. */
export function createComposerSourceState(ref: string, ownerId?: string): ComposerSourceState {
  const compositionOwnerId = ownerId ?? defaultCompositionOwner(ref);
  const initial = readComposerDraftSnapshot(ref);
  let shareId = initial.shareId;
  let pendingShareId: string | undefined;
  const initialAdoption = adoptedAnchorsIn(initial);
  let currentDraftRevisionId = initial.revisionId;
  const state = createStore<ComposerSourceSnapshot>(() => ({
    text: initial.text,
    skillNames: restoredSkillNames(initial),
    commandNames: initial.commandNames ?? [],
    mentions: initial.mentions,
    // A fresh source holds no bytes: attachments are per-mount. So the anchors
    // this record's writer staged are exactly the anchors whose image is gone
    // here, whether this ref mounted for the first time or was remounted after
    // a reload. The record is the provenance; a marker it does not name is
    // literal prose and stays sendable.
    adoptedImageMarkers: initialAdoption.markers,
    adoptedImageRanges: initialAdoption.ranges,
    // A fresh source holds no tiles, so nothing it writes is backed here. The
    // ranges a record carries describe ITS writer's staging, which is exactly
    // what the adoption record above already says: those bytes are gone.
    stagedImageRanges: [],
    activeRecoveryId: null,
    recoveryActivating: false,
    recoveryOperationBusy: false,
    builtinOperationBusy: false,
    freshRecoveryRef: null,
    restoreEpoch: 0,
  }));
  const continuity: ComposerSourceState["continuity"] = {
    textRef: { current: initial.text },
    skillNamesRef: { current: state.getState().skillNames },
    selectionMetaRef: { current: { commandNames: initial.commandNames ?? [], mentions: initial.mentions } },
    draftEditRevisionRef: { current: 0 },
    ownedDraftRevisionIdRef: { current: initial.revisionId },
    ownedDraftRevisionRef: { current: readDraftRevision(ref) },
    recoveryOwnerLeaseRef: { current: null },
    recoveryWriteVersionRef: { current: 0 },
    recoveryReplacementEpochRef: { current: 0 },
    recoveryOwnsLocalDraftRef: { current: false },
  };
  const {
    textRef,
    skillNamesRef,
    selectionMetaRef,
    draftEditRevisionRef,
    ownedDraftRevisionIdRef,
    ownedDraftRevisionRef,
    recoveryOwnerLeaseRef,
    recoveryWriteVersionRef,
    recoveryReplacementEpochRef,
    recoveryOwnsLocalDraftRef,
  } = continuity;
  const attachments = createAttachmentStore();
  // Numbers this composer's adoption record still names are unavailable to a
  // new staging gesture: the marker is spelled in prose this draft carries, so
  // reusing it would pair a fresh tile's bytes with an occurrence the person
  // never staged. Kept in the attachment store because that is where
  // allocation reads it.
  function publishReservedMarkers(markers: readonly number[]): void {
    attachments.getState().reservedMarkersRef.current = [...markers];
  }
  publishReservedMarkers(state.getState().adoptedImageMarkers);
  let disposed = false;
  let boundEditor: TextEditor | null = null;
  // Every marker this source ever recorded a staged RANGE for. A tile whose
  // number is in here has positional provenance, so the absence of a surviving
  // range means its anchor was deleted - not that this source never knew where
  // it was. A tile that never recorded one (a recovery hydration, which
  // restores settled attachments without a gesture to describe) keeps the
  // legacy number-only reading, because for it a number really is all the
  // provenance there is.
  const positionedStagedMarkers = new Set<number>();
  let recoveryWrites: Promise<void> = Promise.resolve();
  let recoveryOperations: Promise<void> = Promise.resolve();
  let recoveryOperationHeld = false;
  const currentRecoveryId = () => recoveryOwnerLeaseRef.current?.clientMutationId ?? null;
  // The last durable shape this pane owned, so a refused write can preserve
  // the local composition under a fresh owner with the same method and target.
  let recoveryBaseRecord: MutationRecoveryRecord | null = null;
  // The durable destination of the row this source owns, if any. Reconciled
  // by every write that observes the row's current targetRef, so a peer's
  // retarget followed by one successful autosave of ours is enough to move it.
  let recoveryTargetRef = ref;
  let committedRecoveryComposition: string | null = null;
  let refreshVersion = 0;
  let persistenceScheduled = false;
  let cleanupRetryTimer: ReturnType<typeof setTimeout> | null = null;
  let cleanupRetryAttempt = 0;
  let cleanupRetryVersion = 0;

  function cancelCleanupRetry(): void {
    cleanupRetryVersion += 1;
    if (cleanupRetryTimer !== null) clearTimeout(cleanupRetryTimer);
    cleanupRetryTimer = null;
    cleanupRetryAttempt = 0;
  }

  function hasEmptyComposition(): boolean {
    return compositionIsEmpty({
      text: textRef.current,
      attachments: operations.items,
      skillNames: skillNamesRef.current,
      commandNames: selectionMetaRef.current.commandNames,
      mentions: selectionMetaRef.current.mentions,
    });
  }

  function scheduleCleanupRetry(clientMutationId: string, replacementEpoch: number): void {
    if (
      disposed ||
      currentRecoveryId() !== clientMutationId ||
      recoveryReplacementEpochRef.current !== replacementEpoch ||
      !hasEmptyComposition()
    )
      return;
    if (cleanupRetryTimer !== null) return;
    cleanupRetryAttempt += 1;
    const delay = Math.min(250 * 2 ** (cleanupRetryAttempt - 1), 5_000);
    const retryVersion = cleanupRetryVersion;
    cleanupRetryTimer = setTimeout(() => {
      cleanupRetryTimer = null;
      if (
        retryVersion !== cleanupRetryVersion ||
        disposed ||
        currentRecoveryId() !== clientMutationId ||
        recoveryReplacementEpochRef.current !== replacementEpoch ||
        !hasEmptyComposition()
      )
        return;
      void queueRecoveryPersistence(clientMutationId, "", [], []);
    }, delay);
  }

  /** Claim the draft record currently persisted under `ref` (or the absence of
   * one). Both ownership signals are snapshotted here; a caller that forgets one
   * would silently disable one of the two checks. */
  function claimDraftRevision(): void {
    ownedDraftRevisionIdRef.current = readComposerDraftRevisionId(ref);
    ownedDraftRevisionRef.current = readDraftRevision(ref);
  }
  function ownsDraftRevision(): boolean {
    // Undefined on both sides means "no record has been written yet", which the
    // source that is about to write the first one does own. Undefined on only
    // one side means another writer produced or removed the record.
    return (
      ownedDraftRevisionIdRef.current === readComposerDraftRevisionId(ref) &&
      // Both are required. The id sees a peer tab's write, which the counter
      // cannot; the counter sees a legacy v1 write, which has no id to compare.
      ownedDraftRevisionRef.current === readDraftRevision(ref)
    );
  }
  function reconcileRecoveryTarget(targetRef: string): void {
    recoveryTargetRef = targetRef;
  }
  function currentDraftText(): string {
    return ownsDraftRevision() ? textRef.current : readComposerDraft(ref).text;
  }
  function updateText(text: string): void {
    if (disposed) return;
    const blank = text.trim() === "";
    if (!blank) cancelCleanupRetry();
    textRef.current = text;
    state.setState({ text });
    // Nothing about the adopted set is decided here. Provenance lives in the
    // editor document as the stagedAnchor mark, so the editor's own history
    // carries it: deleting the only occurrence and pressing Undo restores the
    // mark and the refusal with it. Only replacing the draft outright
    // (clearIfUnchanged, replaceComposerWithGoalDraft) retires the record,
    // because there is then no document left to undo into.
  }
  function updateSkillNames(skillNames: string[]): void {
    if (disposed) return;
    if (skillNames.length > 0) cancelCleanupRetry();
    skillNamesRef.current = skillNames;
    state.setState({ skillNames });
  }
  function updateSelectionMeta(value: SkillEditorValue): void {
    if (disposed) return;
    const next = { commandNames: value.commandNames ?? [], mentions: value.mentions };
    if (next.commandNames.length > 0) cancelCleanupRetry();
    selectionMetaRef.current = next;
    state.setState(next);
  }
  function markEdited(): void {
    draftEditRevisionRef.current += 1;
    if (currentRecoveryId() !== null) {
      markDraftEdited(ref);
      claimDraftRevision();
    }
  }
  function editText(text: string): void {
    if (disposed) return;
    cancelCleanupRetry();
    markEdited();
    updateText(text);
  }
  function editSkillNames(names: string[]): void {
    if (disposed) return;
    cancelCleanupRetry();
    markEdited();
    updateSkillNames(names);
  }
  function setActiveRecoveryId(record: MutationRecoveryRecord | null): void {
    if (disposed) return;
    const activeRecoveryId = record?.clientMutationId ?? null;
    if (currentRecoveryId() !== activeRecoveryId) {
      cancelCleanupRetry();
      committedRecoveryComposition = null;
    }
    recoveryOwnerLeaseRef.current = record
      ? { clientMutationId: record.clientMutationId, compositionRevision: recoveryCompositionRevision(record) }
      : null;
    recoveryBaseRecord = record;
    reconcileRecoveryTarget(record?.targetRef ?? ref);
    state.setState({ activeRecoveryId });
  }
  function persistDraft(text: string): void {
    if (disposed) return;
    // Which of this text's markers were STAGED by a writer that held bytes.
    // Two sources, both by EXACT occurrence rather than by number:
    // - the occurrences this source staged itself whose tiles still back them
    //   (an occurrence a removal took away is gone from this list, and one
    //   whose tile is gone is not provenance any more even if the literal
    //   survives in the text);
    // - the occurrences an adoption carries forward (a reader that adopted a
    //   writer's anchors keeps that provenance when it edits the prose around
    //   them, so an undo or a reload still knows they were staged).
    // Filtered to what this text spells, so a marker the person has removed or
    // retyped never travels as provenance. Typed markers are in neither
    // source, which is why literal prose stays sendable.
    const adopted = state.getState().adoptedImageMarkers;
    const adoptedRanges = state.getState().adoptedImageRanges;
    const ownStagedRanges = state.getState().stagedImageRanges;
    // This composer's own tiles back the occurrences of their numbers, so a
    // range is only a fact about an ADOPTED occurrence when no tile holds
    // those bytes.
    const held = new Set(operations.items.map((item) => item.marker));
    // Nothing staged lies behind this text, so no provenance can exist: skip
    // spelling the text to filter an empty set.
    let staged: number[] = [];
    let stagedRanges: ImageMarkerRange[] = [];
    if (held.size > 0 || adopted.length > 0) {
      // The exact occurrences to persist. The record has to name WHICH spelling
      // was staged: with a literal "[image 1]" and a staged one, a number alone
      // makes a reloading reader mark both, and one repair press then deletes
      // the person's prose. Both sides are reconciled positions now, so each
      // names the occurrence its own gesture wrote or adopted.
      const adoptedValid = validMarkerRanges(text, adoptedRanges);
      stagedRanges = validMarkerRanges(text, [
        ...adoptedValid.filter((range) => !held.has(range.marker)),
        ...validMarkerRanges(
          text,
          ownStagedRanges.filter((range) => held.has(range.marker)),
        ),
      ]);
      // A marker the text does not spell is not in the draft: the person
      // removed it, or a removal's own write took it away, so it has no
      // provenance to travel with.
      const spelled = new Set(spelledImageMarkers(text));
      const located = new Set(stagedRanges.map((range) => range.marker));
      // Only a number no range locates falls back to the number-wide reading:
      // a writer that never recorded a position knew nothing but the number,
      // while a vanished range means the person deleted that anchor.
      const adoptedInText = new Set(markerNumbersOf(adoptedValid));
      const unpositioned = [...held].filter((marker) => !positionedStagedMarkers.has(marker));
      const unlocated = [...unpositioned, ...adopted].filter(
        (marker) => spelled.has(marker) && !located.has(marker) && !adoptedInText.has(marker),
      );
      staged = [...new Set([...located, ...unlocated])].sort((left, right) => left - right);
    }
    currentDraftRevisionId = writeComposerDraft(ref, {
      text,
      ...(shareId && { shareId }),
      skillNames: skillNamesRef.current,
      ...(selectionMetaRef.current.commandNames.length ? { commandNames: selectionMetaRef.current.commandNames } : {}),
      ...(selectionMetaRef.current.mentions ? { mentions: selectionMetaRef.current.mentions } : {}),
      ...(staged.length > 0 ? { stagedImageMarkers: staged } : {}),
      ...(stagedRanges.length > 0 ? { stagedImageRanges: stagedRanges } : {}),
    });
    // writeComposerDraft minted this revision, so this source owns it - even
    // when the write removed the record outright (undefined).
    ownedDraftRevisionIdRef.current = currentDraftRevisionId;
    ownedDraftRevisionRef.current = readDraftRevision(ref);
  }
  function setAdoptedImageRanges(ranges: readonly ImageMarkerRange[], markers?: readonly number[]): void {
    if (disposed) return;
    const exact = ranges.map((range) => ({ ...range })).sort((left, right) => left.start - right.start);
    const next = [...new Set(markers ?? markerNumbersOf(exact))].sort((left, right) => left - right);
    const currentMarkers = state.getState().adoptedImageMarkers;
    const currentRanges = state.getState().adoptedImageRanges;
    const sameMarkers =
      next.length === currentMarkers.length && next.every((marker, index) => marker === currentMarkers[index]);
    if (sameMarkers && sameImageRanges(exact, currentRanges)) return;
    state.setState({ adoptedImageMarkers: next, adoptedImageRanges: exact });
    publishReservedMarkers([...new Set([...attachments.getState().reservedMarkersRef.current, ...next])]);
  }
  function clearAdoptedImageMarkers(): void {
    if (disposed) return;
    state.setState({ adoptedImageMarkers: [], adoptedImageRanges: [] });
    publishReservedMarkers([]);
  }
  function setStagedImageRanges(ranges: readonly ImageMarkerRange[]): void {
    if (disposed) return;
    const exact = ranges.map((range) => ({ ...range })).sort((left, right) => left.start - right.start);
    for (const range of exact) positionedStagedMarkers.add(range.marker);
    if (sameImageRanges(exact, state.getState().stagedImageRanges)) return;
    state.setState({ stagedImageRanges: exact });
  }
  /** The composition this source held has been submitted or replaced, so every
   * part of it empties together: text, skill selections, command and mention
   * metadata, and the staged occurrences. The occurrences go because the text
   * they described is gone, so none of them is a position any more. Left
   * behind, they would be validated away on the next save - but only after a
   * pass that could hand a stale offset to an editor that has since grown its
   * text back. */
  function clearComposition(): void {
    updateText("");
    updateSkillNames([]);
    updateSelectionMeta({ text: "", skillNames: [] });
    setStagedImageRanges([]);
    // Tile numbering restarts at 1, so a remembered position would outlive the
    // tile it described.
    positionedStagedMarkers.clear();
  }
  function markRestore(): void {
    if (!disposed) state.setState((snapshot) => ({ restoreEpoch: snapshot.restoreEpoch + 1 }));
  }
  function writeDraft(
    text: string,
    mayPersist: boolean,
    source?: TextEditSource,
    splice?: TextSplice,
    restoredMentions?: readonly ComposerMention[],
  ): void {
    if (disposed) return;
    const next = patchSelectionText(
      // The adoption travels with the patch: patchSelectionText re-derives the
      // marks a marker keeps from this list, so omitting it would silently
      // retire every OTHER missing reference's provenance on one repair write.
      {
        text: textRef.current,
        skillNames: skillNamesRef.current,
        ...selectionMetaRef.current,
        adoptedImageMarkers: state.getState().adoptedImageMarkers,
        adoptedImageRanges: state.getState().adoptedImageRanges,
        stagedImageRanges: state.getState().stagedImageRanges,
      },
      text,
      splice,
      source === "cleanup" ? undefined : stagedImageRangesAddedBy(text, splice),
      restoredMentions,
    );
    updateSkillNames(next.skillNames);
    updateSelectionMeta(next);
    // This write patched the document from the adoption it started with, so
    // what it returns is exactly which anchors it kept. Adopting that back is
    // what keeps the record agreeing with the marks the editor is about to
    // apply - and what stops a stale list from making every later value look
    // like a non-echo rebuild.
    setAdoptedImageRanges(next.adoptedImageRanges ?? [], next.adoptedImageMarkers ?? []);
    // The staged occurrences move by the same rule, and any the splice itself
    // inserted arrive already naming their span.
    setStagedImageRanges(next.stagedImageRanges ?? []);
    if (source === "submission" || (restoredMentions !== undefined && restoredMentions.length > 0)) {
      updateText(text);
      // React can batch exact marker removals or restored mentions, so sync their mapped atoms.
      markRestore();
    } else editText(text);
    if (mayPersist && currentRecoveryId() === null) persistDraft(text);
  }
  function writeDetachedSourceDraft(
    text: string,
    _cursor: number,
    source?: TextEditSource,
    splice?: TextSplice,
    restoredMentions?: readonly ComposerMention[],
  ): void {
    // A detached continuation cannot claim another pane's newer draft.
    if (disposed || !ownsDraftRevision()) return;
    writeDraft(text, true, source, splice, restoredMentions);
  }
  const editor: TextEditor = {
    read: () => {
      const mounted = boundEditor?.read();
      if (mounted) return mounted;
      const text = currentDraftText();
      return { text, cursor: text.length, selection: { start: text.length, end: text.length } };
    },
    write: (text, cursor, source, splice, restoredMentions) => {
      if (disposed) return;
      if (boundEditor) boundEditor.write(text, cursor, source, splice, restoredMentions);
      else writeDetachedSourceDraft(text, cursor, source, splice, restoredMentions);
    },
    captureMentions: (start, end) =>
      extractMentionsInRange(
        {
          text: textRef.current,
          skillNames: skillNamesRef.current,
          ...selectionMetaRef.current,
        },
        start,
        end,
      ),
    stagedRangeFor: (marker) => ({
      range: state.getState().stagedImageRanges.find((item) => item.marker === marker),
      positioned: positionedStagedMarkers.has(marker),
    }),
  };
  const operations = createAttachmentOperations(editor, attachments);

  function queueRecoveryPersistence(
    clientMutationId: string,
    nextText: string,
    nextAttachments: InputAttachment[],
    nextSkillNames: readonly string[],
    nextCommandNames: readonly string[] = selectionMetaRef.current.commandNames,
    nextMentions: readonly ComposerMention[] | undefined = selectionMetaRef.current.mentions,
    nextComposerText?: string,
    nextAdoptedImageMarkers: readonly number[] = state.getState().adoptedImageMarkers,
    nextAdoptedImageRanges: readonly ImageMarkerRange[] = state.getState().adoptedImageRanges,
  ): Promise<boolean> {
    if (disposed) return Promise.resolve(false);
    const isEmptyCleanup = compositionIsEmpty({
      text: nextText,
      attachments: nextAttachments,
      skillNames: nextSkillNames,
      commandNames: nextCommandNames,
      mentions: nextMentions,
    });
    if (!isEmptyCleanup) cancelCleanupRetry();
    const version = ++recoveryWriteVersionRef.current;
    const replacementEpoch = recoveryReplacementEpochRef.current;
    const draftRevision = readDraftRevision(ref);
    const persistedRevisionId = readComposerDraftRevisionId(ref);
    const localDraftRevisionId =
      ownsDraftRevision() && currentDraftRevisionId !== undefined && currentDraftRevisionId === persistedRevisionId
        ? currentDraftRevisionId
        : undefined;
    const operation = recoveryWrites
      .catch(() => undefined)
      .then(async () => {
        if (
          disposed ||
          currentRecoveryId() !== clientMutationId ||
          recoveryReplacementEpochRef.current !== replacementEpoch
        )
          return true;
        if (
          compositionIsEmpty({
            text: nextText,
            attachments: nextAttachments,
            skillNames: nextSkillNames,
            commandNames: nextCommandNames,
            mentions: nextMentions,
          })
        ) {
          const lease = recoveryOwnerLeaseRef.current;
          if (!lease || lease.clientMutationId !== clientMutationId) return false;
          const discarded = await discardRecoveryPendingTurn(
            clientMutationId,
            ref,
            () =>
              !disposed &&
              recoveryReplacementEpochRef.current === replacementEpoch &&
              currentRecoveryId() === clientMutationId &&
              hasEmptyComposition() &&
              (localDraftRevisionId === undefined || readComposerDraftRevisionId(ref) === localDraftRevisionId),
            lease.compositionRevision,
          );
          if (!discarded) return false;
          cancelCleanupRetry();
          if (
            !disposed &&
            recoveryReplacementEpochRef.current === replacementEpoch &&
            currentRecoveryId() === clientMutationId &&
            readDraftRevision(ref) === draftRevision &&
            (localDraftRevisionId === undefined || readComposerDraftRevisionId(ref) === localDraftRevisionId) &&
            hasEmptyComposition()
          ) {
            recoveryOwnsLocalDraftRef.current = false;
            setActiveRecoveryId(null);
            if (localDraftRevisionId) clearPersistedDraftIfRevisionMatches(ref, localDraftRevisionId);
          }
          return true;
        }
        const lease = recoveryOwnerLeaseRef.current;
        if (!lease || lease.clientMutationId !== clientMutationId) return false;
        const updated = await updateRecoveryPendingTurn(
          clientMutationId,
          ref,
          nextText,
          nextAttachments,
          nextSkillNames,
          nextCommandNames,
          nextMentions,
          [],
          localDraftRevisionId,
          lease.compositionRevision,
          { [clientMutationId]: lease.compositionRevision },
          compositionOwnerId,
          nextComposerText,
          // The row's provenance travels with its text: an autosave that kept
          // the "[image N]" anchor but dropped this fact is exactly what let a
          // reload send an adopted anchor as prose.
          nextAdoptedImageMarkers,
          // ...and by exact position, so a reload marks the adopted spelling
          // and not an identical literal beside it.
          nextAdoptedImageRanges,
        );
        if (!updated) {
          if (!disposed && currentRecoveryId() === clientMutationId) {
            // The row was consumed or advanced by another pane. Keeping this
            // stale lease would make every later save and send fail against an
            // owner this tab can no longer write. A draft carries no image
            // bytes, so preserve the complete local composition under a fresh
            // recovery owner before retiring the lease.
            const preserved = await preserveStaleRecovery(clientMutationId, localDraftRevisionId);
            if (
              !disposed &&
              currentRecoveryId() === clientMutationId &&
              recoveryReplacementEpochRef.current === replacementEpoch
            ) {
              if (preserved) {
                // Own the returned row, including the identical-composition
                // fast path and its current revision: later edits update it,
                // Send consumes it, and a reload restores its attachment
                // bytes.
                setActiveRecoveryId(preserved);
              } else {
                // Preservation failed, so nothing durable carries the images.
                // Fall back to the ordinary draft and retire the lease.
                persistDraft(textRef.current);
                setActiveRecoveryId(null);
              }
            }
          }
          if (!disposed) {
            pushRecoveryRefusal("changed");
          }
          return false;
        }
        // A goal replacement (or another owner) retires this lease while the
        // write is in flight; do not resurrect it in the local mirror.
        if (
          !disposed &&
          currentRecoveryId() === clientMutationId &&
          recoveryReplacementEpochRef.current === replacementEpoch
        ) {
          recoveryOwnerLeaseRef.current = {
            clientMutationId,
            compositionRevision: recoveryCompositionRevision(updated),
          };
          recoveryBaseRecord = updated;
          // The write returned the row's current durable target: a peer may
          // have retargeted the row without changing its composition revision.
          reconcileRecoveryTarget(updated.targetRef);
          committedRecoveryComposition = recoveryCompositionFingerprint({
            text: nextText,
            attachments: nextAttachments,
            skillNames: nextSkillNames,
            commandNames: nextCommandNames,
            mentions: nextMentions,
          });
        }
        if (
          !disposed &&
          recoveryOwnsLocalDraftRef.current &&
          recoveryWriteVersionRef.current === version &&
          readDraftRevision(ref) === draftRevision &&
          (localDraftRevisionId === undefined || readComposerDraftRevisionId(ref) === localDraftRevisionId) &&
          currentRecoveryId() === clientMutationId
        ) {
          recoveryOwnsLocalDraftRef.current = false;
          if (localDraftRevisionId) clearPersistedDraftIfRevisionMatches(ref, localDraftRevisionId);
        }
        return true;
      });
    recoveryWrites = operation.then(
      () => undefined,
      () => undefined,
    );
    void operation.catch((error) => {
      if (disposed || currentRecoveryId() !== clientMutationId) return;
      if (isEmptyCleanup) scheduleCleanupRetry(clientMutationId, replacementEpoch);
      pushToast("error", `Couldn't save recovered message: ${error instanceof Error ? error.message : String(error)}`);
    });
    return operation;
  }

  async function preserveStaleRecovery(
    clientMutationId: string,
    localDraftRevisionId?: string,
  ): Promise<MutationRecoveryRecord | undefined> {
    const base = recoveryBaseRecord;
    if (!base || base.clientMutationId !== clientMutationId) return undefined;
    try {
      return await preserveRecoveryPendingTurn(
        base,
        ref,
        textRef.current,
        operations.toInputAttachments(),
        skillNamesRef.current,
        selectionMetaRef.current.commandNames,
        selectionMetaRef.current.mentions,
        compositionOwnerId,
        localDraftRevisionId,
        state.getState().adoptedImageMarkers,
        state.getState().adoptedImageRanges,
      );
    } catch (error) {
      if (!disposed)
        pushToast(
          "error",
          `Couldn't preserve recovered message: ${error instanceof Error ? error.message : String(error)}`,
        );
      return undefined;
    }
  }

  function activateRecovery(record: MutationRecoveryRecord): Promise<boolean> {
    if (disposed || state.getState().recoveryActivating || recoverySubmissionsInFlight.has(record.clientMutationId))
      return Promise.resolve(false);
    state.setState({ recoveryActivating: true });
    const completed = runRecoveryOperation(() => {
      const operation = recoveryWrites
        .catch(() => undefined)
        .then(async () => {
          // The record's durable destination may be a resumed ref while the
          // composer in front of the user is the displayed one the row was
          // retargeted from - the strip surfaces such rows under the
          // displayed ref, so the displayed composer must be able to open
          // its associated record. The row's own targetRef still governs
          // every durable operation; this gate is association only.
          const associatedHere = record.targetRef === ref || record.displayedRef === ref;
          if (disposed || operations.hasPending || !associatedHere) return false;
          if (currentRecoveryId() === record.clientMutationId) return false;
          // Keep this object: the recovery wrapper carries older-record image
          // reference provenance by identity, not in enumerable properties.
          const recovered = recoveryComposerDraft(record);
          if (record.recoveryKind === "editing") Object.assign(recovered, materializeSelectionReferences(recovered));
          const incorporatedRevisionId = record.localDraftRevisionId;
          const includedDraftRevisionId =
            currentDraftRevisionId !== undefined && currentDraftRevisionId === readComposerDraftRevisionId(ref)
              ? currentDraftRevisionId
              : undefined;
          let incorporatedSnapshotCleared = false;
          // A different source's stored draft does not change this loaded snapshot.
          // Only cleanup below needs to match the current persisted revision.
          const incorporatedSnapshotIsCurrent = () =>
            incorporatedRevisionId !== undefined &&
            (incorporatedSnapshotCleared ||
              (incorporatedRevisionId === currentDraftRevisionId && draftEditRevisionRef.current === 0));
          const merge = () => {
            const skipLocalDraft = incorporatedSnapshotIsCurrent();
            return mergeRecoveryComposerDraft(
              skipLocalDraft ? "" : textRef.current,
              operations.items,
              recovered,
              skipLocalDraft ? [] : skillNamesRef.current,
              skipLocalDraft ? [] : selectionMetaRef.current.commandNames,
              skipLocalDraft ? undefined : selectionMetaRef.current.mentions,
              skipLocalDraft ? undefined : state.getState().adoptedImageMarkers,
              skipLocalDraft ? undefined : state.getState().adoptedImageRanges,
            );
          };
          const merged = merge();
          const ownerId = currentRecoveryId() ?? record.clientMutationId;
          const ownerLease = recoveryOwnerLeaseRef.current;
          const ownerRecord = ownerId === record.clientMutationId ? record : undefined;
          const expectedCompositionRevision = ownerRecord
            ? recoveryCompositionRevision(ownerRecord)
            : ownerLease?.compositionRevision;
          if (expectedCompositionRevision === undefined) return false;
          const replacementEpoch = recoveryReplacementEpochRef.current;
          cancelCleanupRetry();
          const updated = await updateRecoveryPendingTurn(
            ownerId,
            ref,
            merged.text,
            merged.attachments.flatMap((item) =>
              item.data === undefined
                ? []
                : [{ marker: item.marker, name: item.name, mediaType: item.mediaType, data: item.data }],
            ),
            restoredSkillNames(merged),
            merged.commandNames,
            merged.mentions,
            ownerId === record.clientMutationId ? [] : [record.clientMutationId],
            incorporatedSnapshotIsCurrent() ? incorporatedRevisionId : includedDraftRevisionId,
            expectedCompositionRevision,
            {
              [ownerId]: expectedCompositionRevision,
              ...(ownerId === record.clientMutationId
                ? {}
                : { [record.clientMutationId]: recoveryCompositionRevision(record) }),
            },
            compositionOwnerId,
            merged.text,
            // The merge's own provenance, so a row opened for editing keeps
            // refusing exactly the anchors this composer now refuses.
            merged.adoptedImageMarkers ?? [],
            merged.adoptedImageRanges ?? [],
          );
          if (!updated || disposed || recoveryReplacementEpochRef.current !== replacementEpoch) return false;
          recoveryOwnerLeaseRef.current = {
            clientMutationId: ownerId,
            compositionRevision: recoveryCompositionRevision(updated),
          };
          if (incorporatedSnapshotIsCurrent() && incorporatedRevisionId !== undefined) {
            incorporatedSnapshotCleared = clearPersistedDraftIfRevisionMatches(ref, incorporatedRevisionId);
          }
          // A commit may finish after new typing or attachment work. Append to
          // that newer draft rather than restoring the press-time snapshot.
          const latest = merge();
          recoveryReplacementEpochRef.current += 1;
          recoveryOwnsLocalDraftRef.current = true;
          // Adoption uses the transaction's committed record and revision.
          setActiveRecoveryId(updated);
          // Rehydrating an already-owned recovery draft is not a user edit.
          // Counting it as one makes a submission that began before the
          // remount look stale, so its commit observer cannot clear unchanged
          // content or distinguish a later replacement from the restore.
          updateText(latest.text);
          updateSkillNames(restoredSkillNames(latest));
          updateSelectionMeta(latest);
          // The merge keeps an adopted occurrence spelled at its own number
          // while moving the tile that number used to own, so the record has to
          // survive the merge intact for the send-time check to refuse it
          // (composerRecovery's adoptedImageMarkers).
          setAdoptedImageRanges(latest.adoptedImageRanges ?? [], latest.adoptedImageMarkers ?? []);
          operations.replaceWithSettled(latest.attachments);
          markRestore();
          committedRecoveryComposition = recoveryCompositionFingerprint({
            text: merged.text,
            attachments: merged.attachments.flatMap((item) =>
              item.data === undefined
                ? []
                : [{ marker: item.marker, name: item.name, mediaType: item.mediaType, data: item.data }],
            ),
            skillNames: restoredSkillNames(merged),
            commandNames: merged.commandNames ?? [],
            mentions: merged.mentions,
          });
          return true;
        });
      const activation = operation.finally(() => {
        if (!disposed) state.setState({ recoveryActivating: false });
      });
      recoveryWrites = activation.then(
        () => undefined,
        () => undefined,
      );
      return activation;
    });
    return completed;
  }

  function runRecoveryOperation<T>(operation: () => Promise<T>): Promise<T> {
    const result = recoveryOperations.catch(() => undefined).then(operation);
    recoveryOperations = result.then(
      () => undefined,
      () => undefined,
    );
    return result;
  }

  function tryAcquireRecoveryOperation(): (() => void) | null {
    if (disposed || recoveryOperationHeld) return null;
    recoveryOperationHeld = true;
    state.setState({ recoveryOperationBusy: true });
    let released = false;
    return () => {
      if (released) return;
      released = true;
      recoveryOperationHeld = false;
      if (!disposed) state.setState({ recoveryOperationBusy: false });
    };
  }

  // A command belongs to the pane source, not the presentation that started it.
  // Keep this separate from recovery operations, which Queue/Steer already own.
  function tryAcquireBuiltinOperation(): (() => void) | null {
    const snapshot = state.getState();
    if (disposed || snapshot.builtinOperationBusy || snapshot.recoveryActivating || snapshot.recoveryOperationBusy)
      return null;
    state.setState({ builtinOperationBusy: true });
    let released = false;
    return () => {
      if (released) return;
      released = true;
      if (!disposed) state.setState({ builtinOperationBusy: false });
    };
  }

  function settleRecoverySubmission(
    clientMutationId: string,
    draftUnchanged: boolean,
    submittedText: string,
    submittedSkillNames: readonly string[],
    submittedCommandNames: readonly string[],
    submittedAttachments: PendingAttachment[],
  ): void {
    if (disposed || currentRecoveryId() !== clientMutationId) return;
    // The submission's own write (or the unchanged draft it left) is the
    // persisted revision this source still owns.
    if (draftUnchanged) claimDraftRevision();
    // A submission whose draft it left unchanged just claimed that draft, so
    // the ownership it implies needs no second read of localStorage.
    const owns = draftUnchanged ? true : ownsDraftRevision();
    recoveryOwnsLocalDraftRef.current = false;
    recoveryWriteVersionRef.current += 1;
    cancelCleanupRetry();
    recoveryReplacementEpochRef.current += 1;
    setActiveRecoveryId(null);
    if (
      draftUnchanged &&
      textRef.current === submittedText &&
      sameStrings(skillNamesRef.current, submittedSkillNames) &&
      sameStrings(selectionMetaRef.current.commandNames, submittedCommandNames)
    ) {
      clearComposition();
    }
    const markers = new Set(
      operations.items
        .filter((item) =>
          submittedAttachments.some(
            (submitted) =>
              submitted.marker === item.marker &&
              submitted.data === item.data &&
              submitted.name === item.name &&
              submitted.mediaType === item.mediaType,
          ),
        )
        .map((item) => item.marker),
    );
    operations.clearSubmitted(markers);
    if (owns) persistDraft(textRef.current);
  }

  const unsubscribeCommitted = subscribeComposerSubmissionCommitted(
    (targetRef, submittedText, submittedSkillNames, recovery, submittedCommandNames) => {
      if (disposed || targetRef !== ref) return;
      if (recovery && currentRecoveryId() === recovery.clientMutationId) {
        settleRecoverySubmission(
          recovery.clientMutationId,
          recovery.draftUnchanged,
          submittedText,
          submittedSkillNames,
          submittedCommandNames ?? [],
          recovery.attachments as PendingAttachment[],
        );
      } else if (
        !recovery &&
        currentRecoveryId() === null &&
        textRef.current === submittedText &&
        sameStrings(skillNamesRef.current, submittedSkillNames) &&
        sameStrings(selectionMetaRef.current.commandNames, submittedCommandNames ?? [])
      ) {
        claimDraftRevision();
        clearComposition();
      }
    },
  );

  function persistDetachedRecovery(): void {
    if (disposed || boundEditor || persistenceScheduled || currentRecoveryId() === null) return;
    persistenceScheduled = true;
    queueMicrotask(() => {
      persistenceScheduled = false;
      const id = currentRecoveryId();
      const current = recoveryCompositionFingerprint({
        text: textRef.current,
        attachments: operations.toInputAttachments(),
        skillNames: skillNamesRef.current,
        commandNames: selectionMetaRef.current.commandNames,
        mentions: selectionMetaRef.current.mentions,
      });
      if (
        !disposed &&
        !boundEditor &&
        id !== null &&
        !recoverySubmissionsInFlight.has(id) &&
        !operations.hasPending &&
        current !== committedRecoveryComposition
      )
        void queueRecoveryPersistence(id, textRef.current, operations.toInputAttachments(), skillNamesRef.current);
    });
  }
  const unsubscribeAttachments = attachments.subscribe(() => {
    if (operations.items.length > 0) cancelCleanupRetry();
    persistDetachedRecovery();
  });
  const unsubscribeDraft = state.subscribe(persistDetachedRecovery);

  return {
    ref,
    get alive() {
      return !disposed;
    },
    get recoveryTargetRef() {
      return recoveryTargetRef;
    },
    attachments,
    editor,
    continuity,
    getSnapshot: state.getState,
    subscribe: state.subscribe,
    updateText,
    editText,
    updateSkillNames,
    editSkillNames,
    updateSelectionMeta,
    setActiveRecoveryId,
    setAdoptedImageRanges,
    setStagedImageRanges,
    clearAdoptedImageMarkers,
    ownsDraftRevision,
    claimDraftRevision,
    markRestore,
    persistDraft,
    placeShare(id, text) {
      if (disposed || currentRecoveryId() !== null) return false;
      if (shareId !== id) {
        shareId = id;
        writeDraft(mergeDraftText(textRef.current, text), true);
        markRestore();
      } else if (readComposerDraftSnapshot(ref).shareId !== id) {
        persistDraft(textRef.current);
      }
      const placed = readComposerDraftSnapshot(ref).shareId === id;
      pendingShareId = placed ? undefined : id;
      return placed;
    },
    get pendingShareId() {
      return pendingShareId;
    },
    clearPendingShare(id) {
      if (pendingShareId !== id) return false;
      pendingShareId = undefined;
      return true;
    },
    writeDraft,
    compositionOwnerId,
    runRecoveryOperation,
    flushRecoveryWrites: () => recoveryWrites.catch(() => undefined),
    tryAcquireRecoveryOperation,
    tryAcquireBuiltinOperation,
    queueRecoveryPersistence,
    activateRecovery,
    bindEditor(nextEditor) {
      if (disposed) return () => {};
      if (!ownsDraftRevision() && currentRecoveryId() === null) {
        const draft = readComposerDraftSnapshot(ref);
        updateText(draft.text);
        updateSkillNames(restoredSkillNames(draft));
        updateSelectionMeta(draft);
        currentDraftRevisionId = draft.revisionId;
        claimDraftRevision();
        // The revision this source last held is gone, so another writer
        // produced this text. Which of its markers that writer STAGED is the
        // record's own provenance - not every marker the text happens to
        // spell, which would refuse prose a person typed.
        const adoption = adoptedAnchorsIn(draft);
        setAdoptedImageRanges(adoption.ranges, adoption.markers);
        // This composer's retained tiles outlive the draft another pane
        // replaced, so a tile's number can name an occurrence in the incoming
        // text that is ordinary prose there. Moving the tile off that number
        // (the text is left exactly as it arrived) is what stops Send pairing
        // the literal with this tile's older bytes. Runs after the adoption so
        // the move already sees the numbers that adoption reserves.
        operations.renumberOff(draft.text);
      }
      markRestore();
      boundEditor = nextEditor;
      return () => {
        if (boundEditor === nextEditor) boundEditor = null;
      };
    },
    refreshRecovery() {
      if (disposed) return;
      const version = ++refreshVersion;
      state.setState({ freshRecoveryRef: null });
      void refreshPendingTurnsProjection(ref).then((refreshed) => {
        if (!disposed && refreshed && refreshVersion === version) state.setState({ freshRecoveryRef: ref });
      });
    },
    clearIfUnchanged(text, revision, draftRevision, names, commands = []) {
      if (disposed || draftEditRevisionRef.current !== revision) return false;
      if (
        textRef.current === text &&
        sameStrings(skillNamesRef.current, names) &&
        sameStrings(selectionMetaRef.current.commandNames, commands)
      ) {
        clearComposition();
        // Only a draft this source still owns may be cleared: another tab's
        // write since the submit owns the record now, and clearing it would
        // delete that tab's text. Both checks are required - the tab-local
        // counter catches our own later write, the persisted revision id
        // catches another tab's.
        if (readDraftRevision(ref) === draftRevision && ownsDraftRevision()) {
          clearDraft(ref);
          claimDraftRevision();
          // The draft this adoption described is gone, so its numbers are free
          // again - and must be, or a replacement image could never take one.
          // A cleared draft is a replacement, not an edit, so it retires the
          // reservations too: setAdoptedImageRanges alone only grows them.
          clearAdoptedImageMarkers();
        }
      }
      return true;
    },
    clearSubmittedAttachments(items) {
      if (disposed) return;
      const markers = new Set(operations.items.filter((item) => items.includes(item)).map((item) => item.marker));
      operations.clearSubmitted(markers);
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      cancelCleanupRetry();
      boundEditor = null;
      recoveryWriteVersionRef.current += 1;
      recoveryReplacementEpochRef.current += 1;
      unsubscribeCommitted();
      unsubscribeAttachments();
      unsubscribeDraft();
      operations.reset();
    },
  };
}
