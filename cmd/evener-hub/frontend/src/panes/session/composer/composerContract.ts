// The composer contract the durable stores compile against: the source
// interface and its value shapes, extracted so the IndexedDB draft and
// annotation stores land without the composer's machinery. The
// implementation (createComposerSourceState) re-exports these for its
// consumers, and every signature stays defined here so the stores and the
// machinery can never drift apart.
import type { ComposerMention, InputAttachment } from "@evener/appwire-client";
import type {
  DraftBackingIdentity,
  DraftSessionToken,
  DraftSettlement,
  SubmittedDraft,
} from "@evener/appwire-client/state/mutation";
import type { BrowserDraftKey } from "../../../stores/browserDraftRepository";
import type { RecoveryOrdinaryConsumption, RecoveryWriteGuard } from "../../../stores/recoveryInput";
import type { AttachmentStore, PendingAttachment, TextEditor, TextEditSource } from "./attachments/useAttachments";
import type { SkillEditorValue } from "./skillDocument";

type RevisionRef<T> = { current: T };
type SelectionMeta = { commandNames: string[]; mentions: ComposerMention[] | undefined };

// What one annotation preparation captured: the durable session key, the token
// at capture time, the backing it was prepared against and the local generation.
export interface ComposerPreparation {
  key: BrowserDraftKey;
  token: DraftSessionToken;
  backing: DraftBackingIdentity;
  localGeneration: number;
}
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
  freshRecoveryRef: string | null;
  restoreEpoch: number;
  submittedAttachmentRetirementPending: boolean;
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
  queueRecoveryPersistence(
    id: string,
    text: string,
    attachments: InputAttachment[],
    names: readonly string[],
    commands?: readonly string[],
    mentions?: readonly ComposerMention[],
    consumeOrdinary?: RecoveryOrdinaryConsumption,
    onPersisted?: (guard: RecoveryWriteGuard) => void,
  ): Promise<void>;
  /** Fires after a recovery edit's persistence commits (update or discard); cleared at disposal. */
  subscribeRecoveryPersisted(listener: () => void): () => void;
  clearIfUnchanged(
    text: string,
    revision: number,
    draftRevision: number,
    names: readonly string[],
    commands?: readonly string[],
  ): boolean;
  clearSubmittedAttachments(items: PendingAttachment[]): Promise<void>;
  holdSubmittedAttachmentRetirement(): () => Promise<void>;
  persistRecoveryEffect(
    id: string,
    text: string,
    attachments: InputAttachment[],
    names: readonly string[],
    commands?: readonly string[],
    mentions?: readonly ComposerMention[],
  ): Promise<void>;
  /** A runtime UUID identifying this source instance; never a pane record field. */
  readonly sourceId: string;
  /** Resolves once transfer, metadata load and any queued accepted saves settle. */
  ready(): Promise<void>;
  /** Resolves after the current drain attempt, even on storage failure. */
  whenSettled(): Promise<void>;
  /** Accepts one explicit full editor value as an immutable durable edit. */
  acceptEdit(value: SkillEditorValue): void;
  getDraftToken(): DraftSessionToken;
  /**
   * The durable authority a deliberate press presents. It is adopted into this
   * source's editing lineage only while the durable backing still holds exactly
   * the value this editor shows.
   */
  currentDraftToken(): Promise<DraftSessionToken>;
  getLocalGeneration(): number;
  getRuntimeBacking(): DraftBackingIdentity;
  prepareAnnotation(): Promise<ComposerPreparation | null>;
  ownsPreparation(preparation: ComposerPreparation): boolean;
  // The value a recovery append committed carries the record's own
  // attachments; the acknowledgment reconciles the staged set with them.
  acknowledgeAnnotation(
    preparation: ComposerPreparation,
    token: DraftSessionToken,
    value: SkillEditorValue & { attachments?: readonly PendingAttachment[] },
  ): void;
  submissionDraft(value?: SkillEditorValue, opts?: { tolerateStorageFault?: boolean }): Promise<SubmittedDraft>;
  ownsSubmissionSnapshot(submitted: SubmittedDraft, items: readonly PendingAttachment[]): boolean;
  recoveryWriteGuard(submitted: SubmittedDraft): RecoveryWriteGuard;
  acknowledgeRecoveryWrite(guard: RecoveryWriteGuard): RecoveryWriteGuard;
  reconcileMissingRecovery(submitted: SubmittedDraft, items: readonly PendingAttachment[]): Promise<boolean>;
  settleDraft(submitted: SubmittedDraft, opts?: { command?: boolean }): Promise<DraftSettlement>;
  dispose(): void;
}
