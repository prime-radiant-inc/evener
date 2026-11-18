import { type ComposerMention, markerPattern, markerText } from "@evener/appwire-client";
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
  type CompositionImageAnchor,
  type CompositionImageRecord,
  clearDraft,
  clearPersistedDraftIfRevisionMatches,
  dedupeAnchors,
  markDraftEdited,
  readComposerDraft,
  readComposerDraftImageAnchors,
  readComposerDraftRevisionId,
  readComposerDraftSnapshot,
  readCompositionImageRecord,
  readDraftRevision,
  writeComposerDraft,
  writeCompositionImageRecord,
} from "./draft";
import {
  discardRecoveryPendingTurn,
  preserveRecoveryPendingTurn,
  refreshPendingTurnsProjection,
  subscribeComposerSubmissionCommitted,
  updateRecoveryPendingTurn,
} from "./queue/pendingTurnsStore";
import {
  compositionAnchorsForRecord,
  compositionIsEmpty,
  excludedRecoveryImages,
  mergeRecoveryComposerDraft,
  type RecoveredComposerDraft,
  recoveryComposerDraft,
  recoveryDeclaredMarkers,
  recoveryImageReferencesUnavailable,
  recoveryTileEligibility,
  rowCommittedAnchors,
} from "./recovery/recoveryDraft";
import {
  materializeSelectionReferences,
  parseSkillDocument,
  patchSelectionText,
  type SkillEditorValue,
  serializeSkillDocument,
  type TextChangeRange,
} from "./skillDocument";
import { pushRecoveryRefusal, sendableAttachments } from "./submitProtocol";

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
  /**
   * Marker numbers this source ADOPTED from a persisted draft without the
   * bytes their anchor stood for, and that the current text still spells
   * while no staged attachment carries that marker.
   *
   * The invariant this exists to hold: a marker and its bytes are committed
   * together. A draft round-trips through localStorage, which never holds
   * image bytes (see attachments/useAttachments.ts), so a reader that adopts
   * another reader's draft gets the anchor and no image. Sending that would
   * tell the model about an image it cannot see - the marker reaches the wire
   * as literal text with no image item. So the send path refuses while this
   * is non-empty, and the composer says why beside the draft. A marker that
   * was never adopted is never listed: literal "[image N]" prose the person
   * typed is theirs to send.
   */
  unbackedImageMarkers: number[];
  /**
   * Attachment ids this source was holding when it rebound to another draft.
   * Their bytes belong to the composition that was replaced, so they are not
   * sendable under the adopted text. Keyed by attachment identity, never by
   * marker: the number is a text reference that a merge renumbers, so a
   * keyed-by-number flag would move to whichever image later took that number.
   */
  foreignImageIds: string[];
  /**
   * Attachment ids a recovery row restored from its own payload whose text
   * does not spell their marker. The row carried real bytes and never recorded
   * that their reference was dropped, so they stay sendable: the text-spelling
   * rule exists to drop work whose reference is gone, not to second-guess a
   * row.
   */
  restoredImageIds: string[];
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
    ownedDraftRevisionRef: RevisionRef<number>;
    recoveryOwnerLeaseRef: RevisionRef<RecoveryOwnerLease | null>;
    recoveryWriteVersionRef: RevisionRef<number>;
    recoveryReplacementEpochRef: RevisionRef<number>;
    recoveryOwnsLocalDraftRef: RevisionRef<boolean>;
  };
  getSnapshot(): ComposerSourceSnapshot;
  subscribe(listener: () => void): () => void;
  bindEditor(editor: TextEditor): () => void;
  /** Record what a restored recovery composition declares about its images.
   * Both adoption paths call this - activation and the mount-time auto
   * restore - because each replaces the snapshot's provenance wholesale. */
  adoptRecoveredImageFacts(recovered: RecoveredComposerDraft, incomingRowId: string | null): void;
  updateText(text: string, changed?: TextChangeRange): void;
  editText(text: string, changed?: TextChangeRange): void;
  updateSkillNames(names: string[]): void;
  editSkillNames(names: string[]): void;
  updateSelectionMeta(value: SkillEditorValue): void;
  setActiveRecoveryId(record: MutationRecoveryRecord | null): void;
  markRestore(): void;
  persistDraft(text: string): void;
  writeDraft(text: string, mayPersist: boolean, source?: TextEditSource): void;
  refreshRecovery(): void;
  readonly compositionOwnerId: string;
  /** Whether this source's current composition owns a staged tile's reference.
   * False for a tile retained across a rebind: the adopted text's `[image N]`
   * belongs to the adopted draft's image, whatever number this tile bears. */
  readonly ownsImageReference: (item: { id: string; marker: number }) => boolean;
  /** The tiles this source's composition owns, in marker order. Ownership
   * filtered: a tile retained across a rebind is not in here, because the
   * adopted draft's `[image N]` is not that tile's reference. */
  readonly ownedAttachments: () => InputAttachment[];
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

export function sameSkillSelections(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((name, index) => name === right[index]);
}

