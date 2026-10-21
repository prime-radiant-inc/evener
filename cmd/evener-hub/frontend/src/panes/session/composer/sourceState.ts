import type { ComposerMention } from "@evener/appwire-client";
import { createStore } from "zustand/vanilla";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
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

type RevisionRef<T> = { current: T };
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
    activeRecoveryIdRef: RevisionRef<string | null>;
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
  setActiveRecoveryId(id: string | null): void;
  markRestore(): void;
  persistDraft(text: string): void;
  writeDraft(text: string, mayPersist: boolean, source?: TextEditSource): void;
  refreshRecovery(): void;
  activateRecovery(record: MutationRecoveryRecord): Promise<boolean>;
  queueRecoveryPersistence(
    id: string,
    text: string,
    attachments: InputAttachment[],
    names: readonly string[],
    commands?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): Promise<void>;
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

/** Original conversation work belongs to its pane, not to an editor mount. */
export function createComposerSourceState(ref: string): ComposerSourceState {
  const initial = readComposerDraftSnapshot(ref);
  let currentDraftRevisionId = initial.revisionId;
  const state = createStore<ComposerSourceSnapshot>(() => ({
    text: initial.text,
    skillNames: restoredSkillNames(initial),
    commandNames: initial.commandNames ?? [],
    mentions: initial.mentions,
    activeRecoveryId: null,
    recoveryActivating: false,
    freshRecoveryRef: null,
    restoreEpoch: 0,
  }));
  const continuity: ComposerSourceState["continuity"] = {
    textRef: { current: initial.text },
    skillNamesRef: { current: state.getState().skillNames },
    selectionMetaRef: { current: { commandNames: initial.commandNames ?? [], mentions: initial.mentions } },
    draftEditRevisionRef: { current: 0 },
    ownedDraftRevisionRef: { current: readDraftRevision(ref) },
    activeRecoveryIdRef: { current: null },
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
    activeRecoveryIdRef,
    recoveryWriteVersionRef,
    recoveryReplacementEpochRef,
    recoveryOwnsLocalDraftRef,
  } = continuity;
  const attachments = createAttachmentStore();
  let disposed = false;
  let boundEditor: TextEditor | null = null;
  let recoveryWrites: Promise<void> = Promise.resolve();
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
      activeRecoveryIdRef.current !== clientMutationId ||
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
        activeRecoveryIdRef.current !== clientMutationId ||
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
    if (activeRecoveryIdRef.current !== null) {
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
  function setActiveRecoveryId(activeRecoveryId: string | null): void {
    if (disposed) return;
    if (activeRecoveryIdRef.current !== activeRecoveryId) cancelCleanupRetry();
    activeRecoveryIdRef.current = activeRecoveryId;
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
    if (mayPersist && activeRecoveryIdRef.current === null) persistDraft(text);
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
  ): Promise<void> {
    if (disposed) return Promise.resolve();
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
          activeRecoveryIdRef.current !== clientMutationId ||
          recoveryReplacementEpochRef.current !== replacementEpoch
        )
          return;
        if (
          nextText.trim() === "" &&
          nextAttachments.length === 0 &&
          nextSkillNames.length === 0 &&
          nextCommandNames.length === 0 &&
          (nextMentions?.length ?? 0) === 0
        ) {
          const discarded = await discardRecoveryPendingTurn(
            clientMutationId,
            ref,
            () =>
              !disposed &&
              recoveryReplacementEpochRef.current === replacementEpoch &&
              activeRecoveryIdRef.current === clientMutationId &&
              hasEmptyComposition() &&
              (localDraftRevisionId === undefined || readComposerDraftRevisionId(ref) === localDraftRevisionId),
          );
          if (!discarded) return;
          cancelCleanupRetry();
          if (
            !disposed &&
            recoveryReplacementEpochRef.current === replacementEpoch &&
            activeRecoveryIdRef.current === clientMutationId &&
            readDraftRevision(ref) === draftRevision &&
            (localDraftRevisionId === undefined || readComposerDraftRevisionId(ref) === localDraftRevisionId) &&
            hasEmptyComposition()
          ) {
            recoveryOwnsLocalDraftRef.current = false;
            setActiveRecoveryId(null);
            if (localDraftRevisionId) clearPersistedDraftIfRevisionMatches(ref, localDraftRevisionId);
          }
          return;
        }
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
        );
        if (
          !disposed &&
          updated &&
          recoveryOwnsLocalDraftRef.current &&
          recoveryWriteVersionRef.current === version &&
          readDraftRevision(ref) === draftRevision &&
          (localDraftRevisionId === undefined || readComposerDraftRevisionId(ref) === localDraftRevisionId) &&
          activeRecoveryIdRef.current === clientMutationId
        ) {
          recoveryOwnsLocalDraftRef.current = false;
          if (localDraftRevisionId) clearPersistedDraftIfRevisionMatches(ref, localDraftRevisionId);
        }
      });
    recoveryWrites = operation;
    void operation.catch((error) => {
      if (disposed || activeRecoveryIdRef.current !== clientMutationId) return;
      if (isEmptyCleanup) scheduleCleanupRetry(clientMutationId, replacementEpoch);
      pushToast("error", `Couldn't save recovered message: ${error instanceof Error ? error.message : String(error)}`);
    });
    return operation;
  }

  function activateRecovery(record: MutationRecoveryRecord): Promise<boolean> {
    if (disposed || state.getState().recoveryActivating) return Promise.resolve(false);
    state.setState({ recoveryActivating: true });
    const operation = recoveryWrites
      .catch(() => undefined)
      .then(async () => {
        if (disposed || operations.hasPending || record.targetRef !== ref) return false;
        if (activeRecoveryIdRef.current === record.clientMutationId) return false;
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
        const ownerId = activeRecoveryIdRef.current ?? record.clientMutationId;
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
        );
        if (!updated || disposed || recoveryReplacementEpochRef.current !== replacementEpoch) return false;
        if (incorporatedSnapshotIsCurrent() && incorporatedRevisionId !== undefined) {
          incorporatedSnapshotCleared = clearPersistedDraftIfRevisionMatches(ref, incorporatedRevisionId);
        }
        // A commit may finish after new typing or attachment work. Append to
        // that newer draft rather than restoring the press-time snapshot.
        const latest = merge();
        recoveryReplacementEpochRef.current += 1;
        recoveryOwnsLocalDraftRef.current = true;
        setActiveRecoveryId(ownerId);
        editText(latest.text);
        editSkillNames(restoredSkillNames(latest));
        updateSelectionMeta(latest);
        operations.replaceWithSettled(latest.attachments);
        markRestore();
        return true;
      });
    const completed = operation.finally(() => {
      if (!disposed) state.setState({ recoveryActivating: false });
    });
    recoveryWrites = completed.then(
      () => undefined,
      () => undefined,
    );
    return completed;
  }

  const unsubscribeCommitted = subscribeComposerSubmissionCommitted(
    (targetRef, submittedText, submittedSkillNames, recovery, submittedCommandNames) => {
      if (disposed || targetRef !== ref) return;
      if (recovery && activeRecoveryIdRef.current === recovery.clientMutationId) {
        if (recovery.draftUnchanged) ownedDraftRevisionRef.current = readDraftRevision(ref);
        const owns = ownsDraft();
        recoveryOwnsLocalDraftRef.current = false;
        recoveryWriteVersionRef.current += 1;
        cancelCleanupRetry();
        recoveryReplacementEpochRef.current += 1;
        setActiveRecoveryId(null);
        if (
          recovery.draftUnchanged &&
          textRef.current === submittedText &&
          sameSkillSelections(skillNamesRef.current, submittedSkillNames) &&
          sameSkillSelections(selectionMetaRef.current.commandNames, submittedCommandNames ?? [])
        ) {
          updateText("");
          updateSkillNames([]);
          updateSelectionMeta({ text: "", skillNames: [] });
        }
        const markers = new Set(
          operations.items
            .filter((item) =>
              recovery.attachments.some(
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
      } else if (
        !recovery &&
        activeRecoveryIdRef.current === null &&
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
    if (disposed || boundEditor || persistenceScheduled || activeRecoveryIdRef.current === null) return;
    persistenceScheduled = true;
    queueMicrotask(() => {
      persistenceScheduled = false;
      const id = activeRecoveryIdRef.current;
      if (!disposed && !boundEditor && id !== null && !operations.hasPending)
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
    queueRecoveryPersistence,
    activateRecovery,
    bindEditor(nextEditor) {
      if (disposed) return () => {};
      if (!ownsDraft() && activeRecoveryIdRef.current === null) {
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
