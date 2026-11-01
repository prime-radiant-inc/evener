import type { ComposerMention } from "@evener/appwire-client";
import { createStore } from "zustand/vanilla";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import { createSecureUUID } from "../../../stores/secureUUID";
import type { InputAttachment } from "../../../stores/threads";
import { readMutationPersistence } from "../../../stores/threads";
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
import { mergeRecoveryComposerDraft, recoveryComposerDraft } from "./recovery/recoveryDraft";
import {
  materializeSelectionReferences,
  parseSkillDocument,
  patchSelectionText,
  type SkillEditorValue,
  serializeSkillDocument,
} from "./skillDocument";
import { pushRecoveryRefusal } from "./submitProtocol";

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
export interface ComposerSubmissionSnapshot {
  text: string;
  attachments: PendingAttachment[];
  skillNames: string[];
  commandNames: string[];
  mentions?: readonly ComposerMention[];
  revision: number;
  draftRevision: number;
}

export interface ComposerSourceSnapshot {
  text: string;
  skillNames: string[];
  commandNames: string[];
  mentions?: ComposerMention[];
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
    lastDrainSnapshotRef: RevisionRef<ComposerSubmissionSnapshot | null>;
    /** The persisted-draft revision id this source last loaded or wrote.
     * Draft cleanup may only clear a draft it can name by this identity. */
    boundDraftRevisionIdRef: RevisionRef<string | undefined>;
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
  persistDraft(text: string): void;
  writeDraft(text: string, mayPersist: boolean, source?: TextEditSource): void;
  refreshRecovery(): void;
  readonly compositionOwnerId: string;
  runRecoveryOperation<T>(operation: () => Promise<T>): Promise<T>;
  /** Preserve the current composition under a fresh owner at a consumed
   * resend outcome, then adopt the preserved row by lease transfer alone. */
  preserveConsumedRecovery(clientMutationId: string): Promise<void>;
  /** Adopt a mount-restored recovery row by lease transfer alone. The
   * restored row is the composition's durable home; the close's flush reads
   * the row itself to decide whether an unchanged close owes a write. */
  adoptRestoredRecovery(record: MutationRecoveryRecord): void;
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

/** The fingerprint of the composition a durable row itself persists - the
 * representation the row actually holds (its composer text, not the parsed
 * submission body), so a comparison against the live composition never
 * mismatches on the built-in invocation-vs-argument shape. */
function durableRecoveryFingerprint(record: MutationRecoveryRecord): string {
  const draft = recoveryComposerDraft(record);
  return recoveryCompositionFingerprint({
    text: draft.text,
    attachments: draft.attachments.flatMap((item) =>
      item.data === undefined
        ? []
        : [{ marker: item.marker, name: item.name, mediaType: item.mediaType, data: item.data }],
    ),
    skillNames: restoredSkillNames(draft),
    commandNames: draft.commandNames ?? [],
    mentions: draft.mentions,
  });
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
  const boundDraftRevisionIdRef: RevisionRef<string | undefined> = { current: initial.revisionId };
  const state = createStore<ComposerSourceSnapshot>(() => ({
    text: initial.text,
    skillNames: restoredSkillNames(initial),
    commandNames: initial.commandNames ?? [],
    mentions: initial.mentions,
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
    lastDrainSnapshotRef: { current: null },
    boundDraftRevisionIdRef,
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
  // A chain-derived row fact, not a composition copy: the pane's own
  // empty-cleanup discard was mid-flight at disposal and deleted the row as
  // it settled, so nothing durable holds this pane's recovery composition.
  // The disposal flush is the only post-dispose reader.
  let chainDiscardedRecoveryRow = false;
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
    return (
      textRef.current.trim() === "" &&
      operations.items.length === 0 &&
      skillNamesRef.current.length === 0 &&
      selectionMetaRef.current.commandNames.length === 0 &&
      (selectionMetaRef.current.mentions?.length ?? 0) === 0
    );
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
  function currentDraftText(): string {
    return ownsDraft() ? textRef.current : readComposerDraft(ref).text;
  }
  function updateText(text: string): void {
    if (disposed) return;
    if (text.trim() !== "") cancelCleanupRetry();
    textRef.current = text;
    state.setState({ text });
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
    }
    recoveryOwnerLeaseRef.current = record
      ? { clientMutationId: record.clientMutationId, compositionRevision: recoveryCompositionRevision(record) }
      : null;
    recoveryBaseRecord = record;
    state.setState({ activeRecoveryId });
  }
  function persistDraft(text: string): void {
    if (disposed) return;
    boundDraftRevisionIdRef.current = writeComposerDraft(ref, {
      text,
      skillNames: skillNamesRef.current,
      ...(selectionMetaRef.current.commandNames.length ? { commandNames: selectionMetaRef.current.commandNames } : {}),
      ...(selectionMetaRef.current.mentions ? { mentions: selectionMetaRef.current.mentions } : {}),
    });
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
  const operations = createAttachmentOperations(editor, attachments);

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
    const isEmptyCleanup =
      nextText.trim() === "" &&
      nextAttachments.length === 0 &&
      nextSkillNames.length === 0 &&
      nextCommandNames.length === 0 &&
      (nextMentions?.length ?? 0) === 0;
    if (!isEmptyCleanup) cancelCleanupRetry();
    const version = ++recoveryWriteVersionRef.current;
    const replacementEpoch = recoveryReplacementEpochRef.current;
    const draftRevision = readDraftRevision(ref);
    const localDraftRevisionId =
      ownsDraft() &&
      boundDraftRevisionIdRef.current !== undefined &&
      boundDraftRevisionIdRef.current === readComposerDraftRevisionId(ref)
        ? boundDraftRevisionIdRef.current
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
          nextText.trim() === "" &&
          nextAttachments.length === 0 &&
          nextSkillNames.length === 0 &&
          nextCommandNames.length === 0 &&
          (nextMentions?.length ?? 0) === 0
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
          } else if (disposed && currentRecoveryId() === clientMutationId) {
            // The discard passed its liveness check before disposal and
            // settled after it, deleting the row: nothing durable now holds
            // this pane's recovery composition. Record the chain fact so
            // the disposal flush - the only remaining reader - still gives
            // the composition a durable home instead of skipping against a
            // row its own chain deleted.
            chainDiscardedRecoveryRow = true;
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
                // bytes; the close's flush reads the row itself to decide
                // whether an unchanged close owes a final write.
                setActiveRecoveryId(preserved);
              } else {
                // Preservation failed, so nothing durable carries the images.
                // Fall back to the ordinary draft and retire the lease.
                persistDraft(textRef.current);
                setActiveRecoveryId(null);
              }
            } else if (preserved && disposed && currentRecoveryId() === clientMutationId) {
              // The preserve was called before disposal and its transaction
              // settled after it. A write that started before disposal is
              // past the disposed fence, and the durable fact it just
              // committed - the fresh row holding this composition - still
              // belongs to this pane's write chain. Record the lease and
              // the row into the continuity refs so the disposal flush, the
              // only remaining reader, settles onto this row instead of
              // minting a second one.
              recoveryOwnerLeaseRef.current = {
                clientMutationId: preserved.clientMutationId,
                compositionRevision: recoveryCompositionRevision(preserved),
              };
              recoveryBaseRecord = preserved;
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
        } else if (disposed && currentRecoveryId() === clientMutationId) {
          // A save that started before disposal is past the disposed fence,
          // and the row it just committed still belongs to this pane's
          // write chain. Record the returned revision and the row into the
          // continuity refs - never the state store - so the disposal
          // flush, the only remaining reader, settles onto what this chain
          // last wrote instead of deciding from a disposal-time snapshot.
          recoveryOwnerLeaseRef.current = {
            clientMutationId,
            compositionRevision: recoveryCompositionRevision(updated),
          };
          recoveryBaseRecord = updated;
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
    // Capture the composition synchronously: the preserved row holds exactly
    // these refs as they stand now.
    const text = textRef.current;
    const attachments = operations.toInputAttachments();
    const skillNames = [...skillNamesRef.current];
    const commandNames = [...selectionMetaRef.current.commandNames];
    const mentions = selectionMetaRef.current.mentions;
    try {
      const record = await preserveRecoveryPendingTurn(
        base,
        ref,
        text,
        attachments,
        skillNames,
        commandNames,
        mentions,
        compositionOwnerId,
        localDraftRevisionId,
      );
      return record ?? undefined;
    } catch (error) {
      if (!disposed)
        pushToast(
          "error",
          `Couldn't preserve recovered message: ${error instanceof Error ? error.message : String(error)}`,
        );
      return undefined;
    }
  }

  async function preserveConsumedRecovery(clientMutationId: string): Promise<void> {
    if (disposed || currentRecoveryId() !== clientMutationId) return;
    // Serialize with the recovery-write chain: a save CAS that failed against
    // the consumed row may already be preserving this same composition, and
    // whichever write preserves first moves the lease, so the other stands
    // down by identity instead of minting a second row.
    const operation = recoveryWrites
      .catch(() => undefined)
      .then(async () => {
        if (disposed || currentRecoveryId() !== clientMutationId) return undefined;
        return preserveStaleRecovery(clientMutationId);
      });
    recoveryWrites = operation.then(
      () => undefined,
      () => undefined,
    );
    const preserved = await operation;
    if (disposed) {
      // The outcome's preserve started before disposal and settled after
      // it: the fresh row it minted is a durable fact of this pane's write
      // chain, not a stale lease to resurrect. Record it into the
      // continuity refs - never the state store - so the disposal flush,
      // the only remaining reader, settles onto it instead of minting a
      // second row of the same composition.
      if (preserved && currentRecoveryId() === clientMutationId) {
        recoveryOwnerLeaseRef.current = {
          clientMutationId: preserved.clientMutationId,
          compositionRevision: recoveryCompositionRevision(preserved),
        };
        recoveryBaseRecord = preserved;
      }
      return;
    }
    // A concurrent write already re-homed the composition: its durable home
    // is not this call's to replace.
    if (currentRecoveryId() !== clientMutationId) return;
    if (preserved) {
      // Adoption is a lease transfer only: the live refs stay the content's
      // only home, and no content is written back into the composer; the
      // close's flush reads the row itself to decide whether an unchanged
      // close owes a final write.
      setActiveRecoveryId(preserved);
      return;
    }
    // Preservation failed: fall back to the ordinary draft and retire the
    // dead lease, exactly as the save path's preserve fallback does.
    persistDraft(textRef.current);
    setActiveRecoveryId(null);
  }

  function adoptRestoredRecovery(record: MutationRecoveryRecord): void {
    if (disposed) return;
    setActiveRecoveryId(record);
  }

  // The disposal capture: the live refs are the content's only home until
  // disposal, and disposal is synchronous, so this snapshot is the
  // composition's final word. Exactly one durable write follows it on the
  // recovery-write chain - close flushes, never drops - and the disposed
  // guard that fences stale user writes never gates this one.
  function enqueueDisposalFlush(): void {
    const lease = recoveryOwnerLeaseRef.current;
    // A pane holding no lease has already persisted every keystroke to its
    // draft (writeDraft persists exactly when no lease is held); nothing new
    // is owed.
    if (lease === null) return;
    // An image still mid-encode has no bytes to write: its marker reference
    // would restore as a literal orphan, so the final write strips it where
    // the gated autosave paths simply wait out the encode instead. The
    // strip goes through patchSelectionText so the selection metadata's
    // offsets follow the shortened text, exactly as every editor write
    // shifts them; a raw length change would restore displaced skill
    // references.
    let value: SkillEditorValue = {
      text: textRef.current,
      skillNames: [...skillNamesRef.current],
      ...(selectionMetaRef.current.commandNames.length
        ? { commandNames: [...selectionMetaRef.current.commandNames] }
        : {}),
      ...(selectionMetaRef.current.mentions ? { mentions: [...selectionMetaRef.current.mentions] } : {}),
    };
    for (const marker of operations.items.filter((item) => item.pending).map((item) => item.marker)) {
      value = patchSelectionText(value, value.text.split(`[image ${marker}]`).join(""));
    }
    const text = value.text;
    const attachments = operations.toInputAttachments();
    const skillNames = value.skillNames;
    const commandNames = value.commandNames ?? [];
    const mentions = value.mentions;
    const capturedFingerprint = recoveryCompositionFingerprint({
      text,
      attachments,
      skillNames,
      commandNames,
      mentions,
    });
    const localDraftRevisionId =
      ownsDraft() &&
      boundDraftRevisionIdRef.current !== undefined &&
      boundDraftRevisionIdRef.current === readComposerDraftRevisionId(ref)
        ? boundDraftRevisionIdRef.current
        : undefined;
    const isEmpty =
      text.trim() === "" &&
      attachments.length === 0 &&
      skillNames.length === 0 &&
      commandNames.length === 0 &&
      (mentions?.length ?? 0) === 0;
    const operation = recoveryWrites
      .catch(() => undefined)
      .then(async () => {
        // This continuation writes durable state only: every composition
        // value it names was captured synchronously above, before `disposed`
        // flipped, and the source is frozen for good. The guard decisions,
        // though, read the write chain's state at THIS turn: a source-owned
        // write that was mid-flight at disposal settles first on this chain
        // and records its revision, its row, and its committed composition,
        // so the flush settles after its own writes instead of deciding from
        // a disposal-time snapshot.
        const turnLease = recoveryOwnerLeaseRef.current;
        if (turnLease === null) return;
        // The skip decision reads the durable row this pane's chain last
        // named - one source of truth for what the durable side already
        // holds, in the representation the row itself persists. The live row
        // answers while it still carries the chain's own revision; a row
        // that moved under a peer, or no row at all, is answered by the
        // chain's last-known row snapshot - except when this pane's own
        // mid-flight discard deleted the row as it settled, in which case
        // nothing durable holds the captured composition and the flush owes
        // it a home.
        const liveRow = (await readMutationPersistence()).recovery.find(
          (record) => record.clientMutationId === turnLease.clientMutationId,
        );
        let skipSource: MutationRecoveryRecord | null;
        if (chainDiscardedRecoveryRow) {
          skipSource = null;
        } else if (liveRow !== undefined && recoveryCompositionRevision(liveRow) === turnLease.compositionRevision) {
          skipSource = liveRow;
        } else {
          skipSource = recoveryBaseRecord;
        }
        // The durable row already holds exactly this composition - the pane
        // has no unsaved work. Closing writes nothing: an unchanged echo
        // would stale every peer lease on the row, and a write after another
        // tab sent or dismissed the row would resurrect a settled message.
        if (skipSource !== null && durableRecoveryFingerprint(skipSource) === capturedFingerprint) return;
        const expectedRevision = turnLease.compositionRevision;
        if (isEmpty) {
          const discarded = await discardRecoveryPendingTurn(
            turnLease.clientMutationId,
            ref,
            () =>
              // The ordinary discard's liveness terms (!disposed, replacement
              // epoch) speak for a live pane; this write IS the disposal, the
              // lease is frozen, and the captured snapshot answers the emptiness
              // question.
              isEmpty &&
              currentRecoveryId() === turnLease.clientMutationId &&
              (localDraftRevisionId === undefined || readComposerDraftRevisionId(ref) === localDraftRevisionId),
            expectedRevision,
          );
          // The outcome is carried, never ignored: true removed the row
          // this pane emptied; false means the row moved under a writer
          // this pane can no longer name - a peer's send consumed it, or a
          // peer advanced it with that peer's own work - and the cleared
          // composition has nothing durable left to remove, so the
          // surviving row belongs to that writer.
          return discarded;
        }
        // The pane's own submission still in flight owns the row's
        // consumption, and the outbox snapshot it carries predates the
        // post-submit edits: an update onto the row would race that
        // consumption and lose the edits, so the flush routes around the
        // CAS and preserves the edited composition under this pane's seat.
        // The turn-time fingerprint skip above already suppresses the
        // no-edit duplicate of the sent message.
        if (!recoverySubmissionsInFlight.has(turnLease.clientMutationId)) {
          const updated = await updateRecoveryPendingTurn(
            turnLease.clientMutationId,
            ref,
            text,
            attachments,
            skillNames,
            commandNames,
            mentions,
            [],
            localDraftRevisionId,
            expectedRevision,
            { [turnLease.clientMutationId]: expectedRevision },
            compositionOwnerId,
          );
          if (updated) return;
        }
        const base = recoveryBaseRecord;
        if (base === null || base.clientMutationId !== turnLease.clientMutationId) return;
        // The CAS failed: the row moved under another writer. The captured
        // composition still needs a durable home, under this pane's own seat.
        await preserveRecoveryPendingTurn(
          base,
          ref,
          text,
          attachments,
          skillNames,
          commandNames,
          mentions,
          compositionOwnerId,
          localDraftRevisionId,
        );
      });
    recoveryWrites = operation.then(
      () => undefined,
      () => undefined,
    );
    void operation.catch((error) => {
      pushToast("error", `Couldn't save recovered message: ${error instanceof Error ? error.message : String(error)}`);
    });
  }

  function activateRecovery(record: MutationRecoveryRecord): Promise<boolean> {
    if (disposed || state.getState().recoveryActivating || recoverySubmissionsInFlight.has(record.clientMutationId))
      return Promise.resolve(false);
    state.setState({ recoveryActivating: true });
    const completed = runRecoveryOperation(() => {
      const operation = recoveryWrites
        .catch(() => undefined)
        .then(async () => {
          if (disposed || operations.hasPending || record.targetRef !== ref) return false;
          if (currentRecoveryId() === record.clientMutationId) return false;
          // Keep this object: the recovery wrapper carries older-record image
          // reference provenance by identity, not in enumerable properties.
          const recovered = recoveryComposerDraft(record);
          if (record.recoveryKind === "editing") Object.assign(recovered, materializeSelectionReferences(recovered));
          const incorporatedRevisionId = record.localDraftRevisionId;
          const includedDraftRevisionId =
            boundDraftRevisionIdRef.current !== undefined &&
            boundDraftRevisionIdRef.current === readComposerDraftRevisionId(ref)
              ? boundDraftRevisionIdRef.current
              : undefined;
          let incorporatedSnapshotCleared = false;
          // A different source's stored draft does not change this loaded snapshot.
          // Only cleanup below needs to match the current persisted revision.
          const incorporatedSnapshotIsCurrent = () =>
            incorporatedRevisionId !== undefined &&
            (incorporatedSnapshotCleared ||
              (incorporatedRevisionId === boundDraftRevisionIdRef.current && draftEditRevisionRef.current === 0));
          const merge = () => {
            const skipLocalDraft = incorporatedSnapshotIsCurrent();
            return mergeRecoveryComposerDraft(
              skipLocalDraft ? "" : textRef.current,
              operations.items,
              recovered,
              skipLocalDraft ? [] : skillNamesRef.current,
              skipLocalDraft ? [] : selectionMetaRef.current.commandNames,
              skipLocalDraft ? undefined : selectionMetaRef.current.mentions,
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
          operations.replaceWithSettled(latest.attachments);
          markRestore();
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
      // The row the chain last knew - the same identity the preserve path
      // seeds from - answers what the durable side already holds, in the
      // representation the row persists.
      const durable = recoveryBaseRecord === null ? null : durableRecoveryFingerprint(recoveryBaseRecord);
      if (
        !disposed &&
        !boundEditor &&
        id !== null &&
        !recoverySubmissionsInFlight.has(id) &&
        !operations.hasPending &&
        durable !== current
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
    persistDraft,
    writeDraft,
    compositionOwnerId,
    runRecoveryOperation,
    preserveConsumedRecovery,
    adoptRestoredRecovery,
    flushRecoveryWrites: () => recoveryWrites.catch(() => undefined),
    tryAcquireRecoveryOperation,
    tryAcquireBuiltinOperation,
    queueRecoveryPersistence,
    activateRecovery,
    bindEditor(nextEditor) {
      if (disposed) return () => {};
      if (!ownsDraft() && currentRecoveryId() === null) {
        const draft = readComposerDraftSnapshot(ref);
        updateText(draft.text);
        updateSkillNames(restoredSkillNames(draft));
        updateSelectionMeta(draft);
        boundDraftRevisionIdRef.current = draft.revisionId;
        ownedDraftRevisionRef.current = readDraftRevision(ref);
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
      operations.clearSubmitted(markers);
    },
    dispose() {
      if (disposed) return;
      enqueueDisposalFlush();
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
