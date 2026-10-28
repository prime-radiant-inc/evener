import type { ComposerMention } from "@evener/appwire-client";
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
  clearRecoveryPick,
  markDraftEdited,
  type RecoveryPick,
  readComposerDraft,
  readComposerDraftRevisionId,
  readComposerDraftSnapshot,
  readDraftRevision,
  readRecoveryPick,
  succeedRecoveryPick,
  writeComposerDraft,
  writeRecoveryPick,
  writeRecoveryPickAutomatically,
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
export type RecoveryOwnerLease = {
  clientMutationId: string;
  compositionRevision: number;
  /** The row's pick rank at activation; settles inherit it when the registry entry is gone. */
  pick?: RecoveryPick;
};
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
  };
  getSnapshot(): ComposerSourceSnapshot;
  subscribe(listener: () => void): () => void;
  bindEditor(editor: TextEditor): () => void;
  updateText(text: string): void;
  editText(text: string): void;
  updateSkillNames(names: string[]): void;
  editSkillNames(names: string[]): void;
  updateSelectionMeta(value: SkillEditorValue): void;
  setActiveRecoveryId(
    record: MutationRecoveryRecord | null,
    options?: { automatic?: boolean; focused?: boolean; replaces?: string },
  ): void;
  markRestore(): void;
  persistDraft(text: string): void;
  writeDraft(text: string, mayPersist: boolean, source?: TextEditSource): void;
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
  const compositionOwnerId = ownerId ?? defaultCompositionOwners.get(ref) ?? createSecureUUID();
  if (ownerId === undefined && !defaultCompositionOwners.has(ref))
    defaultCompositionOwners.set(ref, compositionOwnerId);
  const initial = readComposerDraftSnapshot(ref);
  let currentDraftRevisionId = initial.revisionId;
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
  function setActiveRecoveryId(
    record: MutationRecoveryRecord | null,
    options?: { automatic?: boolean; focused?: boolean; replaces?: string },
  ): void {
    if (disposed) return;
    const activeRecoveryId = record?.clientMutationId ?? null;
    const previousRecoveryId = currentRecoveryId();
    if (previousRecoveryId !== activeRecoveryId) {
      cancelCleanupRetry();
      committedRecoveryComposition = null;
    }
    recoveryBaseRecord = record;
    if (record === null) {
      // Retirement releases the retiring row's own pick entry: the pick is
      // keyed by that row, and the send consumed it. Picks this tab holds
      // for other rows are separate entries and survive untouched.
      if (previousRecoveryId !== null) clearRecoveryPick(previousRecoveryId);
      recoveryOwnerLeaseRef.current = null;
    } else {
      if (options?.automatic === true && options.replaces !== undefined) {
        // A background settle that displaces a row succeeds the pick: the
        // fresh row inherits the displaced row's rank, so only the user's own
        // later acts outrank it.
        succeedRecoveryPick(options.replaces, record.clientMutationId);
      } else if (options?.automatic === true) {
        // A background activation appends a pick that cannot outrank an
        // explicit one; only the focused pane's activation ranks as its own.
        writeRecoveryPickAutomatically(record.clientMutationId, options?.focused === true);
      } else {
        // A user pick is the newest choice by definition: it always wins.
        writeRecoveryPick(record.clientMutationId);
      }
      recoveryOwnerLeaseRef.current = {
        clientMutationId: record.clientMutationId,
        compositionRevision: recoveryCompositionRevision(record),
        // Retain the row's pick rank with the lease: a settle completing
        // after another tab consumed the row cannot re-read the registry
        // entry - a concurrent resolution may have released it as dangling -
        // so succession inherits from this captured rank.
        pick: readRecoveryPick(record.clientMutationId),
      };
    }
    state.setState({ activeRecoveryId });
  }
  function persistDraft(text: string): void {
    if (disposed) return;
    currentDraftRevisionId = writeComposerDraft(ref, {
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
            if (preserved) {
              // The preservation committed, so the fresh row succeeds the displaced
              // row's pick even when the pane closed while it settled: a reopen
              // restores the local work by row id wherever the durable target now
              // lives, and the user's later picks still outrank it.
              succeedRecoveryPick(clientMutationId, preserved.clientMutationId, lease.pick);
            }
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
                setActiveRecoveryId(preserved, { automatic: true, replaces: clientMutationId });
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
          const refreshedLease = recoveryOwnerLeaseRef.current;
          recoveryOwnerLeaseRef.current = {
            clientMutationId,
            compositionRevision: recoveryCompositionRevision(updated),
            pick: refreshedLease?.clientMutationId === clientMutationId ? refreshedLease.pick : undefined,
          };
          recoveryBaseRecord = updated;
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
          if (disposed || operations.hasPending || record.targetRef !== ref) return false;
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
          const refreshedLease = recoveryOwnerLeaseRef.current;
          recoveryOwnerLeaseRef.current = {
            clientMutationId: ownerId,
            compositionRevision: recoveryCompositionRevision(updated),
            pick: refreshedLease?.clientMutationId === ownerId ? refreshedLease.pick : undefined,
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
        currentDraftRevisionId = draft.revisionId;
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
