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
  type CompositionImageRecord,
  clearDraft,
  clearPersistedDraftIfRevisionMatches,
  markDraftEdited,
  readComposerDraft,
  readComposerDraftImageMarkers,
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
  compositionIsEmpty,
  excludedRecoveryImages,
  mergeRecoveryComposerDraft,
  type RecoveredComposerDraft,
  recoveryComposerDraft,
  recoveryDeclaredMarkers,
  recoveryImageReferencesUnavailable,
  recoveryTileEligibility,
} from "./recovery/recoveryDraft";
import {
  materializeSelectionReferences,
  parseSkillDocument,
  patchSelectionText,
  type SkillEditorValue,
  serializeSkillDocument,
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
  updateText(text: string): void;
  editText(text: string): void;
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
  function updateText(text: string): void {
    if (disposed) return;
    if (text.trim() !== "") cancelCleanupRetry();
    // An anchor follows the text it was adopted with. Reconciling on the WRITE
    // rather than re-reading spellings afterwards is what keeps two identical
    // `[image 1]` occurrences distinct: only the one the person removed goes.
    reconcileAdoptedAnchors(textRef.current, text);
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
  function recordedMarkerNumbers(text: string): number[] {
    const snapshot = state.getState();
    const staged = sendableAttachments(operations.items, text, {
      foreign: snapshot.foreignImageIds,
      restored: snapshot.restoredImageIds,
    }).map((item) => item.marker);
    // The live anchors, filtered against the text they now describe. These are
    // occurrences: an anchor survives while its own spelling is still there.
    const unbacked = liveAdoptedMarkers();
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
    return [...new Set([...staged, ...unbacked])].sort((left, right) => left - right);
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
    priorAnchors: readonly number[],
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
    const anchors = [...new Set(priorAnchors.filter((marker) => spoken.has(marker) && !tileMarkers.has(marker)))].sort(
      (left, right) => left - right,
    );
    return { anchors, tiles, ...(revision === undefined ? {} : { revision }) };
  }

  // What a row's stored image facts say, in the form the reuse comparison
  // compares. Absent and empty agree - neither states an anchor - and both
  // differ from a record that states one. The stored `revision` is left out on
  // purpose: it counts one row's own commits and says nothing about whether two
  // compositions mean the same thing.
  function storedImageFacts(clientMutationId: string): string {
    const record = readCompositionImageRecord(clientMutationId);
    if (record === undefined) return "none";
    return JSON.stringify({ anchors: record.anchors, tiles: record.tiles });
  }

  // The same form, for a composition that has not been stored yet.
  function compositionImageRecordSnapshot(
    text: string,
    owned: readonly PendingAttachment[],
    priorAnchors: readonly number[],
  ): ReturnType<typeof compositionImageRecord> {
    return compositionImageRecord(text, owned, priorAnchors);
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
  function ownedCompositionAttachments(candidates: readonly InputAttachment[]): InputAttachment[] {
    const foreign = new Set(state.getState().foreignImageIds);
    const owned = operations.items.filter((item) => !foreign.has(item.id) && item.data !== undefined);
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
      recordedMarkerNumbers(text),
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

  /** The occurrence form of a marker list recorded before occurrences existed.
   *
   * A bare number says "this composition had an anchor under N" without saying
   * which spelling. The first match is the answer it could have meant, and it
   * is what that record was read as before the field existed - so an old saved
   * draft loads exactly as it used to. Never inventing an offset is what keeps
   * an upgrade from silently invalidating somebody's unsent work.
   */
  function occurrencesOf(markers: readonly number[], text: string): { marker: number; offset: number }[] {
    const available = markerOccurrences(text);
    const used = new Set<number>();
    const out: { marker: number; offset: number }[] = [];
    for (const marker of markers) {
      const hit = available.find((item) => item.marker === marker && !used.has(item.offset));
      if (hit === undefined) continue;
      used.add(hit.offset);
      out.push(hit);
    }
    return out;
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

  /** Carry the adopted anchors across an edit to the text.
   *
   * The same splice rule the mention anchors use: a shared prefix and suffix
   * bound the region the edit touched, an anchor entirely before or after it
   * survives (shifted when it is after), and one the edit overlapped is gone.
   * That is what lets a deletion retire its own anchor while an identical
   * spelling elsewhere in the text stays ordinary prose.
   */
  function reconcileAdoptedAnchors(before: string, after: string): void {
    if (before === after || adoptedAnchors.length === 0) return;
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
    const delta = after.length - before.length;
    adoptedAnchors = adoptedAnchors.flatMap((anchor) => {
      const length = markerText(anchor.marker).length;
      if (anchor.offset + length <= prefix) return [anchor];
      if (anchor.offset >= oldEnd) return [{ ...anchor, offset: anchor.offset + delta }];
      return [];
    });
  }

  /** Adopt the markers a persisted record says stood for images. */
  function adoptUnbackedImageMarkers(recorded: readonly number[]): void {
    adoptedAnchors = occurrencesOf(recorded, textRef.current);
    refreshUnbackedImageMarkers();
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
  ): { restored: string[]; foreign: string[]; anchors: number[] } {
    // A row's own tags, read from the row that ARRIVED - not from whichever row
    // is active afterwards. Opening a second row merges its text into the row
    // already open and consumes the second, so "the active row's record" then
    // answers with the FIRST row and drops the incoming anchor: the merged text
    // spells a reference nothing is left to warn about, and Send ships it as
    // literal prose. Read here, before the caller clears the incoming row.
    // The row's own record, read by the row that ARRIVED. `tiles` is keyed by
    // attachment id and `anchors` by spelling; neither is a marker-number list
    // borrowed from whatever the store happens to hold.
    const incomingRecord = incomingRowId === null ? undefined : readCompositionImageRecord(incomingRowId);
    const incoming = incomingRecord?.anchors ?? [];
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
    const recorded =
      rowId !== null
        ? (readCompositionImageRecord(rowId)?.anchors ?? [])
        : ownsDraft()
          ? readComposerDraftImageMarkers(ref)
          : [];
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
    const anchors = [
      ...new Set([
        ...liveAdoptedMarkers(),
        ...[...new Set(recorded)]
          .filter((marker) => spoken.has(marker) && !restoredMarkers.has(marker) && !backedMarkers.has(marker))
          .sort((left, right) => left - right),
        ...[...new Set(incoming)]
          .filter((marker) => spoken.has(marker) && !restoredMarkers.has(marker))
          .sort((left, right) => left - right),
        ...[...declared]
          .filter((marker) => spoken.has(marker) && !restoredMarkers.has(marker))
          .sort((left, right) => left - right),
      ]),
    ].sort((left, right) => left - right);
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
    return { restored, foreign, anchors };
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
  adoptUnbackedImageMarkers(readComposerDraftImageMarkers(ref));

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
    nextAttachments = ownedCompositionAttachments(nextAttachments);
    // The tiles whose facts this save describes, captured HERE with the text
    // and before the first await. The record and the durable row are written to
    // two stores, so a live store read after the await can describe an edit
    // that the row this write commits never had - facts stamped for revision N
    // but built from the store as it stands at revision N+1.
    const committedTilesForRecord = operations.items;
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
          // The facts are stamped with the revision this write just created.
          // The record and the row are two stores: stamping lets a reader tell
          // "these facts describe the row I am opening" from "these facts
          // describe an older composition of it", which is the difference
          // between honouring a deleted reference and restoring the exemption
          // the person's edit revoked.
          writeCompositionImageRecord(
            clientMutationId,
            compositionImageRecord(
              nextText,
              committedTilesForRecord,
              readCompositionImageRecord(clientMutationId)?.anchors ?? [],
              undefined,
              recoveryCompositionRevision(updated),
            ),
          );
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
    const preservedAttachments = ownedCompositionAttachments(operations.toInputAttachments());
    // The tiles the record below describes, captured with the text and the
    // bytes it describes them for.
    const preservedTiles = operations.items;
    const priorAnchors = liveAdoptedMarkers();
    try {
      const preserved = await preserveRecoveryPendingTurn(
        base,
        ref,
        preservedText,
        preservedAttachments,
        skillNamesRef.current,
        selectionMetaRef.current.commandNames,
        selectionMetaRef.current.mentions,
        compositionOwnerId,
        localDraftRevisionId,
        // Both sides of the comparison come from the ONE record reader, so a
        // row is reused only when it already says the same thing about its
        // images as this composition does.
        storedImageFacts,
        JSON.stringify(compositionImageRecordSnapshot(preservedText, preservedTiles, priorAnchors)),
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
          const updated = await updateRecoveryPendingTurn(
            ownerId,
            ref,
            merged.text,
            [...durableAttachments(merged), ...durableAttachments({ ...merged, attachments: retainedBefore })],
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
          const adopted = recoveredImageFacts(recovered, record.clientMutationId, ownerId);
          writeCompositionImageRecord(
            ownerId,
            compositionImageRecord(
              merged.text,
              committedTiles,
              [
                ...(readCompositionImageRecord(ownerId)?.anchors ?? []),
                ...(readCompositionImageRecord(record.clientMutationId)?.anchors ?? []),
                // The anchors this merge itself creates: an incoming missing
                // reference is one, and the row has to carry it in the same
                // transaction that merges its text.
                ...adopted.anchors,
              ],
              // `adopted` supplies the exemptions this merge installs: an incoming missing reference is an anchor of the
              // composition being committed, and the row must carry it in the
              // same transaction that merges its text. Without it a close
              // between the commit and the live editor catching up leaves the
              // row holding "[image 1]" as prose.
              adopted,
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
          adoptUnbackedImageMarkers(adopted.anchors);
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
        adoptUnbackedImageMarkers(readComposerDraftImageMarkers(ref));
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