export function restoredSkillNames(value: SkillEditorValue): string[] {
  return serializeSkillDocument(parseSkillDocument(value)).skillNames;
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
  let currentDraftRevisionId = initial.revisionId;
  const state = createStore<ComposerSourceSnapshot>(() => ({
    text: initial.text,
    skillNames: restoredSkillNames(initial),
    commandNames: initial.commandNames ?? [],
    mentions: initial.mentions,
    unbackedImageMarkers: [],
    foreignImageIds: [],
    restoredImageIds: [],
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
    ownedDraftRevisionRef,
    recoveryOwnerLeaseRef,
    recoveryWriteVersionRef,
    recoveryReplacementEpochRef,
    recoveryOwnsLocalDraftRef,
  } = continuity;
  const attachments = createAttachmentStore();
  let disposed = false;
  let boundEditor: TextEditor | null = null;
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

  // "Empty" means nothing the request would carry, which is the sendable
  // selection rather than the raw tile list. A tile whose reference the person
  // deleted is excluded from Send by the editor, so a draft holding only that
  // tile IS empty: counting the raw tile kept the discard guard from retiring
  // the durable row, and reopening it restored a marker the person had erased.
  // The excluded tile stays staged; only the durable composition retires.
  // Tiles this composition OWNS, by identity: everything staged here except a
  // tile retained from the composition a recovery row replaced. A foreign tile
  // stays in the pane by design and is never part of this row, so it must not
  // keep the row alive; a tile whose reference the text does not currently
  // spell is still the person's staged work.
  function ownedTiles(): PendingAttachment[] {
    const foreign = new Set(state.getState().foreignImageIds);
    return operations.items.filter((item) => !foreign.has(item.id));
  }
  // Whether the person has actually emptied this composition. That is the only
  // fact that licenses discarding a recovery row's durable copy.
  //
  // It is NOT "nothing is sendable right now". `sendableAttachments` drops any
  // tile the submitted text does not spell, so clearing the editor to retype it
  // passes through a moment where nothing is sendable while the tile is still
  // staged and visible. Discarding on that moment deletes the durable copy of
  // work still on screen, and the person's next keystroke writes a fresh row
  // that never held the images.
  function hasEmptyComposition(): boolean {
    return compositionIsEmpty({
      text: textRef.current,
      attachments: ownedTiles(),
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

  const ownsDraft = () => ownedDraftRevisionRef.current === readDraftRevision(ref);
  function reconcileRecoveryTarget(targetRef: string): void {
    recoveryTargetRef = targetRef;
  }
  function currentDraftText(): string {
    return ownsDraft() ? textRef.current : readComposerDraft(ref).text;
  }
  function updateText(text: string, changed?: TextChangeRange): void {
    if (disposed) return;
    if (text.trim() !== "") cancelCleanupRetry();
    // An anchor follows the text it was adopted with. Reconciling on the WRITE
    // rather than re-reading spellings afterwards is what keeps two identical
    // `[image 1]` occurrences distinct: only the one the person removed goes.
    reconcileAdoptedAnchors(textRef.current, text, changed);
    // The staged tiles' own references move by the same rule, and only while
    // their spelling survives where the splice put it. A reference the edit
    // destroyed is forgotten rather than relocated: an offset that no longer
    // spells its marker is not evidence about where this tile's bytes used to
    // be, and guessing "some other spelling" is the defect this record exists
    // to remove.
    //
    // MOVED FIRST, THEN SEEDED. A tile inserted by this very write already
    // carries its reference in the NEW text's coordinates, so it is recorded
    // after the mapping rather than through it - otherwise the same delta
    // would be applied to an offset that already includes it.
    if (stagedReferences.size > 0 && text !== textRef.current) {
      for (const [id, reference] of stagedReferences) {
        const [moved] = mapAnchorsThroughEdit([reference], textRef.current, text, changed);
        if (moved === undefined) stagedReferences.delete(id);
        else stagedReferences.set(id, moved);
      }
    }
    // A tile's `insertedAt` is where its own spelling began at the splice, and
    // the splice is the write this call is handling. Recording it here is what
    // makes "which `[image N]` do these bytes back" a fact about the tile
    // instead of a guess about the text.
    const liveTileIds = new Set(operations.items.map((item) => item.id));
    for (const id of stagedReferences.keys()) if (!liveTileIds.has(id)) stagedReferences.delete(id);
    for (const item of operations.items) {
      if (stagedReferences.has(item.id)) continue;
      if (item.insertedAt === undefined) continue;
      stagedReferences.set(item.id, { marker: item.marker, offset: item.insertedAt });
    }
    textRef.current = text;
    state.setState({ text });
    refreshUnbackedImageMarkers();
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
      ownedDraftRevisionRef.current = readDraftRevision(ref);
    }
  }
  function editText(text: string, changed?: TextChangeRange): void {
    if (disposed) return;
    cancelCleanupRetry();
    markEdited();
    updateText(text, changed);
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
  // The invariant at its write site: every anchor the text
  // names travels WITH the text, whether this source holds its bytes or
  // adopted it without them. localStorage never holds image bytes, so a
  // record without this list cannot say whether a later reader's "[image N]"
  // was an image anchor or literal prose, and the adopter would have to guess
  // either way - send the anchor with no image, or refuse a legitimate send.
  //
  // The union is what makes provenance survive a rewrite. Writing only the
  // store's markers would drop an adopted marker on the first edit after a
  // reload, and a second reload would then send it as literal text. The
  // adopted half is already filtered to markers the text still spells, so a
  // reference the person deleted leaves the record for good: an undo that
  // restores the text does not restore the tag, because text alone cannot tell
  // a restored anchor from retyped prose.
  // Provenance describes the TEXT BEING WRITTEN, so it is computed from that
  // text and the store at this instant, never from the store alone.
  //
  // Two writes are ordered text-first in practice - the cascade guard's failed
  // decode and submitted-marker cleanup both strip the reference before the
  // tile leaves the store - and reading the store alone would record a tile
  // whose reference is already gone. The next reader would then pair that
  // number with prose that no longer meant it.
  //
  // The staged half is the same sendable selection the request uses, so a tile
  // retained from the composition this draft replaced stays out of it, even
  // when the adopted text happens to spell its number: recording it would make
  // a reload read the peer's literal prose as an anchor and refuse a draft that
  // is complete. The unbacked half keeps a known missing image's tag only while
  // the text still spells it.
  function recordedMarkerAnchors(text: string): CompositionImageAnchor[] {
    const snapshot = state.getState();
    const stagedTiles = sendableAttachments(operations.items, text, {
      foreign: snapshot.foreignImageIds,
      restored: snapshot.restoredImageIds,
    });
    const staged = stagedTiles.map((item) => item.marker);
    // The live anchors, filtered against the text they now describe. These are
    // OCCURRENCES: an anchor survives while its own spelling is still there, and
    // reducing them to numbers here is what let a newly staged image save with
    // no offset - so after a reload the record's bare number resolved to the
    // FIRST spelling, which was prose the person had typed before the image.
    // Deleting that prose then enabled a Send whose real reference was still in
    // the text.
    const unbackedAnchors = adoptedAnchors.filter(
      (anchor) => anchor.offset === undefined || spellingSurvives({ ...anchor, offset: anchor.offset }, text),
    );
    const unbacked = unbackedAnchors.map((anchor) => anchor.marker);
    // A stored list describes the snapshot that wrote it, and this source may
    // neither be that writer nor have adopted that snapshot. Reading it
    // unconditionally copies another pane's facts into this one: pane A types
    // "[image 1]" as prose, pane B saves a real image 1, and A's next edit
    // inherits B's marker and makes A's prose-only draft refuse to send. So the
    // stored list is carried only when THIS source adopted the exact snapshot
    // it describes, which `ownsDraft()` alone cannot answer - it is also true
    // for a draft this source wrote itself, and then `staged`/`unbacked` above
    // already speak for it.
    //
    // A recovery row's tags are NOT this function's business. The row's
    // composition is not this composer's draft, so a list derived from the live
    // store describes numbering the row may not use; the row's own record
    // answers for it (see `compositionImageRecord`).
    // The stored anchors keep their OCCURRENCES, so the persisted draft says
    // which of two identical spellings the warning was about. A bare number
    // cannot: it always resolves to the first spelling, and deleting that
    // literal then cleared the warning while the real missing reference was
    // still in the text.
    const carried: CompositionImageAnchor[] =
      ownsDraft() && currentDraftRevisionId === readComposerDraftRevisionId(ref)
        ? readComposerDraftImageAnchors(ref)
        : [];
    // The live anchors are the ones this composer currently holds, WITH their
    // offsets - that is the fact the bare numbers below cannot express.
    const live = unbackedAnchors;
    // A staged or unbacked marker is already described by the two lists above
    // only when it has NO occurrence to carry: those entries are offsetless.
    // An occurrence the record kept is a distinct fact - which spelling - so it
    // survives even when its number is also staged.
    const covered = new Set([...staged, ...unbacked]);
    // `live` has already been through this source's own reconciliation, so it
    // is the authoritative statement of what survived. Retirement exists to
    // stop a STALE record re-admitting a reference that reconciliation threw
    // away - applying it to the live list as well retires an anchor whose
    // offset merely returned to a value an earlier edit had retired. Add a
    // character before an adopted reference and delete it: the anchor is live
    // and correct in the editor, but its offset matches a retired entry, so the
    // save dropped it and a reload sent the reference as prose.
    const liveAnchors: CompositionImageAnchor[] = live;
    // TWO KINDS OF STALE ENTRY, and retirement may only veto one of them.
    //
    // `staged` is THIS revision's own work: the person attached that tile in
    // the composer as it stands. `unbackedAnchors` and `carried` are inherited -
    // the live tags and the stored record's copy, which describe earlier
    // revisions and are exactly what retirement exists to stop re-admitting.
    //
    // The distinction matters because a NUMBER CAN BE REUSED. Delete an adopted
    // missing `[image 1]`, then attach a replacement, and an emptied store hands
    // the new tile number 1 again. Filtering the new tile's staged entry through
    // `retiredAnchors` dropped it, so a reload lost the replacement's bytes
    // while the text still spelled its reference - a marker with no image.
    // The newly staged tiles, recorded independently so they still name their
    // reference after a reload. A staged tile is a fact about the current
    // revision: no earlier retirement describes it.
    //
    // WITH ITS OCCURRENCE. A bare number means "this marker's first spelling",
    // so staging image 1 and then typing a literal `[image 1]` BEFORE the
    // inserted reference saved only the number, and a reload attached the
    // warning to the prose. Deleting that prose then enabled Send while the real
    // reference was still in the text.
    //
    // WHICH spelling is the tile's own is recorded at the splice that inserted
    // it (`stagedReferences`), not inferred here. Inferring it - "the last
    // spelling no other anchor accounts for" - is right only while nothing is
    // typed after the reference. Type a literal `[image N]` after it and the
    // last unaccounted spelling is the PROSE, so the record named prose as the
    // image's anchor: a reload then tied the warning to the words, and deleting
    // the reference left Send blocked on text that never had bytes.
    //
    // A tile whose splice this source never saw (it was staged before this
    // source existed, or re-hydrated from storage) keeps the old reading: the
    // last spelling still unaccounted for. That is a guess, and it is the best
    // available one for a tile nobody observed being inserted.
    //
    // An older RECORD has no offset and keeps the first-spelling reading; that
    // is `resolveAnchors`' existing fallback, unchanged.
    // Occurrences this revision already accounts for, before inheritance is
    // considered. `live` first, because reconciliation is the authority on
    // what survived in the editor.
    const claimedOccurrences = new Set<number>(
      liveAnchors.filter((anchor) => anchor.offset !== undefined).map((anchor) => anchor.offset as number),
    );
    const occurrences = markerOccurrences(text);
    const stagedAnchors: CompositionImageAnchor[] = stagedTiles.map(({ id, marker }) => {
      const observed = stagedReferences.get(id);
      if (observed !== undefined && observed.marker === marker) {
        claimedOccurrences.add(observed.offset);
        return { marker, offset: observed.offset };
      }
      const candidates = occurrences.filter(
        (occurrence) => occurrence.marker === marker && !claimedOccurrences.has(occurrence.offset),
      );
      const own = candidates.at(-1);
      if (own === undefined) return { marker };
      claimedOccurrences.add(own.offset);
      return { marker, offset: own.offset };
    });
    // INHERITANCE IS WHAT IS LEFT OVER. A staged tile has already named the
    // spelling its bytes back, so the stored copy of that same occurrence is not
    // a second fact - and admitting it both duplicated the entry and, by
    // claiming the offset first, pushed the staged tile onto an earlier
    // spelling. That is what made a save idempotent only the first time:
    // `staged` and `carried` described one reference and the second save
    // recorded the pair.
    const inheritedAnchors: CompositionImageAnchor[] = [
      // The live occurrences themselves, minus any whose marker already came in
      // as an offset-bearing live anchor (they ARE those anchors). A bare
      // number stays a bare number: an offsetless live anchor was already the
      // first-spelling fallback before this save.
      ...unbackedAnchors.filter(
        (anchor) => anchor.offset !== undefined || !live.some((item) => item.marker === anchor.marker),
      ),
      ...carried.filter((anchor) =>
        anchor.offset === undefined
          ? !covered.has(anchor.marker) && !live.some((item) => item.marker === anchor.marker)
          : // An occurrence the live half or a staged tile already accounts for
            // is the SAME reference reached twice. A different offset of the
            // same marker is a second reference and is kept.
            !claimedOccurrences.has(anchor.offset) && !live.some((item) => item.offset === anchor.offset),
      ),
    ];
    // Deduplicated by OCCURRENCE: `live` and the first stale list are the same
    // anchors reached by two routes, and two entries for one spelling would
    // give one missing image two owners - deleting the reference would retire
    // one and the other would go on refusing a Send the person had repaired.
    const byOccurrence = new Map<string, CompositionImageAnchor>();
    // RETIREMENT APPLIES TO INHERITED PROVENANCE, NOT TO THIS REVISION'S OWN.
    //
    // A retirement records "this reference is gone", and it is the only thing
    // that stops a stored record re-admitting one. But it is keyed by marker and
    // offset, and a number can be reused: delete an adopted missing `[image 1]`,
    // then attach a replacement, and an emptied store hands the new tile number
    // 1 again. Filtering the NEW tile's staged entry through `retiredAnchors`
    // dropped it, so a reload lost the replacement's bytes while the text still
    // spelled its reference - and Send then went out with a marker and no image.
    //
    // A staged tile is a fact about the CURRENT revision: the person attached it
    // now, and no earlier retirement describes it. So the retirement filter runs
    // only over the anchors carried in from the stored record, and the live and
    // staged halves are recorded independently. They are words the person can
    // see in the editor right now, which is a better authority than a note about
    // a spelling that a previous revision used to contain.
    // THE TEXT, NOT THE STALE LIST, DECIDES WHETHER A REFERENCE SURVIVES.
    //
    // A failed decode strips both the tile and its reference, text first, and
    // the stored list still names that marker for one more save - which made the
    // persisted draft name an image whose bytes it no longer held. That is the
    // issue this PR exists to fix, and the cascade guard caught it ("persisted
    // draft names the markers whose bytes it holds", actual [4, 6, 5] where 5
    // was the failed decode's marker).
    //
    // Holding an offset does not exempt an anchor: an occurrence whose exact
    // spelling is still where it was is alive, but one whose offset no longer
    // lands on its spelling is only dead when the text spells that marker
    // nowhere else. Offsets move with edits before them, so the second test
    // keeps an anchor that merely slid, and drops the one whose reference is
    // gone - which is the distinction the stored list cannot make alone.
    const spellsItsReference = (anchor: CompositionImageAnchor): boolean =>
      markerNumbersIn(text).includes(anchor.marker) ||
      (anchor.offset !== undefined && spellingSurvives({ ...anchor, offset: anchor.offset }, text));
    // The live anchors are authoritative - reconciliation already decided what
    // survived - and this revision's staged tiles are its own work. Neither is
    // subject to retirement's veto.
    for (const anchor of [...liveAnchors, ...stagedAnchors]) {
      byOccurrence.set(anchorKey(anchor), anchor);
    }
    // Inherited provenance answers to BOTH tests: retirement's veto, which stops
    // a stale record re-admitting a reference this source already threw away,
    // and the text's, which stops a stored list naming bytes it no longer holds.
    for (const anchor of inheritedAnchors) {
      if (retiredAnchors.has(anchorKey(anchor))) continue;
      if (!spellsItsReference(anchor)) continue;
      if (byOccurrence.has(anchorKey(anchor))) continue;
      byOccurrence.set(anchorKey(anchor), anchor);
    }
    return [...byOccurrence.values()];
  }

  // Build the composition image record for a composition about to be saved.
  //
  // ONE builder, so the bytes, the eligibility and the anchors of a composition
  // are always derived from the same snapshot. Every earlier bug in this area
  // came from computing one of the three somewhere else: bytes from the live
  // store while eligibility came from the text, or eligibility from a
  // momentary state while bytes came from the row. They are one record.
  //
  // `owned` is every attachment this composition owns - NOT the sendable
  // subset. Inclusion and eligibility are separate facts: a released tile
  // (its reference deleted, the tile kept) belongs in the record with its
  // bytes and `eligible: false`, so a reload restores the image the person
  // still sees without adding it to a send their text does not ask for.
  function compositionImageRecord(
    text: string,
    owned: readonly PendingAttachment[],
    priorAnchors: readonly CompositionImageAnchor[],
    // The exemptions this composition will carry, when they are being installed
    // in the same breath as the record. A caller that is about to adopt must
    // pass them: the record answers "may this tile ship", and adoption is what
    // makes a restored tile shippable, so reading the state before adoption
    // files every restored image ineligible and the next reopen then withholds
    // the exemption the record invented.
    sending?: { readonly restored: readonly string[]; readonly foreign: readonly string[] },
    // The recovery revision this record describes, when the caller knows it.
    // `undefined` leaves the record unstamped, which readers trust: that is the
    // shape a record written for a row whose revision this writer could not see.
    revision?: number,
  ): CompositionImageRecord {
    const snapshot = state.getState();
    const sendableIds = new Set(
      sendableAttachments(owned, text, {
        foreign: sending?.foreign ?? snapshot.foreignImageIds,
        restored: sending?.restored ?? snapshot.restoredImageIds,
      }).map((item) => item.id),
    );
    const foreign = new Set(sending?.foreign ?? snapshot.foreignImageIds);
    const spoken = new Set(markerNumbersIn(text));
    const tiles = owned
      .filter((item) => !foreign.has(item.id))
      .map((item) => ({
        id: item.id,
        marker: item.marker,
        eligible: sendableIds.has(item.id),
        hasBytes: item.data !== undefined,
        // The BYTES, reduced to a digest. A row's flags say "marker 1 has some
        // bytes"; they cannot say WHICH bytes, and two panes can stage
        // different images under the same marker, filename and media type. With
        // both references deleted the payloads match and the flags match, so a
        // reuse decision taken on flags alone lets the stale pane treat the
        // peer's row as proof its own image was saved - and closing loses it.
        // A digest is committed with the fact so a later pane can still compare
        // without the bytes themselves living in this record.
        digest: imageByteDigest(item.data),
      }));
    // An anchor survives while the text still spells it. An anchor is a
    // reference with no bytes, so it has no attachment to be keyed by; it is
    // keyed by the spelling it describes, and it retires when that spelling
    // leaves the text. Both the returned record and the row's own prior list
    // are filtered the same way, so a stale entry cannot outlive its reference.
    const tileMarkers = new Set(tiles.filter((tile) => tile.hasBytes).map((tile) => tile.marker));
    // ONLY what the caller declares is an anchor. Reading
    // `adoptedAnchors` here as well looks harmless and is not: those
    // tags belong to the LIVE composer, and a composition written by another
    // path (a row whose prose says "[image 1]" literally) can use the same
    // number as ordinary text. Carrying them turns that prose into a
    // missing-image anchor, so a reopen refuses a send the row never needed to
    // refuse. The adoption path passes its own computed anchors in; every other
    // writer passes the row's stored ones.
    // The anchor is an OCCURRENCE, not a number. Two spellings of `[image 1]`
    // are two references, and a record that keeps only the number cannot say
    // which one the warning was about - so a merge would tag whichever spelling
    // it found first, and deleting the literal one would clear a warning about
    // the real missing image. Each anchor is therefore resolved to the exact
    // spelling in THIS text, and stored with its offset.
    const anchors = resolveAnchors(priorAnchors, text).filter(
      (anchor) => spoken.has(anchor.marker) && !tileMarkers.has(anchor.marker),
    );
    return { anchors, tiles, ...(revision === undefined ? {} : { revision }) };
  }

  // What a row's stored image facts say, in the form the reuse comparison
  // compares. Absent and empty agree - neither states an anchor - and both
  // differ from a record that states one. The stored `revision` is left out on
  // purpose: it counts one row's own commits and says nothing about whether two
  // compositions mean the same thing.
  //
  // So is each tile's ID. An id is minted per PANE - decoding a row gives every
  // tile a fresh one - so two panes holding the same image, with identical
  // bytes, marker and eligibility, still disagree on the id. Comparing it made
  // the same composition look different and produced a SECOND actionable
  // recovery row for work that was already preserved. What identifies a tile to
  // a reader is its marker, its eligibility and its bytes; the id is the key a
  // single composition uses internally, not something another pane can share.
  //
  // Anchors are compared by marker alone for the same reason: an offset is a
  // position in one composition's text, and the two panes' texts are the same
  // text here, so the offsets agree whenever the markers do.
  // WHICH COPY IS AUTHORITATIVE: the candidate ROW, not a side record by id.
  //
  // Text and bytes are committed to IndexedDB and these facts to localStorage,
  // so the two can disagree. Reading the side record here asks "what did the
  // last write to localStorage say about this row id?" - and after a peer
  // replaces a missing reference with identical literal text, the row commits an
  // EMPTY anchor list while the side record still describes the reference it
  // removed. A stale pane then reads its OWN warning out of that record, calls
  // the peer's row proof that its composition was saved, and reuses it: the row
  // reopens without the warning the pane is looking at.
  //
  // The row is the only witness to what it contains. `imageFacts` is handed the
  // record being considered, so this reads the anchors and eligibility the
  // transaction committed WITH that text. The side record is used only for the
  // fields a row cannot state, and only when it names that revision - the same
  // derivation rule `compositionAnchorsForRecord` applies, so there is one
  // answer to "which record describes this composition" rather than two.
  function storedImageFacts(record: MutationRecoveryRecord): string {
    // `anchors` is an optional field the writers add; the portable record type
    // does not declare it, so it is read the same way `recoveryDraft.ts` reads
    // it rather than by widening another lane's interface.
    const declared = record as { anchors?: CompositionImageAnchor[] };
    const committed = Array.isArray(declared.anchors) ? declared.anchors : undefined;
    const side = readCompositionImageRecord(record.clientMutationId);
    // THE ANCHORS ARE THE ROW'S, THE TILES ARE THE SIDE RECORD'S, and the split
    // is not arbitrary.
    //
    // An anchor list is committed WITH the text it describes (the writers pass
    // it in the same transaction), so the row is the authority on which
    // references its text carries. The side record is keyed by row id alone and
    // survives a repair: a peer can commit an EMPTY anchor list - "this text
    // references no missing image" - and leave the old side record still naming
    // the reference it removed. Reading that record's anchors made a stale pane
    // treat the peer's row as proof its own composition was saved, so the row
    // reopened holding a warning the committed text no longer had.
    //
    // Per-tile eligibility has no row field to read: the row states which
    // attachments it owns, not which are sendable, and a released tile's bytes
    // are in `attachments` beside a sendable one's. The side record is the only
    // place that fact is written, so it stays the source for tiles - with the
    // digest that separates two images staged under one marker.
    const anchors = committed ?? side?.anchors ?? compositionAnchorsForRecord(declared, record.clientMutationId) ?? [];
    return imageFactsOf(anchors, side?.tiles ?? []);
  }

  /** A cheap, stable fingerprint of an attachment's base64 bytes.
   *
   * Two panes can stage DIFFERENT images under one marker with the same filename
   * and media type, so `hasBytes` cannot tell their compositions apart. A
   * cryptographic hash buys nothing here - this decides whether two in-memory
   * strings describing the same user's two uploads are the same upload - and a
   * 53-bit rolling hash over the whole string is enough to separate two PNGs
   * while staying cheap on a megabyte of base64.
   */
  function imageByteDigest(data: string | undefined): string {
    if (data === undefined) return "";
    // FNV-1a over UTF-16 code units, plus the length: the length alone already
    // separates most different encodings, and the hash separates the rest.
    let hash = 0x811c9dc5;
    for (let index = 0; index < data.length; index += 1) {
      hash ^= data.charCodeAt(index);
      hash = Math.imul(hash, 0x01000193) >>> 0;
    }
    return `${data.length}:${hash.toString(36)}`;
  }

  /** Per-tile facts, ordered and without any per-pane identity. */
  function comparableTileFacts(
    tiles: readonly { marker: number; eligible: boolean; hasBytes: boolean; digest?: string }[],
  ): { marker: number; eligible: boolean; hasBytes: boolean; digest: string }[] {
    return tiles
      .map((tile) => ({
        marker: tile.marker,
        eligible: tile.eligible,
        hasBytes: tile.hasBytes,
        digest: tile.digest ?? "",
      }))
      .sort((left, right) => left.marker - right.marker || left.digest.localeCompare(right.digest));
  }

  /** THE comparator for "is this the same composition's image state".
   *
   * Two serialisers used to answer this - one for a stored record, one for a
   * composition about to be written - and they disagreed by construction: the
   * live side emitted a `bytes` field the stored side never had, so equality was
   * impossible EVEN FOR A TEXT-ONLY DRAFT and every stale save minted a second
   * recovery row instead of reusing the peer's. Two comparators that cannot
   * agree is the same defect as two predicates that cannot agree.
   *
   * One function, both callers. It covers everything the reuse decision needs:
   * anchors as OCCURRENCES (which spelling, not just which number), per-tile
   * eligibility, and the byte digest that separates two images staged under one
   * marker with the same filename and media type.
   */
  function imageFactsOf(
    anchors: readonly CompositionImageAnchor[],
    tiles: readonly { marker: number; eligible: boolean; hasBytes: boolean; digest?: string }[],
  ): string {
    return JSON.stringify({
      // OCCURRENCES, in canonical order. Collapsing to numbers made two
      // distinct missing references of one marker look like one, so a pane
      // could reuse a row that had lost one of them.
      anchors: anchors
        .map((anchor) => (anchor.offset === undefined ? `${anchor.marker}:` : `${anchor.marker}@${anchor.offset}`))
        .sort(),
      // Digest-carrying facts: "this marker has some bytes" cannot distinguish
      // two panes that staged different images under it.
      tiles: comparableTileFacts(tiles),
    });
  }

  // The same form, for a composition that has not been stored yet.
  function compositionImageRecordSnapshot(
    text: string,
    owned: readonly PendingAttachment[],
    priorAnchors: readonly CompositionImageAnchor[],
  ): string {
    const record = compositionImageRecord(text, owned, priorAnchors);
    return imageFactsOf(record.anchors, record.tiles);
  }

  // Which of a caller's `InputAttachment`s belong to THIS composition.
  //
  // `InputAttachment` carries no id - it is the wire shape - so ownership is
  // resolved against the store by identity on the tiles that are actually
  // owned, matching the payload back on all four of its fields. A number alone
  // is not an owner: a foreign tile retained across a rebind can bear the same
  // label as an owned one, and filtering by label would then keep the wrong
  // tile's bytes and drop the right tile's.
  //
  // ONE function for both durable writers (the recovery commit and the
  // stale-lease preservation), because two filters for one question is how
  // they drift apart.
  function compositionAttachmentsWhere(
    candidates: readonly InputAttachment[],
    include: (item: PendingAttachment) => boolean,
  ): InputAttachment[] {
    const owned = operations.items.filter((item) => item.data !== undefined && include(item));
    return candidates.filter((candidate) =>
      owned.some(
        (item) =>
          item.marker === candidate.marker &&
          item.data === candidate.data &&
          item.name === candidate.name &&
          item.mediaType === candidate.mediaType,
      ),
    );
  }

  function ownedCompositionAttachments(candidates: readonly InputAttachment[]): InputAttachment[] {
    const foreign = new Set(state.getState().foreignImageIds);
    return compositionAttachmentsWhere(candidates, (item) => !foreign.has(item.id));
  }

  // The same list, narrowed to the tiles a Send from this composition would
  // carry. Both are views of ONE caller-supplied list, resolved by attachment
  // identity, because the payload needs the sendable subset while the durable
  // composition must keep everything it owns.
  function sendableCompositionAttachments(candidates: readonly InputAttachment[], text: string): InputAttachment[] {
    const snapshot = state.getState();
    const sendable = new Set(
      sendableAttachments(operations.items, text, {
        foreign: snapshot.foreignImageIds,
        restored: snapshot.restoredImageIds,
      }).map((item) => item.id),
    );
    return compositionAttachmentsWhere(candidates, (item) => sendable.has(item.id));
  }

  function persistDraft(text: string): void {
    if (disposed) return;
    currentDraftRevisionId = writeComposerDraft(
      ref,
      {
        text,
        skillNames: skillNamesRef.current,
        ...(selectionMetaRef.current.commandNames.length
          ? { commandNames: selectionMetaRef.current.commandNames }
          : {}),
        ...(selectionMetaRef.current.mentions ? { mentions: selectionMetaRef.current.mentions } : {}),
      },
      recordedMarkerAnchors(text),
    );
    ownedDraftRevisionRef.current = readDraftRevision(ref);
  }
  function markRestore(): void {
    if (!disposed) state.setState((snapshot) => ({ restoreEpoch: snapshot.restoreEpoch + 1 }));
  }
  function writeDraft(text: string, mayPersist: boolean, source?: TextEditSource): void {
    if (disposed) return;
    const next = patchSelectionText(
      { text: textRef.current, skillNames: skillNamesRef.current, ...selectionMetaRef.current },
      text,
    );
    updateSkillNames(next.skillNames);
    updateSelectionMeta(next);
    if (source === "submission") {
      updateText(text);
      // React can batch exact marker removals, so restore their already mapped atoms.
      markRestore();
    } else editText(text);
    if (mayPersist && currentRecoveryId() === null) persistDraft(text);
  }
  function writeDetachedSourceDraft(text: string, _cursor: number, source?: TextEditSource): void {
    // A detached continuation cannot claim another pane's newer draft.
    if (disposed || !ownsDraft()) return;
    writeDraft(text, true, source);
  }
  const editor: TextEditor = {
    read: () => {
      const mounted = boundEditor?.read();
      if (mounted) return mounted;
      const text = currentDraftText();
      return { text, cursor: text.length, selection: { start: text.length, end: text.length } };
    },
    write: (text, cursor, source) => {
      if (disposed) return;
      if (boundEditor) boundEditor.write(text, cursor, source);
      else writeDetachedSourceDraft(text, cursor, source);
    },
  };
  // A tile this source was already holding when it adopted another
  // composition's text is FOREIGN: its reference may share a number with the
  // adopted draft's prose, but the text belongs to that draft's image, not to
  // this tile. Completions that settle later read this to decide whether they
  // may edit the text at all.
  const ownsImageReference = (item: { id: string; marker: number }) =>
    !state.getState().foreignImageIds.includes(item.id);
  const operations = createAttachmentOperations(editor, attachments, ownsImageReference);

  // Markers an adoption brought in without bytes, each with the OCCURRENCE it
  // named. Seeded only from a draft this source did not own (the mount read, or
  // bindEditor replacing a stale draft with a newer one), never from anything
  // typed here.
  //
  // An occurrence, not a number: `[image 1]` can appear twice in one text - the
  // anchor whose bytes are gone and a literal spelling that was always prose -
  // and a number cannot say which of the two it meant. Retiring by "is this
  // number still spelled anywhere" then keeps the tag alive on the strength of
  // the OTHER occurrence, and deleting the real reference leaves Send disabled
  // forever. The offset moves with the text through the same splice logic the
  // mention anchors use, so an edit that removes the anchor retires it while the
  // literal spelling beside it stays prose.
  let adoptedAnchors: { marker: number; offset: number }[] = [];

  /** Each staged tile's OWN reference, by tile id.
   *
   * Which spelling of `[image N]` a tile's bytes back is a fact about the tile,
   * and the splice that inserted that spelling is the only moment it is
   * knowable. Deriving it later - "the last spelling nothing else accounts
   * for" - is wrong the moment the person types the same literal AFTER the
   * reference: the last unaccounted spelling is then the prose, so the record
   * named the prose as the image's anchor and a reload tied the warning to the
   * words rather than to the reference. Deleting the reference left Send
   * blocked on text that was only prose.
   *
   * Recorded at insertion and moved by the same splice arithmetic the adopted
   * anchors use, because an offset is a position in THIS text and every later
   * edit before it shifts it.
   */
  const stagedReferences = new Map<string, { marker: number; offset: number }>();

  /** Occurrences this composer RETIRED, as `marker:offset` (or `marker:` bare).
   *
   * A stored draft record is copied forward so an anchor adopted before this
   * source started editing is not lost. That copy is only safe for
   * occurrences this source has NOT retired: an edit that removes a reference
   * drops it from `adoptedAnchors`, and re-admitting it from the stored list
   * would bring the warning back the moment the person typed the same spelling
   * at the same place - blocking a Send they had repaired. Nothing else in the
   * pipeline remembers the retirement, because the retirement IS the absence
   * from `adoptedAnchors`.
   */
  const retiredAnchors = new Set<string>();

  function anchorKey(anchor: { marker: number; offset?: number }): string {
    return `${anchor.marker}:${anchor.offset ?? ""}`;
  }

  function markerNumbersIn(text: string): number[] {
    return Array.from(text.matchAll(markerPattern()), (match) => Number(match[1]));
  }

  /** Every `[image N]` in `text` with the offset it sits at. */
  function markerOccurrences(text: string): { marker: number; offset: number }[] {
    return Array.from(text.matchAll(markerPattern()), (match) => ({
      marker: Number(match[1]),
      offset: match.index ?? 0,
    }));
  }

  // An anchor keeps its tag while its reference survives, and only while it
  // survives: once the person deletes "[image N]" the adoption no longer
  // covers anything, so a later typing of that same spelling is literal prose
  // and must send. Retiring on absence is what separates "this anchor lost its
  // bytes" from "this text merely mentions the format".
  //
  // A blank text retires every tag, which is right for both ways a composition
  // ends: a send finished it, or a recovery row took it. The row is not a loss
  // of provenance, because opening a row adopts the markers its own text
  // spells without bytes; nothing needs carrying across the clear.
  //
  // A locally staged image never rescues an adopted marker, even when the
  // numbers match. An adopted text belongs to another draft, so bytes staged
  // beside it in this source are a different image; letting them satisfy the
  // anchor would pair unrelated bytes with the adopted text.
  function refreshUnbackedImageMarkers(): void {
    // Publishing only. WHICH anchors live is decided by
    // `reconcileAdoptedAnchors`, on every text write - one rule, in the one
    // place the text changes. A second filter here that re-reads spellings
    // would answer the same question a different way, and the two answers
    // disagree exactly when a number is spelled twice.
    const next = liveAdoptedMarkers();
    const current = state.getState().unbackedImageMarkers;
    if (next.length === current.length && next.every((marker, index) => marker === current[index])) return;
    state.setState({ unbackedImageMarkers: next });
  }

  /** The marker numbers the live anchors still name, in order. */
  function liveAdoptedMarkers(): number[] {
    return [...new Set(adoptedAnchors.map((anchor) => anchor.marker))].sort((left, right) => left - right);
  }

  /** The live tags AS OCCURRENCES.
   *
   * The merge's local side was a marker-number list, so it could only ever
   * express "this number is missing somewhere". When the incoming row held a
   * genuine missing reference of the SAME number, the incoming occurrence
   * claimed it and the local side's real, different spelling was dropped. The
   * merged text then spelled a reference nothing warned about: deleting the
   * incoming one enabled Send while the local missing reference was still
   * there. A number cannot say which spelling it meant; the occurrence can.
   */
  function liveAdoptedOccurrences(): CompositionImageAnchor[] {
    return adoptedAnchors.map(({ marker, offset }) => ({ marker, offset }));
  }

  /** This pane's own live anchors, dropped unless their spelling is still in
   * `text`.
   *
   * `text` is a PARAMETER, not `textRef`, because the caller installing these
   * anchors may be installing them into text that landed while a durable write
   * was in flight: `latest.text`, not the snapshot the write was built from. A
   * caption typed before a reference during that window shifts its offset, so
   * testing the reference against the stale snapshot drops the anchor, and the
   * text that keeps the caption then has no anchor for the reference beside it.
   */
  function localAnchorsIn(text: string): CompositionImageAnchor[] {
    return liveAdoptedOccurrences().filter(
      (anchor) =>
        anchor.offset !== undefined && spellingSurvives({ marker: anchor.marker, offset: anchor.offset }, text),
    );
  }

  /** True while `anchor`'s offset still spells its own marker in `text`. */
  function spellingSurvives(anchor: { marker: number; offset: number }, text: string): boolean {
    const spelling = markerText(anchor.marker);
    return text.slice(anchor.offset, anchor.offset + spelling.length) === spelling;
  }

  /** Where a pure insertion turned `shorter` into `longer`, if it did.
   *
   * `longer` is `shorter` with `delta` characters spliced in at one point.
   * Testing that hypothesis - by removing those characters and comparing - is
   * what distinguishes "text was added beside the anchor" from "the anchor's
   * own characters changed". A shared prefix and suffix cannot tell them
   * apart when the inserted text and the anchor begin with the same words,
   * which is exactly the `[image 1]` / `[image 2]` case.
   *
   * Returns `undefined` when no single insertion explains the difference, so
   * the caller falls back to the prefix/suffix bound.
   */
  function insertionPoint(shorter: string, longer: string, delta: number): number | undefined {
    if (longer.length - shorter.length !== delta || delta <= 0) return undefined;
    // Several positions can explain one difference, and the choice among them
    // decides whether an anchor survives. `[image 1]` becoming
    // `[image 2][image 1]` reads either as nine characters inserted at 0 or at
    // 7 - and 7 is the split that cuts the anchor's own marker apart, leaving
    // an offset that no longer spells it. The earliest reconstruction is the
    // one that keeps every anchor's characters contiguous, so it is the one
    // taken.
    for (let at = 0; at <= shorter.length; at += 1) {
      if (longer.slice(0, at) === shorter.slice(0, at) && longer.slice(at + delta) === shorter.slice(at)) return at;
    }
    return undefined;
  }

  /** Carry the adopted anchors across an edit to the text.
   *
   * An anchor dies only when the edit DESTROYS its characters. A shared prefix
   * and suffix cannot tell that from an insertion, and the difference is not
   * academic: pasting `[image 2]` before a missing `[image 1]` shares the
   * `[image ` prefix with the anchor, so the changed region looks like it
   * spans the anchor and the warning was dropped while the person's reference
   * was still in the text. Send then went out with the old marker as literal
   * prose.
   *
   * An insertion cannot destroy anything, so its hypothesis is tested rather
   * than assumed: if removing the length delta from `after` reproduces
   * `before`, nothing was removed and every anchor survives, the ones at or
   * after the insertion point shifted. A deletion is the mirror, and there the
   * anchors inside the removed range are exactly the ones that die. Only when
   * neither reconstruction holds (a replacement, or several edits at once) is
   * the shared prefix and suffix the best available bound.
   */
  function reconcileAdoptedAnchors(before: string, after: string, changed?: TextChangeRange): void {
    if (before === after || adoptedAnchors.length === 0) return;
    // Remember what this edit retires, wherever in this function it happens.
    // The retirement's only other record is the absence from
    // `adoptedAnchors`, which a stored draft record would then silently undo.
    const beforeReconcile = adoptedAnchors;
    try {
      reconcileAdoptedAnchorsInner(before, after, changed);
    } finally {
      for (const anchor of beforeReconcile) {
        if (!adoptedAnchors.some((kept) => anchorKey(kept) === anchorKey(anchor))) {
          retiredAnchors.add(anchorKey(anchor));
          // A retirement is also the whole marker: an anchor with no offset is
          // the offsetless fallback for the same reference, and re-admitting
          // that would restore the warning under a different spelling.
          retiredAnchors.add(`${anchor.marker}:`);
        }
      }
    }
  }

  /** Move anchors through one edit.
   *
   * ONE rule for every offset in this source: an anchor is a position in the
   * text being edited, and an edit either leaves it where it is, shifts it by
   * the delta, or destroys the spelling it named. A staged tile's own reference
   * is such a position too - it is recorded at the splice and must follow the
   * same arithmetic - so this is a pure function rather than a mutation of
   * `adoptedAnchors`, and both callers get identical handling by construction.
   */
  function mapThroughSpan(
    anchors: readonly { marker: number; offset: number }[],
    span: { from: number; to: number; delta: number },
  ): { marker: number; offset: number }[] {
    return anchors.flatMap((anchor) => {
      const length = markerText(anchor.marker).length;
      // Entirely before the step: the step's own coordinates did not reach it.
      if (anchor.offset + length <= span.from) return [anchor];
      // Entirely after it: it slides by the length the step changed.
      if (anchor.offset >= span.to) return [{ marker: anchor.marker, offset: anchor.offset + span.delta }];
      // It OVERLAPS what this step replaced, so its own characters are gone.
      return [];
    });
  }

  function mapAnchorsThroughEdit(
    anchors: readonly { marker: number; offset: number }[],
    before: string,
    after: string,
    changed?: TextChangeRange,
  ): { marker: number; offset: number }[] {
    if (before === after || anchors.length === 0) return [...anchors];
    // A COMPOUND EDIT IS MAPPED STEP BY STEP, not through its hull.
    //
    // `from`/`to` bound every step the transaction applied, and a drag that
    // deletes at one offset and inserts at another puts untouched text between
    // them inside that bound. Trusting the hull retires an anchor whose own
    // characters are still exactly where they were - so dragging prose past a
    // missing reference cleared its warning, and the reference then shipped as
    // literal text. Each step's own span says what that step did; an anchor
    // the step did not touch is carried through it unchanged.
    if (changed?.spans !== undefined && changed.spans.length > 1) {
      let moved = anchors.map(({ marker, offset }) => ({ marker, offset }));
      for (const span of changed.spans) {
        moved = mapThroughSpan(moved, span);
      }
      return moved.filter((anchor) => spellingSurvives(anchor, after));
    }
    // When the editor reports what it replaced, the range settles the one
    // question two strings cannot: WHICH of two identical spellings went.
    // `[image 1][image 1]` with the second one a reference, edited to
    // `[image 1]`, is indistinguishable from editing away the first - both
    // readings explain the strings, and the inference below takes the earlier
    // one, which retires the reference while keeping the prose. The reported
    // range names the removed span outright.
    if (changed !== undefined && changed.to === changed.from) {
      // A pure INSERTION at a known point. Nothing can be destroyed by text
      // arriving beside an anchor, but an insertion INSIDE its spelling is a
      // different thing: `[image 1]` edited to `[imXage 1]` no longer reads as
      // a marker, so that anchor is retired rather than carried. Using the
      // reported point matters because the inference cannot tell an insertion
      // before an anchor from one that split it when the inserted text shares
      // a prefix with the marker - which `[image 1]` pasted after `[image 1]`
      // always does.
      const delta = after.length - before.length;
      const at = changed.from;
      return anchors.flatMap((anchor) => {
        const length = markerText(anchor.marker).length;
        if (anchor.offset >= at) {
          const shifted = { ...anchor, offset: anchor.offset + delta };
          return spellingSurvives(shifted, after) ? [shifted] : [];
        }
        return anchor.offset + length <= at ? [anchor] : [];
      });
    }
    if (changed !== undefined && changed.to > changed.from) {
      return anchors.flatMap((anchor) => {
        const length = markerText(anchor.marker).length;
        const anchorEnd = anchor.offset + length;
        // An occurrence the change does not touch survives untouched.
        if (anchorEnd <= changed.from) return [anchor];
        if (anchor.offset >= changed.to) return [{ ...anchor, offset: anchor.offset + (after.length - before.length) }];
        // It OVERLAPS the replaced span, so its own characters are gone. An
        // anchor whose offset sits past the span in the PREVIOUS text shifted
        // by the same delta; one inside it is retired. This branch is only
        // taken for an edit that actually removed or rewrote text, which is
        // what keeps a pure insertion beside an anchor from retiring it.
        return [];
      });
    }
    const delta = after.length - before.length;
    if (delta > 0) {
      const at = insertionPoint(before, after, delta);
      if (at !== undefined) {
        // An insertion BEFORE an anchor cannot touch it, and the anchor is
        // shifted to follow its own text. An insertion INSIDE it destroys the
        // spelling the anchor named: `[image 1]` edited to `[imXage 1]` leaves
        // an offset that no longer reads as a marker, so the anchor is retired
        // rather than carried. Keeping it was the defect: the obsolete tag
        // survived invisibly and came back as a blocked Send the moment the
        // person typed the literal `[image 1]` somewhere else.
        return anchors.flatMap((anchor) => {
          if (anchor.offset >= at) {
            const shifted = { ...anchor, offset: anchor.offset + delta };
            return spellingSurvives(shifted, after) ? [shifted] : [];
          }
          return anchor.offset + markerText(anchor.marker).length <= at ? [anchor] : [];
        });
      }
    } else if (delta < 0) {
      const at = insertionPoint(after, before, -delta);
      if (at !== undefined) {
        const removedThrough = at - delta;
        return anchors.flatMap((anchor) => {
          const length = markerText(anchor.marker).length;
          if (anchor.offset + length <= at) return [anchor];
          if (anchor.offset >= removedThrough) return [{ ...anchor, offset: anchor.offset + delta }];
          return [];
        });
      }
    }
    let prefix = 0;
    const limit = Math.min(before.length, after.length);
    while (prefix < limit && before.charCodeAt(prefix) === after.charCodeAt(prefix)) prefix += 1;
    let suffix = 0;
    while (
      suffix < limit - prefix &&
      before.charCodeAt(before.length - 1 - suffix) === after.charCodeAt(after.length - 1 - suffix)
    )
      suffix += 1;
    const oldEnd = before.length - suffix;
    return anchors.flatMap((anchor) => {
      const length = markerText(anchor.marker).length;
      if (anchor.offset + length <= prefix) return [anchor];
      if (anchor.offset >= oldEnd) return [{ ...anchor, offset: anchor.offset + delta }];
      return [];
    });
  }

  function reconcileAdoptedAnchorsInner(before: string, after: string, changed?: TextChangeRange): void {
    adoptedAnchors = mapAnchorsThroughEdit(adoptedAnchors, before, after, changed);
  }

  /** Resolve stored anchors to the occurrences they name in `text`.
   *
   * An anchor that carries an offset keeps it ONLY while that offset still
   * spells its number: that is the spelling the record was about, and an
   * identical spelling elsewhere is a different reference. When the offset no
   * longer spells it, the anchor is DROPPED rather than relocated - falling
   * back to "the first spelling" here would re-tag exactly the occurrence this
   * field exists to distinguish, and would resurrect a warning the person had
   * already repaired.
   *
   * An anchor with no offset (a record written before offsets existed, or a
   * caller that only knows the number) resolves to that number's first
   * unclaimed spelling, which is how those records were read before the field
   * existed.
   */
  function resolveAnchors(
    anchors: readonly CompositionImageAnchor[],
    text: string,
  ): { marker: number; offset: number }[] {
    const spelled = markerOccurrences(text);
    const used = new Set<number>();
    const out: { marker: number; offset: number }[] = [];
    for (const anchor of anchors) {
      const hit = spelled.find((occurrence) =>
        anchor.offset !== undefined
          ? occurrence.marker === anchor.marker && occurrence.offset === anchor.offset && !used.has(occurrence.offset)
          : occurrence.marker === anchor.marker && !used.has(occurrence.offset),
      );
      if (hit === undefined) continue;
      used.add(hit.offset);
      out.push(hit);
    }
    return out;
  }

  /** Adopt the anchors a persisted record says stood for images. */
  function adoptUnbackedImageMarkers(recorded: readonly CompositionImageAnchor[]): void {
    adoptedAnchors = resolveAnchors(recorded, textRef.current);
    refreshUnbackedImageMarkers();
  }

  /** Move anchors from a recovery row's own text into the text that carries it.
   *
   * A merge appends the row's text to the current draft's, so every offset the
   * row's record named is now wrong by the length of the prefix. Recomputing
   * from a length is not safe either: the merge renumbers the row's TILE-backed
   * markers, and a wider number shifts everything after it.
   *
   * An anchor, though, names a reference with NO tile - that is what an anchor
   * is - and the merge never renumbers one. Its spelling survives verbatim, so
   * its position can be named without arithmetic: count from the END. The k-th
   * spelling of a number from the end of the row's text is the same reference
   * as the k-th spelling from the end of the merged text, because the row's
   * text is the merged text's suffix and nothing after it was added.
   *
   * This is what keeps the merged text's literal prose and the row's real
   * missing reference apart: prose came from the prefix, so counting from the
   * end never selects it.
   */
  function anchorsIntoMergedText(
    anchors: readonly CompositionImageAnchor[],
    recoveredText: string,
    mergedText: string,
  ): CompositionImageAnchor[] {
    const source = markerOccurrences(recoveredText);
    const target = markerOccurrences(mergedText);
    return anchors.flatMap((anchor) => {
      // An offsetless anchor describes no position, so there is nothing to
      // translate: it is resolved when it is installed, against the text it is
      // installed into. That is also what keeps a LOCAL tag - which belongs to
      // the merge's prefix, not to the row - from being counted into the row's
      // matching from the end.
      if (anchor.offset === undefined) return [anchor];
      const sameMarkerSource = source.filter((occurrence) => occurrence.marker === anchor.marker);
      const ordinal = sameMarkerSource.findIndex((occurrence) => occurrence.offset === anchor.offset);
      if (ordinal < 0) return [];
      const fromEnd = sameMarkerSource.length - ordinal;
      const sameMarkerTarget = target.filter((occurrence) => occurrence.marker === anchor.marker);
      const hit = sameMarkerTarget[sameMarkerTarget.length - fromEnd];
      return hit === undefined ? [] : [{ marker: hit.marker, offset: hit.offset }];
    });
  }

  /** The merged composition's anchors: both sides in ONE coordinate system.
   *
   * The row's anchors are positions in the row's text; the live ones are
   * positions in this pane's text, which is the merged text's PREFIX. Comparing
   * them before translation is a comparison of two different rulers, and it is
   * exactly how a genuine local missing reference was dropped: both sides began
   * with `[image 1]`, both offsets were 0, the row's entry claimed the key, and
   * the local warning vanished - so deleting the incoming reference enabled a
   * Send whose local reference was still in the text.
   *
   * Translate first, then deduplicate. One spelling reached by two routes is one
   * reference; two spellings of one number are two references and both stay.
   * Both readers - the live editor and the durable row - take their set from
   * here, so they cannot disagree about which spelling a warning belongs to.
   */
  function anchorsIntoOneComposition(
    rowAnchors: readonly CompositionImageAnchor[],
    localAnchors: readonly CompositionImageAnchor[],
    recoveredText: string,
    mergedText: string,
  ): CompositionImageAnchor[] {
    const merged = [...anchorsIntoMergedText(rowAnchors, recoveredText, mergedText), ...localAnchors];
    const seen = new Set<string>();
    return merged.filter((anchor) => {
      const key = anchorKey(anchor);
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
  }

  // A restored recovery composition is authoritative about its own images: its
  // attachment list says which markers were images, and its payload says which
  // of those still carry bytes. Marker numbers the list never named were
  // prose, so they are not adopted; numbers it named whose bytes did not
  // survive are anchors with nothing behind them.
  //
  // The exemption is recorded against the SETTLED TILE, by id, and read from
  // that same tile's marker. The merge renumbers a recovered image when the
  // incoming row collides with the current draft's own numbering (marker 1
  // becomes 2), so a marker recorded here would describe the row's original
  // reference while the tile that carries the bytes answers to the new one -
  // the exemption would miss its own image and a resend would consume the row
  // after sending only its prose.
  //
  // The local tags surviving the merge are kept: a reference this composer
  // adopted before the row arrived is still an anchor with no bytes, and
  // replacing the set is what cleared that refusal.
  // The exemptions adopting `recovered` will install, computed without
  // installing them.
  //
  // Split out because the DURABLE write needs the answer too, and it has to
  // happen in the same step as the merge that makes the composition durable:
  // a reader that waits for the editor to catch up leaves a window where the
  // row holds merged text and no record of its images. Computing it twice
  // would be the drift this whole PR is about, so both callers take it from
  // here and `adoptRecoveredImageFacts` installs exactly this.
  function recoveredImageFacts(
    recovered: RecoveredComposerDraft,
    incomingRowId: string | null,
    // The row this composition will be stored as, when the caller computes
    // these facts BEFORE the transaction that creates that ownership. Reading
    // `activeRecoveryId` then answers with the row that is still active - or
    // with nothing, which falls through to the LOCAL DRAFT's record and turns
    // a staged tile's stored marker into an anchor the composition never had.
    // Naming the target row keeps the answer about that row.
    activeRowId?: string | null,
  ): {
    restored: string[];
    foreign: string[];
    /** Anchors in the ROW's text, which callers translate into the merged text. */
    anchors: CompositionImageAnchor[];
    /** Anchors already in THIS composer's text - the merged text's prefix - and
     * therefore never translated. See the note where they are collected. */
    localAnchors: CompositionImageAnchor[];
  } {
    // A row's own tags, read from the row that ARRIVED - not from whichever row
    // is active afterwards. Opening a second row merges its text into the row
    // already open and consumes the second, so "the active row's record" then
    // answers with the FIRST row and drops the incoming anchor: the merged text
    // spells a reference nothing is left to warn about, and Send ships it as
    // literal prose. Read here, before the caller clears the incoming row.
    // The row's own record, read by the row that ARRIVED. `tiles` is keyed by
    // attachment id and `anchors` by spelling; neither is a marker-number list
    // borrowed from whatever the store happens to hold.
    // The row's anchors, live copy first. The live copy is the localStorage
    // record; the durable copy is the `anchors` field the merge transaction
    // committed beside the text, carried here on the decoded draft. Reading only
    // the live copy was the defect: the two stores cannot share a transaction,
    // so a closed pane or a full quota left a row whose text spells `[image 1]`
    // and nothing saying that reference had no bytes, and a reopen shipped it
    // as prose. Both describe the same revision when both exist.
    // The row's own anchors come first, from the record committed WITH its
    // text - and an EMPTY list is an answer, not a missing one. Testing
    // `length > 0` here read "the merge retired the last reference" as "this row
    // states nothing" and fell through to the localStorage record, which still
    // described an older revision. A failed write then let that older record win
    // and a reopen shipped the missing reference as prose.
    const incomingRecord =
      rowCommittedAnchors(recovered) ??
      (incomingRowId === null
        ? undefined
        : (() => {
            const liveRecord = readCompositionImageRecord(incomingRowId);
            return liveRecord === undefined ? undefined : { anchors: liveRecord.anchors };
          })());
    const restoredMarkers = new Set(recovered.attachments.map((item) => item.marker));
    const declared = new Set(recoveryDeclaredMarkers(recovered));
    const spoken = new Set(markerNumbersIn(recovered.text));
    // A row that once held a missing image has no attachment entry to describe
    // it: the record's list carries only tiles that still have bytes, so a
    // marker whose bytes were already gone is not among `declared`. Deriving
    // the row's facts from this record alone therefore loses the anchor, and a
    // reopen sends it as literal prose. The composition filed the tag against
    // the ROW before it cleared the local draft that held it, and the row is
    // the right key: several panes share a ref, so a per-ref list lets any
    // other pane's write replace this row's tags, dropping its missing
    // reference and letting its numbers block a different pane's literal text.
    // Restored here because this is the one point both open paths reach.
    const rowId = activeRowId === undefined ? state.getState().activeRecoveryId : activeRowId;
    // WHICH RECORD DESCRIBES THIS COMPOSITION.
    //
    // The row's own record, whenever there is a row: it was written with that
    // row's composition, so its anchors are the ones the merged text inherits.
    //
    // The LOCAL DRAFT's record only while this source actually owns that
    // draft. Otherwise the draft on disk is somebody else's work that this pane
    // has not adopted, and its stored markers describe a composition this
    // merge is not committing - reading them turns a staged tile's marker into
    // an anchor the merge never had, and the row then refuses a send it should
    // allow. A pane that opened a row and never touched the local draft is
    // exactly that case.
    // Marker NUMBERS, deliberately: both branches below name "a number this
    // composition once had an image under", and the occurrence the row's own
    // record may also carry is read separately as `declaredAnchors` - that is
    // the one place an offset is authoritative, and duplicating it here as a
    // number would be the second answer this whole change removes.
    // The row's own copy wins when the row being asked about IS the row this
    // draft was decoded from - the reopen and activation paths. Reading the live
    // store there was a second answer to the question `incomingRecord` already
    // answered: a failed localStorage write left an older revision's numbers,
    // and a merge that retired its last reference re-stamped the retired one
    // onto the new text, blocking a Send the person had repaired.
    //
    // When they are DIFFERENT rows - a merge into an active row - this must
    // describe the ACTIVE row's prior tags, which this draft does not carry, so
    // the live store stays the only source it has.
    const sameRowAsRecovered = rowId !== null && rowId === incomingRowId;
    // The ACTIVE row's prior tags, when this merge is into a row other than the
    // one that arrived. The row's own committed copy is still the first answer,
    // and the side record is consulted only when the row states none - and only
    // when it describes the revision being merged. Reading it unconditionally
    // was how a failed localStorage write left an older revision's list in
    // force: the merge retired the last reference, the write did not land, and
    // the obsolete side record then re-stamped it onto the next revision.
    //
    // OCCURRENCES on every branch. Reducing these to numbers made a local
    // occurrence fall back to "the first spelling", so a warning the second
    // spelling owned moved onto earlier prose - and deleting that prose enabled
    // a Send whose real reference was still there.
    const activeRowAnchors: CompositionImageAnchor[] =
      rowId === null
        ? ownsDraft()
          ? readComposerDraftImageAnchors(ref)
          : []
        : sameRowAsRecovered
          ? (rowCommittedAnchors(recovered)?.anchors ?? [])
          : // The ACTIVE row's statement about its own images, through the ONE
            // derivation rule. Reading the side record directly is what let a
            // failed write outlive the composition it described: the row had
            // committed "nothing is missing" and the stale record re-stamped
            // the retired reference onto this revision.
            (compositionAnchorsForRecord(
              recoveryBaseRecord?.clientMutationId === rowId
                ? recoveryBaseRecord
                : { compositionRevision: recoveryOwnerLeaseRef.current?.compositionRevision },
              rowId,
            ) ?? []);
    // The local draft's anchors are OCCURRENCES in the merged text's PREFIX, so
    // their offsets still name the same spellings and must not be reduced to
    // numbers here: a number resolves to the first spelling, so local prose
    // would take over a warning the second spelling owned.
    const recorded: CompositionImageAnchor[] = activeRowAnchors;
    // Both sides of the merge keep their surviving tags. The active row is one
    // side; the row that just arrived is the other, and it is the only record
    // that can describe an anchor the incoming text brought with it.
    // A marker the local draft's record said had bytes, when a tile THIS
    // source still holds carries bytes under that number, is not a missing
    // reference at all. The record states the definition itself - an anchor is
    // a reference with NO bytes - but this filter only checked the row's own
    // attachments, so a staged tile's stored marker stayed an "anchor" after
    // the merge renumbered the tile out from under it. That turns the row's
    // literal "[image 1]" into a refusal the composition never earned.
    const backedMarkers = new Set(
      operations.items.filter((item) => item.data !== undefined).map((item) => item.marker),
    );
    // These are the exemptions the row will install, as OCCURRENCES in the
    // row's own text. Which spelling is meant matters: the row's record names
    // one, and a pane beside it may already spell the same number as prose.
    //
    // The incoming record's anchors carry offsets into that same text, so they
    // are used as they stand; everything else here is a number and resolves to
    // its first unclaimed spelling.
    const declaredAnchors: CompositionImageAnchor[] = (incomingRecord?.anchors ?? []).filter(
      (anchor) => spoken.has(anchor.marker) && !restoredMarkers.has(anchor.marker),
    );
    // ONLY the row's own record can name an occurrence, and it names one in ITS
    // OWN text, so those are resolved here and translated by the caller.
    //
    // Everything else stays OFFSETLESS on purpose. The live tags belong to THIS
    // composer's text, which is the merge's prefix, and `recorded`/`declared`
    // are numbers by construction; resolving any of them against the row's text
    // would look for a spelling that is not there and drop the tag entirely.
    // An offsetless anchor is resolved when it is installed, against the text it
    // is installed into.
    const namedOccurrences = resolveAnchors(declaredAnchors, recovered.text);
    // The offsets already named, by OCCURRENCE. Two spellings of one marker are
    // two references - that is the whole reason an offset is carried - so a
    // marker-keyed set here would drop a second genuine missing reference and
    // leave its spelling shipping as prose. What must NOT double is one
    // SPELLING: the row's record can name the same marker once as an occurrence
    // and once in the older number-only shape, and both resolve to the same
    // first spelling only when that spelling is still unclaimed.
    // One missing image per marker on the NUMBER-ONLY paths below. A record can
    // name one marker twice - once as an occurrence, once in the older
    // number-only shape - and binding both would put TWO spellings behind one
    // missing image: deleting the reference retires one and the other goes on
    // refusing a Send the person had repaired. Two GENUINE references of one
    // marker are different: the record names each with its own offset, they
    // come through `namedOccurrences` as two distinct occurrences, and the set
    // below is only what the number-only fallbacks may not re-add.
    const claimed = new Set(namedOccurrences.map((anchor) => anchor.marker));
    // The RESOLVED OCCURRENCES already taken, as opposed to the marker numbers
    // held by the number-only paths below.
    //
    // The local draft's side was filtered by `claimed`, which is a set of
    // NUMBERS - so when both sides held a genuine missing `[image 1]`, the
    // incoming row's occurrence claimed the number and the local side's real
    // reference was dropped outright. The merged text then spelled a reference
    // nothing warned about: deleting the incoming one enabled Send while the
    // local missing reference was still in the text.
    //
    // Each side resolves in its OWN text, both are mapped into the merged text,
    // and what must not double is the resulting SPELLING. Two spellings of one
    // marker are two references and both stay; one spelling reached twice is
    // one reference and is committed once. This is the same identity rule the
    // durable row and the editor share - one set, not two that can disagree.
    const takenOccurrences = new Set(namedOccurrences.map((anchor) => anchorKey(anchor)));
    const anchors: CompositionImageAnchor[] = [
      ...namedOccurrences,
      // The live tags are OCCURRENCES in this pane's text, which is the merged
      // text's prefix, so their offsets still name the same spellings.
      // Filtering them by NUMBER is what dropped a local missing reference when
      // the incoming row happened to use the same number: one warning for two
      // references, and the local one shipped as prose.
      // The active row's own anchors, translated through the merge exactly as
      // the incoming row's are. Their offsets are positions in THIS pane's text,
      // which is the merged text's prefix, so they still name the same
      // occurrences - but the merged text may be longer, and the writers below
      // resolve offsets against the merged text.
      ...resolveAnchors(recorded, textRef.current).filter(
        (anchor) =>
          // Its own spelling is not already held by the incoming side. A
          // DIFFERENT spelling of the same marker is a second reference and is
          // kept: suppressing it by number is what lost the local warning.
          !takenOccurrences.has(anchorKey(anchor)) &&
          spoken.has(anchor.marker) &&
          !restoredMarkers.has(anchor.marker) &&
          !backedMarkers.has(anchor.marker),
      ),
      ...[...new Set(declared)]
        .filter((marker) => !claimed.has(marker) && spoken.has(marker) && !restoredMarkers.has(marker))
        .map((marker) => ({ marker })),
    ];
    // ONE set, deduplicated by resulting occurrence, used for both the editor
    // and the durable row. Two spellings of one marker are two references and
    // both survive; one spelling reached by two routes is one reference and is
    // committed once. `dedupeAnchors` keys an offsetless anchor as `marker@*`,
    // which agrees only with another offsetless entry of the same number - so
    // a resolved occurrence and a bare number of the same marker stay distinct,
    // and both are resolved against the text when installed.
    const resolvedAnchors = dedupeAnchors(anchors);
    // The live tags are a THIRD coordinate system: offsets in this composer's
    // own text, which is the merged text's PREFIX. Everything above is in the
    // incoming ROW's text and is translated by the caller. Mixing the two in
    // one list made the translation drop the local anchor - offset 5 of this
    // pane's text is not offset 5 of the row's - so a merge that should have
    // carried two references committed one and shipped the other as prose.
    // NOT deduplicated here, and that is the point.
    //
    // The sets above are keyed in the ROW's coordinates. A local anchor lives in
    // THIS composer's text, which is the merged text's PREFIX - so the same key
    // means a different place. When both sides began with a missing `[image 1]`,
    // both offsets were 0 and the row's entry claimed the local one outright:
    // the merge dropped the local warning, and deleting the incoming reference
    // then enabled a Send whose local missing reference was still in the text.
    // "Same marker at the same offset" only answers "same reference" once both
    // offsets are positions in the SAME text.
    //
    // The caller that translates the row's anchors into the merged text is the
    // one that knows the final offsets, so it deduplicates the union there, by
    // resulting occurrence. Each side is still filtered against the text it was
    // resolved in, which is what keeps a dead spelling out.
    const localAnchors = localAnchorsIn(textRef.current);
    // Bytes the row restored that its own text does not name stay sendable:
    // the row carried them, and a resend consumes the row.
    //
    // The ids come from the RECORD's own attachment list, not from matching
    // numbers against the settled tiles. The merge gives a recovered image a
    // new number whenever it collides with the current draft's numbering, and
    // it carries each attachment object through by spreading it, so the id
    // minted when the record was decoded is the one the settled tile answers
    // to. Looking up tiles by the row's original number would describe the
    // draft's own image instead and leave the recovered bytes excluded from
    // the very send that consumes the row.
    //
    // A merge KEEPS images that were already staged, so an exemption recorded
    // for one of them survives by identity. Replacing the set instead would
    // drop the first row's markerless image the moment a second row is edited
    // into the same composer: the image is still staged, still exempt, and
    // would then be omitted from the resend that consumes the row.
    //
    // WHICH restored images are exempt is a question about the row, not about
    // its prose. When the record could not say which references its bytes
    // belonged to, its text may spell an image's number as a literal the merge
    // then renumbers away - so matching the row's own numbers against the
    // prose would miss the tile and autosave would drop real bytes. The row's
    // decoded status answers instead: every image it restored stays eligible.
    const referencesUnavailable = recoveryImageReferencesUnavailable(recovered);
    // A row that kept a tile whose reference the person deleted holds real
    // bytes it must NOT send. Those bytes have to survive a reload - the tile
    // is still on screen - so the row persists them and records here that they
    // carry no exemption. Withholding it is what stops the reopen from
    // treating a released tile as the markerless image a row is allowed to
    // resend.
    // Bytes the row kept but must NOT resend, answered by ATTACHMENT IDENTITY.
    //
    // The ids must be the ones the RECOVERED TILES carry now, not the ones the
    // record stored. Decoding a row mints a fresh id per tile, so reading
    // `record.tiles[].id` here compares two different lifetimes of the same
    // image and never matches - which silently turned every released tile back
    // into an eligible one on reopen, and let Send carry an image whose
    // reference the person had deleted. The decode already paired each stored
    // eligibility with the id it minted, so this is a lookup, not a comparison.
    const releasedIds = new Set(
      [...recoveryTileEligibility(recovered)].filter(([, eligible]) => !eligible).map(([id]) => id),
    );
    const live = new Set(operations.items.map((item) => item.id));
    const prior = state.getState();
    const restored = [
      ...new Set([
        ...prior.restoredImageIds.filter((id) => live.has(id)),
        ...recovered.attachments
          .filter((item) => !releasedIds.has(item.id) && (referencesUnavailable || !spoken.has(item.marker)))
          .map((item) => item.id),
      ]),
    ].sort();
    // A retained tile stays foreign while it is staged, whatever number the
    // merge gives it. Retiring on absence is what clears it, not adoption:
    // adopting a row must not silently re-enable bytes that belong to the
    // composition it replaced.
    const foreign = prior.foreignImageIds.filter((id) => live.has(id));
    return { restored, foreign, anchors: resolvedAnchors, localAnchors };
  }

  function adoptRecoveredImageFacts(recovered: RecoveredComposerDraft, incomingRowId: string | null): void {
    const facts = recoveredImageFacts(recovered, incomingRowId);
    adoptUnbackedImageMarkers(facts.anchors);
    state.setState({ restoredImageIds: facts.restored, foreignImageIds: facts.foreign });
  }

  // The mount read is an adoption too: this source starts with an empty
  // attachment store, so a draft already on disk was written by someone
  // else's composer (or by this one before a reload, which lost its bytes the
  // same way). Only the markers its record names as having had image bytes
  // count - a marker the record does not name was literal prose.
  //
  // A local draft record states OCCURRENCES, because the writer now records the
  // spelling its warning was about. An older record states bare numbers, and
  // the reader turns each into an offsetless anchor that adopts its first
  // spelling - the reading those records always had.
  adoptUnbackedImageMarkers(readComposerDraftImageAnchors(ref));

  function queueRecoveryPersistence(
    clientMutationId: string,
    nextText: string,
    nextAttachments: InputAttachment[],
    nextSkillNames: readonly string[],
    nextCommandNames: readonly string[] = selectionMetaRef.current.commandNames,
    nextMentions: readonly ComposerMention[] | undefined = selectionMetaRef.current.mentions,
    nextComposerText?: string,
  ): Promise<boolean> {
    if (disposed) return Promise.resolve(false);
    // The record must describe what a Send from this composer would carry, and
    // nothing else, using the ONE selection the send path uses.
    //
    // Send eligibility and durability are DIFFERENT questions, and answering
    // both with "is it sendable" loses the person's image.
    //
    // A tile whose reference they deleted while editing is not sendable, but
    // they never removed the tile: it is still on screen, and a reload must
    // bring it back. So the row keeps the bytes of every tile this composition
    // OWNS - foreign is the one exclusion, because a tile retained from the
    // composition a row replaced is not this row's work and must never enter
    // its durable payload.
    //
    // The exemption problem the old filter solved is real, though: a restored
    // attachment is sendable by default (that is what lets a row whose text
    // never spelled its image still resend it), so persisting a released tile
    // unchanged would make the reopen send bytes the prose never asked for.
    // The row therefore also records which of its attachments are NOT exempt,
    // and adoption withholds the exemption for exactly those.
    // Eligibility is NOT decided here any more: the composition record carries
    // it per tile, keyed by attachment id, so this write is only about which
    // tiles the row owns. Deriving an ineligible list from marker numbers was
    // the defect - the numbers move, and a number is not an owner.
    // TWO lists, from one snapshot, exactly as activation builds them.
    //
    // `outgoingAttachments` is what a Send would put on the wire: the sendable
    // selection, from the ONE selection function. A tile whose reference the
    // person deleted is not in it.
    //
    // `ownedAttachments` is the composition this row keeps, that tile included,
    // so its bytes survive a reload. Writing the owned tiles into the payload
    // as well was the defect: a reopen whose eligibility record could not be
    // written (full quota, or a refusal restored without one) found those bytes
    // in `payload.input` and no exclusion record to say otherwise, so the tile
    // took the markerless-image exemption and the next Send carried an image
    // the person had excluded.
    //
    // Deliberately NOT narrowed: the exemption itself is untouched. It is
    // granted only where the payload provably has no reference, and this split
    // is what keeps a released tile out of the payload while preserving it.
    //
    // Both views come from the caller's ONE list, resolved by attachment
    // IDENTITY - the wire shape carries no id, so they are matched back against
    // the owned tiles on their bytes, and a number alone would let a foreign
    // tile and an owned one swap places. Sendability is then decided by the same
    // selection function the Send path uses.
    const outgoingAttachments = sendableCompositionAttachments(nextAttachments, nextText);
    // The payload's images are paired with the durable list BY INDEX
    // (`recoverMutationComposition`), so the sendable tiles must come first, in
    // the order the payload carries them, and the excluded ones may only follow.
    const excludedAttachments = ownedCompositionAttachments(nextAttachments).filter(
      (candidate) => !outgoingAttachments.includes(candidate),
    );
    nextAttachments = [...outgoingAttachments, ...excludedAttachments];
    // The tiles whose facts this save describes, captured HERE with the text
    // and before the first await. The record and the durable row are written to
    // two stores, so a live store read after the await can describe an edit
    // that the row this write commits never had - facts stamped for revision N
    // but built from the store as it stands at revision N+1.
    const committedTilesForRecord = operations.items;
    // The live anchors, captured with the text for the same reason - and for a
    // sharper one: this save's text may have RETIRED a tag. The person deletes
    // a missing reference and types the same spelling as prose while an image is
    // decoding; the edit retires the anchor immediately, but this save is
    // paused behind that decode. Reading the row's stored anchors when the write
    // finally lands would stamp the retired tag onto the new revision, and a
    // reopen would block a Send the person had already repaired. An EMPTY set is
    // the answer here and is written as such - "nothing is reserved".
    //
    // The OCCURRENCE travels too. Collapsing each anchor back to its number
    // discarded the one fact that says which spelling the warning was about:
    // the live editor can hold the right occurrence while the record keeps a
    // bare number, and the next reopen attaches the warning to the first
    // spelling instead. Deleting that literal spelling then enabled Send while
    // the real missing reference was still in the text. An offset is kept only
    // while it still spells its marker in the text being committed; otherwise
    // the anchor falls back to its number, which is the reading every older
    // record gets.
    const committedAnchorsForRecord: CompositionImageAnchor[] = adoptedAnchors.map(({ marker, offset }) =>
      nextText.slice(offset, offset + markerText(marker).length) === markerText(marker)
        ? { marker, offset }
        : { marker },
    );
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
    const localDraftRevisionId =
      ownsDraft() && currentDraftRevisionId !== undefined && currentDraftRevisionId === readComposerDraftRevisionId(ref)
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
          outgoingAttachments,
          nextSkillNames,
          nextCommandNames,
          nextMentions,
          [],
          localDraftRevisionId,
          lease.compositionRevision,
          { [clientMutationId]: lease.compositionRevision },
          compositionOwnerId,
          nextComposerText,
          nextAttachments,
          // The anchors this text carries, committed in the SAME write. Without
          // them the row's anchors were only as fresh as the last merge, so an
          // edit that retired the last missing reference left the old set
          // standing and a reopen blocked a Send the person had repaired - or,
          // with the localStorage write failed, restored a reference the merge
          // had already removed. An EMPTY set is written as such: "nothing is
          // reserved" is an answer, and skipping the write leaves the previous
          // answer standing.
          committedAnchorsForRecord,
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
          // The record commits with EVERY successful edit, not only the first
          // handoff. Gating it on local-draft ownership meant a later edit that
          // deleted a reference never retired it: the live composer dropped the
          // tag, the row kept it, and a reopen blocked the same spelling typed
          // as prose. An empty answer is a real answer and is written too.
          //
          // Ownership, not eligibility, decides what goes in: the released
          // bytes are part of the composition the person can see.
          //
        }
        // The facts are stamped with the revision this write just created, and
        // they are written OUTSIDE the pane-lifetime gate above. The row and
        // the record are two stores: a commit that reached IndexedDB is durable
        // whether or not this pane is still mounted, and skipping the record
        // because the pane closed left a committed payload whose exemption no
        // reader could find. A reopen then withheld the image's eligibility
        // while still restoring its bytes, so Send silently omitted an image
        // the person kept. Both stores name the same revision: the row's.
        writeCompositionImageRecord(
          clientMutationId,
          compositionImageRecord(
            nextText,
            committedTilesForRecord,
            committedAnchorsForRecord,
            undefined,
            recoveryCompositionRevision(updated),
          ),
        );
        if (
          !disposed &&
          recoveryOwnsLocalDraftRef.current &&
          recoveryWriteVersionRef.current === version &&
          readDraftRevision(ref) === draftRevision &&
          (localDraftRevisionId === undefined || readComposerDraftRevisionId(ref) === localDraftRevisionId) &&
          currentRecoveryId() === clientMutationId
        ) {
          recoveryOwnsLocalDraftRef.current = false;
          // The tags are already filed by the commit block above, which runs
          // for EVERY committed edit and not only this first handoff. Writing
          // them twice would leave this branch able to disagree with it.
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
    // A stale-lease preservation is still a durable write of a COMPOSITION, so
    // it carries every tile this composition OWNS - the released bytes
    // included. Filtering it to the sendable subset was the same mistake the
    // resend made: it writes "what may ship" over "what the person can see",
    // and the tile they never removed is gone from the row that replaces it.
    //
    // Ownership is by ID. A marker number is not an owner: the store's tiles
    // and the composition's numbering can differ after a merge, and a foreign
    // tile may be the one holding a given number.
    //
    // ONE snapshot, taken here before the first await, and used for both the
    // durable write and the record: text, owned tiles and anchors describe the
    // SAME composition or they describe nothing. Reading the anchors back off
    // `clientMutationId`'s record was the defect - the peer that advanced this
    // row replaced that record, so the fork inherited the peer's anchors while
    // carrying its own text, and a reopen then answered "is this reference a
    // missing image" with the other draft's answer. This composer's own tags
    // are what it actually adopted; the row's stored copy is a second opinion
    // that can now belong to someone else.
    const preservedText = textRef.current;
    // ONE list, two views, resolved by identity - the same split ordinary
    // recovery autosave uses. The payload is the request the preserved row
    // would send: a released tile is not in it. The durable list is the
    // composition: every owned tile, released ones included, so their bytes
    // survive.
    //
    // Writing the owned tiles into the payload as well was the defect. A
    // reopen whose eligibility record could not be written (full quota, and the
    // panel closing before the follow-up autosave) found those bytes in
    // `payload.input`, read them as still-sent, and the next Send carried an
    // image whose reference the person had deleted.
    const preservedOwnedTiles = ownedCompositionAttachments(operations.toInputAttachments());
    const preservedSendable = sendableCompositionAttachments(preservedOwnedTiles, preservedText);
    // THE INVARIANT LIVES IN THE WRITERS, AND THIS IS ONE OF THEM.
    //
    // Recovery pairs a payload image with its durable metadata BY INDEX, so a
    // payload holding only the second image while the durable list still starts
    // with the first makes a reopen assign the second image's bytes to the first
    // marker, restore the second again as an excluded tile, and lose the first
    // image outright. The reader is not going to be taught to pair another way;
    // two lists it must zip are one list to it.
    //
    // `queueRecoveryPersistence` applies the same normalisation, but ONLY to its
    // own local argument (`nextAttachments`) before writing its row - it does
    // not reorder the attachment store. The store keeps staging order, so in
    // principle a composition whose earlier marker lost its reference while a
    // later one kept it can reach THIS function as [excluded, sendable], and
    // preserve must not trust incoming order. That is what the next two lines
    // prevent.
    //
    // MEASURED CAVEAT, stated rather than glossed: I could not build a fixture
    // that makes these two lines change the persisted row. Two attempts - one
    // staging [excluded, sendable] and one advancing the row out from under the
    // pane - both produced a correctly ordered row with the normalisation
    // REMOVED, so on the paths a test can drive, some earlier writer has already
    // ordered the list. Either this is hardening for an ordering that only an
    // untested path produces, or the normalisation is duplicated upstream. This
    // comment does not settle which. The two lines are kept because the reader's
    // by-index pairing makes the wrong order silently destructive rather than
    // merely wrong, and they cost nothing when the order is already right.
    //
    // Put the other way, for the next caller: a writer that reorders a durable
    // list must reorder the payload it commits with it. This function is where
    // that rule would otherwise be discovered, and it is stated here rather than
    // encoded once in a shared helper because the two writers do not share a
    // code path - autosave has the caller's list and this one has the store.
    const preservedOwned = [
      ...preservedSendable,
      ...preservedOwnedTiles.filter((candidate) => !preservedSendable.includes(candidate)),
    ];
    // The tiles the record below describes, captured with the text and the
    // bytes it describes them for.
    const preservedTiles = operations.items;
    // The live anchors WITH their occurrences, validated against the text being
    // preserved. Collapsing these to bare numbers discarded which of two
    // identical spellings the warning was about, so a fork whose second
    // spelling was the missing one reopened warning about the first. An offset
    // is kept only while it still spells its marker in this text, which is the
    // same rule autosave applies, so the two writers cannot disagree.
    const priorAnchors: CompositionImageAnchor[] = adoptedAnchors.map(({ marker, offset }) =>
      preservedText.slice(offset, offset + markerText(marker).length) === markerText(marker)
        ? { marker, offset }
        : { marker },
    );
    try {
      const preserved = await preserveRecoveryPendingTurn(
        base,
        ref,
        preservedText,
        // The payload: what this composition would send.
        preservedSendable,
        skillNamesRef.current,
        selectionMetaRef.current.commandNames,
        selectionMetaRef.current.mentions,
        compositionOwnerId,
        localDraftRevisionId,
        // Both sides of the comparison come from the ONE record reader, so a
        // row is reused only when it already says the same thing about its
        // images as this composition does.
        storedImageFacts,
        compositionImageRecordSnapshot(preservedText, preservedTiles, priorAnchors),
        // The composition: every owned tile, released ones included, sendable
        // first so the payload's images keep pairing by index.
        preservedOwned,
        // ...and its anchors, committed with the text they describe. Omitting
        // this was the one statement a preserved row could not make about
        // itself, so a reader fell back to a side record describing the row
        // this one replaced. `priorAnchors` is captured above with the text, so
        // an empty list here is a real answer: this composition reserves
        // nothing, having retired its last reference.
        priorAnchors,
      );
      // The preservation mints a NEW row, and the record travels with it: it is
      // part of the composition, not of the old row. Keying it to the row it
      // replaced would leave the replacement with no anchors and no per-tile
      // eligibility, so a reload of the preserved composition would send a
      // missing image as prose and re-admit a released tile.
      //
      // ...but only when it minted one. Storage returns the EXISTING row when
      // the composition it was asked to preserve is identical to what that row
      // already holds - and that row may be the peer's, whose facts describe
      // the peer's composition. Writing this pane's tags over them is how a
      // stale tag came to block a peer draft the person had already fixed:
      // text and tiles matched, so the row was reused, and the tags - the one
      // part of the composition that did NOT match - were then overwritten
      // anyway. Identical composition means identical facts, so leaving them is
      // correct in the only case this branch can fire.
      if (preserved !== undefined && preserved.clientMutationId !== clientMutationId) {
        writeCompositionImageRecord(
          preserved.clientMutationId,
          // The CAPTURED tiles, not the live store. A pane closed while this
          // write was in flight has already reset the store, so reading it here
          // answers "this composition owned nothing": the fork keeps the bytes
          // and gets no eligibility entry for them, and a reopen then grants
          // the default markerless-image exemption to a tile whose reference
          // the person had removed. Text, tiles and anchors are one snapshot,
          // taken before the await, or they describe different compositions.
          compositionImageRecord(preservedText, preservedTiles, priorAnchors),
        );
      }
      return preserved;
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
          //
          // The row's excluded tiles come from the row's own durable bytes, read
          // here - inside the operation that already awaits storage - and handed
          // to the decoder as a value. A restored image's exclusion facts and its
          // bytes are therefore read from the same record and the same revision.
          const recovered = recoveryComposerDraft(record, await excludedRecoveryImages(record));
          // The pending check above ran BEFORE the Blob read, and the person can
          // start an upload while it is in flight. Their new image has no bytes
          // yet and no reference in any text this merge knows about, so
          // committing now writes a marker that is neither backed nor declared
          // missing - and closing before the encode lands ships it as prose.
          // The row stays available for explicit Edit.
          if (disposed || operations.hasPending) return false;
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
            // The merge's current side is what this composition would SEND,
            // not every staged tile. A tile retained from the composition a
            // peer draft replaced is not part of this draft: including it lets
            // the merge renumber it around the peer's literal marker and
            // rewrite that prose. It stays staged in the pane - only this
            // merge and its durable write leave it out.
            const facts = state.getState();
            const mergeableIds = new Set(
              sendableAttachments(operations.items, textRef.current, {
                foreign: facts.foreignImageIds,
                restored: facts.restoredImageIds,
              }).map((item) => item.id),
            );
            return mergeRecoveryComposerDraft(
              skipLocalDraft ? "" : textRef.current,
              skipLocalDraft ? [] : operations.items.filter((item) => mergeableIds.has(item.id)),
              recovered,
              skipLocalDraft ? [] : skillNamesRef.current,
              skipLocalDraft ? [] : selectionMetaRef.current.commandNames,
              skipLocalDraft ? undefined : selectionMetaRef.current.mentions,
            );
          };
          const merged = merge();
          // The durable composition carries what a Send from this composer
          // would carry, using the one selection the request uses. A tile
          // retained from the composition this row replaced is staged here but
          // is not this row's image: committing it would make it the row's own
          // on the next reopen, so a close during this write would durably
          // reintroduce an image the person never attached to this draft.
          const durableAttachments = (draft: RecoveredComposerDraft): InputAttachment[] => {
            const facts = state.getState();
            // Decide from the MERGED composition's own attachments and their
            // final markers, never from the pre-merge store. A fresh composer
            // that opens a row carrying an image has an empty store, so asking
            // the store which markers are sendable answers "none" and the row
            // is committed without the bytes it just restored - the image shows
            // on screen and is gone on the next reopen.
            //
            // The tile to exclude is the one this row did not bring: a tile
            // retained from the replaced composition. That is a property of the
            // attachment, so identity answers it.
            const foreign = new Set(facts.foreignImageIds);
            return draft.attachments.flatMap((item) =>
              item.data === undefined || foreign.has(item.id)
                ? []
                : [{ marker: item.marker, name: item.name, mediaType: item.mediaType, data: item.data }],
            );
          };
          // The composition this transaction is about to make durable: the
          // merge's tiles PLUS every tile this composition owns that the merge
          // could not see. Computed here, before the write, because the write
          // has to carry them - appending them to the editor afterwards leaves
          // a window where the row holds the merged text and not the tiles, and
          // closing the pane in that window loses bytes the person still had.
          //
          // Their numbers come from the same allocator as ingest: a retained
          // tile may not take a number the final text spells or another tile
          // bears, because those words are then a reference to it.
          const mergedIdsBefore = new Set(merged.attachments.map((item) => item.id));
          const reservedBefore = new Set<number>([
            ...markerNumbersIn(merged.text),
            ...merged.attachments.map((item) => item.marker),
          ]);
          const retainedBefore = operations.items
            .filter(
              (item) =>
                !mergedIdsBefore.has(item.id) &&
                item.data !== undefined &&
                !state.getState().foreignImageIds.includes(item.id),
            )
            .map((item) => {
              let marker = 1;
              while (reservedBefore.has(marker)) marker += 1;
              reservedBefore.add(marker);
              return { ...item, marker };
            });
          const committedTiles = [...merged.attachments, ...retainedBefore];
          const ownerId = currentRecoveryId() ?? record.clientMutationId;
          const ownerLease = recoveryOwnerLeaseRef.current;
          const ownerRecord = ownerId === record.clientMutationId ? record : undefined;
          const expectedCompositionRevision = ownerRecord
            ? recoveryCompositionRevision(ownerRecord)
            : ownerLease?.compositionRevision;
          if (expectedCompositionRevision === undefined) return false;
          const replacementEpoch = recoveryReplacementEpochRef.current;
          cancelCleanupRetry();
          // The exemptions this merge installs, captured HERE, before the write
          // is awaited, while this source still owns the tiles.
          //
          // Reading them after the write was the defect: closing the pane while
          // IndexedDB is pending resets the live store, so the post-commit
          // builder saw no tiles and filed an eligible markerless image as
          // ineligible. The reopen then showed the image and the next Send
          // silently omitted it. These facts belong to the composition being
          // committed, so they are read from that composition, not from
          // whatever the store holds when the write returns.
          // Translated ONCE, here, into the merged text's coordinates. The
          // facts are resolved in the row's own text, but every reader below
          // works on the merged text: the adoption installs these tags into the
          // live editor, and the record files them for the next reopen. If the
          // two disagreed about which spelling an anchor named, the tag would
          // land on whichever `[image N]` came first in the combined text -
          // the pane's literal prose - and deleting that prose would clear a
          // warning about the row's real missing reference.
          const adoptedInRowText = recoveredImageFacts(recovered, record.clientMutationId, ownerId);
          const adopted = {
            ...adoptedInRowText,
            // The incoming row's anchors translated out of ITS text, and this
            // pane's own live tags - already in the merged prefix, never
            // translated - unioned and deduplicated in the ONE coordinate
            // system they now share.
            anchors: anchorsIntoOneComposition(
              adoptedInRowText.anchors,
              adoptedInRowText.localAnchors,
              recovered.text,
              merged.text,
            ),
          };
          // TWO LISTS, because they answer two questions.
          //
          // `payload.input` is what a Send would put on the wire: the sendable
          // selection, from the ONE selection function. A released tile - its
          // reference deleted, the tile kept - is not in it.
          //
          // `attachments` is the composition the row OWNS, the released tile
          // included, so its bytes survive a reload. Writing the owned tiles
          // into the payload as well was the defect: a reopen then read those
          // bytes as the request's own images, and when the image-facts write
          // failed (full quota) there was no exclusion record to say otherwise,
          // so the tile got the markerless-image exemption and the next Send
          // carried the image the person had excluded.
          //
          // The payload's images are paired with this list BY INDEX
          // (`recoverMutationComposition`), so the sendable tiles must come
          // first, in the order the payload carries them, and the excluded ones
          // may only follow.
          const sendableIds = new Set(
            sendableAttachments(committedTiles, merged.text, {
              foreign: adopted.foreign,
              restored: adopted.restored,
            }).map((item) => item.id),
          );
          const foreignBefore = new Set(state.getState().foreignImageIds);
          const wireEntries = committedTiles.flatMap((item) =>
            item.data === undefined || foreignBefore.has(item.id)
              ? []
              : [
                  {
                    id: item.id,
                    wire: { marker: item.marker, name: item.name, mediaType: item.mediaType, data: item.data },
                  },
                ],
          );
          const outgoingAttachments = wireEntries
            .filter((entry) => sendableIds.has(entry.id))
            .map((entry) => entry.wire);
          const ownedAttachments = [
            ...outgoingAttachments,
            ...wireEntries.filter((entry) => !sendableIds.has(entry.id)).map((entry) => entry.wire),
          ];
          // The anchors the merged composition carries, resolved HERE, once,
          // because they are committed by the same transaction that commits the
          // text. Reading them after the write left a window a closed pane or a
          // full localStorage quota could fall into: the row held a merged text
          // spelling `[image 1]` with no fact saying that reference had no
          // bytes, and a reopen shipped it as prose.
          // Deduplicated by the same identity rule the live record uses, so a
          // merge that re-derives an anchor it already had commits it once.
          // The INCOMING row's own committed copy, read from the decoded record
          // rather than from localStorage. `undefined` means the row never
          // stated anchors; an empty array means it stated "none", a fact.
          const incomingCommittedAnchors = rowCommittedAnchors(recovered)?.anchors;
          // The OWNER row's statement about its own images, read through the
          // SAME derivation rule the incoming side uses. The side record was
          // read here directly, so a row that committed the person's repair
          // (an EMPTY list: "this text references no missing image") lost to a
          // stale localStorage record left behind by a failed write, and the
          // merge re-stamped the retired reference onto the new revision.
          const ownerCommittedAnchors =
            ownerId === record.clientMutationId
              ? []
              : (compositionAnchorsForRecord(
                  {
                    // The owner row when this pane holds it, and otherwise only
                    // its lease: `compositionRevision` is what the record would
                    // have stated, so the rule's revision check still applies.
                    ...(recoveryBaseRecord?.clientMutationId === ownerId ? recoveryBaseRecord : {}),
                    compositionRevision: expectedCompositionRevision,
                  },
                  ownerId,
                ) ?? []);
          const mergedAnchors = dedupeAnchors([
            // The owner's anchors are already in the merged text's coordinates:
            // the merge's own text is the PREFIX of the merged text, so its
            // offsets still spell their markers at the same positions.
            //
            // Read through the ROW's own committed copy, not the side record.
            // The side record is a separate localStorage write that can fail or
            // go stale while the IndexedDB commit succeeds, and this merge then
            // copied the stale anchors straight into the replacement row: the
            // person replaced a missing reference with same-number prose, the
            // row committed an EMPTY list, the side write failed, and reopening
            // the merged revision blocked a Send on literal prose. An empty
            // committed list is a statement - "this text references no missing
            // image" - and it is honored as one. `compositionAnchorsForRevision`
            // is the one place that precedence lives; re-deriving it here is how
            // a fifth site came to answer the same question differently.
            //
            // The owner row is the one this merge commits INTO. When it is the
            // incoming row itself, `incomingCommittedAnchors` is already its
            // statement; otherwise the owner's own recorded facts are read the
            // same way as the incoming side below.
            ...(ownerId === record.clientMutationId ? [] : ownerCommittedAnchors),
            // The incoming row's anchors were resolved in ITS text, which is
            // not the merged text and not a prefix of it. Copying them raw filed
            // offsets into a document they never described, so a warning could
            // land on whatever spelling happened to sit at that position in the
            // combined text. Translate them the same way adoption does.
            ...anchorsIntoMergedText(
              // Same rule for the incoming row: its own committed copy first,
              // and only then a side record that names the revision it claims.
              incomingCommittedAnchors ?? readCompositionImageRecord(record.clientMutationId)?.anchors ?? [],
              recovered.text,
              merged.text,
            ),
            // The anchors this merge itself creates. They were resolved in
            // the row's OWN text, so they are moved into the merged text
            // here - by occurrence, not by arithmetic, because the merge
            // renumbers the row's tile-backed markers and a wider number
            // shifts every offset after it.
            ...adopted.anchors,
          ]);
          const updated = await updateRecoveryPendingTurn(
            ownerId,
            ref,
            merged.text,
            outgoingAttachments,
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
            undefined,
            ownedAttachments,
            mergedAnchors,
          );
          if (!updated) return false;
          // The transaction has ALREADY merged this text and consumed the
          // incoming row. Everything below is the live editor catching up, and
          // none of it may decide whether the facts get recorded: a pane closed
          // right here would otherwise leave the durable row holding a merged
          // text with no record of its anchors, so a reopen would pass an
          // incoming missing-image reference through as literal prose.
          //
          // The record names the exact composition the transaction committed,
          // built from those tiles rather than from the store, which is what
          // makes the write describe the revision it belongs to.
          // The exemptions adoption will install, computed here so the record
          // and the editor cannot disagree about who is shippable. Adoption
          // below installs exactly these.
          writeCompositionImageRecord(
            ownerId,
            compositionImageRecord(
              merged.text,
              committedTiles,
              // The SAME anchors the transaction committed, from the same
              // snapshot. Two computations of one fact is how they drift.
              mergedAnchors,
              // `adopted` supplies the exemptions this merge installs: an incoming missing reference is an anchor of the
              // composition being committed, and the row must carry it in the
              // same transaction that merges its text. Without it a close
              // between the commit and the live editor catching up leaves the
              // row holding "[image 1]" as prose.
              adopted,
              // The record describes the revision this write just created.
              recoveryCompositionRevision(updated),
            ),
          );
          if (disposed || recoveryReplacementEpochRef.current !== replacementEpoch) return false;
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
          // The merge's attachments are this composition's; tiles it excluded
          // are the person's other work, staged in this pane and not sent with
          // this row. Replacing the store with the merge alone would delete
          // them - the high's "excluded from the send, destroyed by the
          // cleanup" in its adoption form. Append them instead so they stay
          // staged.
          //
          // They cannot simply keep their numbers. The merge never saw them, so
          // it may have handed a recovered image the number an excluded tile
          // still bears; two tiles on one number makes the reference ambiguous
          // and the marker-keyed payload and removal paths then act on both.
          // Renumber the retained tiles above everything the merge used, which
          // is the same allocation the store does on ingest: identity carries
          // eligibility, the number is only the next free label.
          const mergedIds = new Set(latest.attachments.map((item) => item.id));
          // A retained tile takes a number the composition does not already
          // mean. Reserving only the numbers merged TILES hold is not enough:
          // the merged text can spell a marker with no tile behind it - the
          // person deleted the reference and left the tile, or a row's prose
          // says "[image 1]" literally - and handing a retained tile that
          // number makes the text's own words a reference to it. Send then
          // delivers an image the prose never asked for and strips the words
          // that were there. The shared allocator reserves text AND tiles,
          // which is the same rule ingest uses, so both agree on what is free.
          // Numbers this composition already means, read from the FINAL merged
          // composition - `latest`, not the snapshot the merge was built from.
          // Text typed while the recovery write was in flight is part of what
          // will be saved, and a number it spells is already taken even though
          // no tile bears it yet. Reading the earlier text would hand a
          // retained tile a number the person is looking at as prose.
          const taken = new Set<number>([
            ...markerNumbersIn(latest.text),
            ...latest.attachments.map((item) => item.marker),
          ]);
          const retained = operations.items
            .filter((item) => !mergedIds.has(item.id))
            .map((item) => {
              let marker = 1;
              while (taken.has(marker)) marker += 1;
              taken.add(marker);
              return { ...item, marker };
            });
          operations.replaceWithSettled([...latest.attachments, ...retained]);
          // The LIVE editor may have re-merged against typing that landed while
          // the write was in flight, so these tags are installed against
          // `latest.text` - the text they are about to sit in - rather than the
          // durable snapshot's offsets. Same helper, same rule: the occurrence,
          // never the number's first spelling.
          // Same split here: the row's anchors are translated, the live ones are
          // already in this text's coordinates.
          // Same one set, translated into the text the install actually lands
          // in. `latest` may have re-merged against typing that arrived during
          // the write, so the coordinates are its, not `merged`'s.
          // THE LIVE HALF IS MOVED, NOT CARRIED. `adoptedInRowText` resolved
          // this pane's own anchors against the text that existed BEFORE the
          // durable write, and its offsets are positions in `merged.text`.
          // Text typed during the write - a caption before a reference, say -
          // shifts them, and `latest` is the text that will be saved. Installing
          // the pre-write offsets tests each live anchor against text it is no
          // longer in, so an anchor that merely slid is dropped: the caption
          // survives in `latest.text` while the reference beside it loses its
          // warning, and the next Send carries it as prose.
          //
          // It cannot be re-derived from `adoptedAnchors` here either: the
          // `updateText(latest.text)` above is the MERGE's own write, and its
          // reconciliation treats the appended row as an edit to this pane's
          // anchors. The pre-write list is the only surviving statement of what
          // this pane held, so it is mapped forward by the same arithmetic
          // every other offset uses.
          //
          // The row's half cannot be re-derived at all: its offsets are
          // positions in the row's own text, which is why it is translated from
          // `recovered.text`. The two halves stay separate for exactly this
          // reason.
          adoptUnbackedImageMarkers(
            anchorsIntoOneComposition(
              adoptedInRowText.anchors,
              mapAnchorsThroughEdit(
                adoptedInRowText.localAnchors.filter(
                  (anchor): anchor is { marker: number; offset: number } => anchor.offset !== undefined,
                ),
                merged.text,
                latest.text,
              ),
              recovered.text,
              latest.text,
            ),
          );
          state.setState({ restoredImageIds: adopted.restored, foreignImageIds: adopted.foreign });
          // The record was written once, by the transaction above, from exactly
          // the text and tiles that transaction committed (`merged.text`,
          // `committedTiles`). There is deliberately no second write here.
          //
          // `latest` may have re-merged against typing or a paste that landed
          // while the transaction was in flight, and that composition is NOT
          // durable: IndexedDB holds `merged`. Writing a record for `latest`
          // would file per-tile markers and eligibility for a revision no row
          // carries - a reopen would then apply them to the committed tiles,
          // which is how an excluded tile gets a number the durable text never
          // gave it. The rebased facts are published by the write that commits
          // the rebased composition (`queueRecoveryPersistence`), which builds
          // its record from the same snapshot as its text.
          markRestore();
          committedRecoveryComposition = recoveryCompositionFingerprint({
            text: merged.text,
            attachments: durableAttachments(merged),
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
    if (draftUnchanged) ownedDraftRevisionRef.current = readDraftRevision(ref);
    const owns = ownsDraft();
    recoveryOwnsLocalDraftRef.current = false;
    recoveryWriteVersionRef.current += 1;
    cancelCleanupRetry();
    recoveryReplacementEpochRef.current += 1;
    setActiveRecoveryId(null);
    if (
      draftUnchanged &&
      textRef.current === submittedText &&
      sameSkillSelections(skillNamesRef.current, submittedSkillNames) &&
      sameSkillSelections(selectionMetaRef.current.commandNames, submittedCommandNames)
    ) {
      updateText("");
      updateSkillNames([]);
      updateSelectionMeta({ text: "", skillNames: [] });
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
    // Retire this press's tiles, but strip a reference only where the tile that
    // bears that number is not one this composition kept across a rebind. A
    // retained foreign tile's number in the current text belongs to the adopted
    // draft, whose prose and missing-image tag must survive.
    const foreign = new Set(state.getState().foreignImageIds);
    const ownedMarkers = new Set(operations.items.filter((item) => !foreign.has(item.id)).map((item) => item.marker));
    operations.clearSubmitted(markers, new Set([...markers].filter((marker) => ownedMarkers.has(marker))));
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
        sameSkillSelections(skillNamesRef.current, submittedSkillNames) &&
        sameSkillSelections(selectionMetaRef.current.commandNames, submittedCommandNames ?? [])
      ) {
        ownedDraftRevisionRef.current = readDraftRevision(ref);
        updateText("");
        updateSkillNames([]);
        updateSelectionMeta({ text: "", skillNames: [] });
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
    // A settled item can back a marker number this source adopted, and a
    // removal can take that backing away again.
    refreshUnbackedImageMarkers();
    // Both per-image flags belong to one tile for that tile's whole life, so
    // they retire when it leaves the store. Numbering restarts when the store
    // empties, and a new image must never inherit the eligibility of the tile
    // whose number it now bears.
    const live = new Set(operations.items.map((item) => item.id));
    const snapshot = state.getState();
    const foreign = snapshot.foreignImageIds.filter((id) => live.has(id));
    const restored = snapshot.restoredImageIds.filter((id) => live.has(id));
    if (foreign.length !== snapshot.foreignImageIds.length || restored.length !== snapshot.restoredImageIds.length)
      state.setState({ foreignImageIds: foreign, restoredImageIds: restored });
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
    markRestore,
    adoptRecoveredImageFacts,
    persistDraft,
    writeDraft,
    compositionOwnerId,
    ownsImageReference,
    runRecoveryOperation,
    flushRecoveryWrites: () => recoveryWrites.catch(() => undefined),
    tryAcquireRecoveryOperation,
    tryAcquireBuiltinOperation,
    queueRecoveryPersistence,
    // Which attachments this composition owns, in the wire shape a durable
    // write takes. The send path needs the same answer the durable writers use:
    // a resend replaces the row, so it must carry the tiles this composition
    // owns and the request did not ship.
    ownedAttachments: () => ownedCompositionAttachments(operations.toInputAttachments()),
    activateRecovery,
    bindEditor(nextEditor) {
      if (disposed) return () => {};
      if (!ownsDraft() && currentRecoveryId() === null) {
        const draft = readComposerDraftSnapshot(ref);
        // Whatever this source already staged predates the draft it is about
        // to adopt, so those bytes belong to a composition that is being
        // replaced. Recorded here because this is the only point that knows
        // it: afterwards a staged tile and an adopted marker look identical.
        // Recorded by ATTACHMENT ID: a number-keyed flag would stay behind
        // when the merge renumbers, and would then apply to the tile that
        // later took that number instead of the tile it was written for.
        const stagedBeforeAdoption = operations.items.map((item) => item.id);
        state.setState({
          foreignImageIds: [...new Set(stagedBeforeAdoption)].sort(),
          restoredImageIds: [],
        });
        updateText(draft.text);
        updateSkillNames(restoredSkillNames(draft));
        updateSelectionMeta(draft);
        currentDraftRevisionId = draft.revisionId;
        ownedDraftRevisionRef.current = readDraftRevision(ref);
        // This is the adoption the invariant names: another reader's draft
        // (or this one before a reload) carried the marker anchors, and this
        // source's attachment store starts empty, so the bytes those anchors
        // stood for are gone. Only the anchors the record itself listed as
        // having had bytes are unbacked - anything else spelled "[image N]" in
        // the text is prose the person typed, and it sends as text.
        adoptUnbackedImageMarkers(readComposerDraftImageAnchors(ref));
        // A rebind can land on a PEER's newer draft while this source already
        // holds staged work of its own. Marker numbers are not identity: the
        // peer's "[image 1]" is a different image, and the local bytes staged
        // under that number must not be read as backing for it. The local
        // work is preserved; it simply does not satisfy
        // the adopted text.
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
        sameSkillSelections(skillNamesRef.current, names) &&
        sameSkillSelections(selectionMetaRef.current.commandNames, commands)
      ) {
        updateText("");
        updateSkillNames([]);
        updateSelectionMeta({ text: "", skillNames: [] });
        if (readDraftRevision(ref) === draftRevision) {
          clearDraft(ref);
          ownedDraftRevisionRef.current = readDraftRevision(ref);
        }
      }
      return true;
    },
    clearSubmittedAttachments(items) {
      if (disposed) return;
      const markers = new Set(operations.items.filter((item) => items.includes(item)).map((item) => item.marker));
      // Same split as the recovery settle: retire every tile this completion is
      // done with, strip only the references this composition owns.
      const foreignIds = new Set(state.getState().foreignImageIds);
      const ownedMarkers = new Set(
        operations.items.filter((item) => !foreignIds.has(item.id)).map((item) => item.marker),
      );
      operations.clearSubmitted(markers, new Set([...markers].filter((marker) => ownedMarkers.has(marker))));
      // Sent bytes belong to the request that carried them. Retiring both the
      // adopted tags and the per-image exemptions here keeps a later
      // composition from inheriting facts meant for the composition now gone.
      // Exemptions retire by identity.
      //
      // A TAG retires when its REFERENCE leaves the text, and only then. It
      // must not retire because a tile number it shares was sent: a pane that
      // rebinds during a send adopts a peer draft whose missing reference uses
      // the same number, and this cleanup correctly leaves that text in place -
      // but retiring by number would then clear the peer's warning, and its
      // orphaned anchor would send literally later. The number is a label; the
      // reference is what the tag describes.
      const sentIds = new Set(items.map((item) => item.id));
      // Re-read against the editor's current text, which the tags follow by
      // occurrence: a tag retires when the reference it named is gone, and an
      // identical spelling elsewhere does not keep it alive.
      // The snapshot is read BEFORE the refresh so the comparison below sees
      // what it looked like a moment ago.
      const snapshot = state.getState();
      const priorUnbackedLength = snapshot.unbackedImageMarkers.length;
      refreshUnbackedImageMarkers();
      const foreign = snapshot.foreignImageIds.filter((id) => !sentIds.has(id));
      const restored = snapshot.restoredImageIds.filter((id) => !sentIds.has(id));
      if (
        foreign.length !== snapshot.foreignImageIds.length ||
        restored.length !== snapshot.restoredImageIds.length ||
        liveAdoptedMarkers().length !== priorUnbackedLength
      )
        state.setState({
          foreignImageIds: foreign,
          restoredImageIds: restored,
          unbackedImageMarkers: liveAdoptedMarkers(),
        });
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
