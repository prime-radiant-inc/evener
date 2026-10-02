import { createStore } from "zustand/vanilla";
import type { InputAttachment } from "../../../stores/threads";
import { pushToast } from "../../../widgets/toast/store";
import {
  type AttachmentStore,
  createAttachmentOperations,
  createAttachmentStore,
  type PendingAttachment,
  type TextEditor,
} from "./attachments/useAttachments";
import {
  clearDraft,
  clearPersistedDraft,
  markDraftEdited,
  readComposerDraft,
  readDraftRevision,
  writeComposerDraft,
} from "./draft";
import {
  discardRecoveryPendingTurn,
  refreshPendingTurnsProjection,
  subscribeComposerSubmissionCommitted,
  updateRecoveryPendingTurn,
} from "./queue/pendingTurnsStore";
import { parseSkillDocument, type SkillEditorValue, serializeSkillDocument } from "./skillDocument";

type RevisionRef<T> = { current: T };
export interface ComposerSubmissionSnapshot {
  text: string;
  attachments: PendingAttachment[];
  skillNames: string[];
  revision: number;
  draftRevision: number;
}

export interface ComposerSourceSnapshot {
  text: string;
  skillNames: string[];
  activeRecoveryId: string | null;
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
  setActiveRecoveryId(id: string | null): void;
  markRestore(): void;
  persistDraft(text: string): void;
  refreshRecovery(): void;
  queueRecoveryPersistence(
    id: string,
    text: string,
    attachments: InputAttachment[],
    names: readonly string[],
  ): Promise<void>;
  clearIfUnchanged(text: string, revision: number, draftRevision: number, names: readonly string[]): boolean;
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
  const initial = readComposerDraft(ref);
  const state = createStore<ComposerSourceSnapshot>(() => ({
    text: initial.text,
    skillNames: restoredSkillNames(initial),
    activeRecoveryId: null,
    freshRecoveryRef: null,
    restoreEpoch: 0,
  }));
  const continuity: ComposerSourceState["continuity"] = {
    textRef: { current: initial.text },
    skillNamesRef: { current: state.getState().skillNames },
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

  const ownsDraft = () => ownedDraftRevisionRef.current === readDraftRevision(ref);
  function currentDraftText(): string {
    return ownsDraft() ? textRef.current : readComposerDraft(ref).text;
  }
  function updateText(text: string): void {
    if (disposed) return;
    textRef.current = text;
    state.setState({ text });
  }
  function updateSkillNames(skillNames: string[]): void {
    if (disposed) return;
    skillNamesRef.current = skillNames;
    state.setState({ skillNames });
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
    markEdited();
    updateText(text);
  }
  function editSkillNames(names: string[]): void {
    if (disposed) return;
    markEdited();
    updateSkillNames(names);
  }
  function setActiveRecoveryId(activeRecoveryId: string | null): void {
    if (disposed) return;
    activeRecoveryIdRef.current = activeRecoveryId;
    state.setState({ activeRecoveryId });
  }
  function persistDraft(text: string): void {
    if (disposed) return;
    writeComposerDraft(ref, { text, skillNames: skillNamesRef.current });
    ownedDraftRevisionRef.current = readDraftRevision(ref);
  }
  function markRestore(): void {
    if (!disposed) state.setState((snapshot) => ({ restoreEpoch: snapshot.restoreEpoch + 1 }));
  }
  function writeDetachedSourceDraft(text: string, _cursor: number, source?: "edit" | "submission"): void {
    if (disposed || !ownsDraft()) return;
    updateSkillNames(restoredSkillNames({ text, skillNames: skillNamesRef.current }));
    if (source === "submission") updateText(text);
    else editText(text);
    if (activeRecoveryIdRef.current === null) persistDraft(text);
  }
  const editor: TextEditor = {
    read: () => {
      const text = currentDraftText();
      const mounted = boundEditor?.read();
      return mounted
        ? { ...mounted, text }
        : { text, cursor: text.length, selection: { start: text.length, end: text.length } };
    },
    write: (text, cursor, source) => {
      // A continuation cannot take ownership back from another pane's edit.
      if (disposed || !ownsDraft()) return;
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
  ): Promise<void> {
    if (disposed) return Promise.resolve();
    const version = ++recoveryWriteVersionRef.current;
    const replacementEpoch = recoveryReplacementEpochRef.current;
    const draftRevision = readDraftRevision(ref);
    const operation = recoveryWrites
      .catch(() => undefined)
      .then(async () => {
        if (disposed || activeRecoveryIdRef.current !== clientMutationId) return;
        if (nextText.trim() === "" && nextAttachments.length === 0 && nextSkillNames.length === 0) {
          if (textRef.current.trim() !== "" || operations.items.length > 0 || skillNamesRef.current.length > 0) return;
          await discardRecoveryPendingTurn(
            clientMutationId,
            ref,
            () =>
              !disposed &&
              recoveryReplacementEpochRef.current === replacementEpoch &&
              activeRecoveryIdRef.current === clientMutationId &&
              textRef.current.trim() === "" &&
              operations.items.length === 0 &&
              skillNamesRef.current.length === 0,
          );
          if (
            !disposed &&
            activeRecoveryIdRef.current === clientMutationId &&
            readDraftRevision(ref) === draftRevision &&
            textRef.current.trim() === "" &&
            operations.items.length === 0 &&
            skillNamesRef.current.length === 0
          ) {
            recoveryOwnsLocalDraftRef.current = false;
            setActiveRecoveryId(null);
            clearPersistedDraft(ref);
          }
          return;
        }
        const updated = await updateRecoveryPendingTurn(
          clientMutationId,
          ref,
          nextText,
          nextAttachments,
          nextSkillNames,
        );
        if (
          !disposed &&
          updated &&
          recoveryOwnsLocalDraftRef.current &&
          recoveryWriteVersionRef.current === version &&
          readDraftRevision(ref) === draftRevision &&
          activeRecoveryIdRef.current === clientMutationId
        ) {
          recoveryOwnsLocalDraftRef.current = false;
          clearPersistedDraft(ref);
        }
      });
    recoveryWrites = operation;
    void operation.catch((error) => {
      if (disposed || activeRecoveryIdRef.current !== clientMutationId) return;
      pushToast("error", `Couldn't save recovered message: ${error instanceof Error ? error.message : String(error)}`);
    });
    return operation;
  }

  const unsubscribeCommitted = subscribeComposerSubmissionCommitted(
    (targetRef, submittedText, submittedSkillNames, recovery) => {
      if (disposed || targetRef !== ref) return;
      if (recovery && activeRecoveryIdRef.current === recovery.clientMutationId) {
        if (recovery.draftUnchanged) ownedDraftRevisionRef.current = readDraftRevision(ref);
        const owns = ownsDraft();
        recoveryOwnsLocalDraftRef.current = false;
        recoveryWriteVersionRef.current += 1;
        recoveryReplacementEpochRef.current += 1;
        setActiveRecoveryId(null);
        if (
          recovery.draftUnchanged &&
          textRef.current === submittedText &&
          sameSkillSelections(skillNamesRef.current, submittedSkillNames)
        ) {
          updateText("");
          updateSkillNames([]);
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
        sameSkillSelections(skillNamesRef.current, submittedSkillNames)
      ) {
        ownedDraftRevisionRef.current = readDraftRevision(ref);
        updateText("");
        updateSkillNames([]);
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
  const unsubscribeAttachments = attachments.subscribe(persistDetachedRecovery);
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
    setActiveRecoveryId,
    markRestore,
    persistDraft,
    queueRecoveryPersistence,
    bindEditor(nextEditor) {
      if (disposed) return () => {};
      if (!ownsDraft() && activeRecoveryIdRef.current === null) {
        const draft = readComposerDraft(ref);
        updateText(draft.text);
        updateSkillNames(restoredSkillNames(draft));
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
    clearIfUnchanged(text, revision, draftRevision, names) {
      if (disposed || draftEditRevisionRef.current !== revision) return false;
      if (textRef.current === text && sameSkillSelections(skillNamesRef.current, names)) {
        updateText("");
        updateSkillNames([]);
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
