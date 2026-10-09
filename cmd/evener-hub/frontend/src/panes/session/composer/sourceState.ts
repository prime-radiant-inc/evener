import { buildInput, type ComposerMention } from "@evener/appwire-client";
import type {
  DraftBackingIdentity,
  DraftSessionToken,
  DraftSettlement,
  DraftSettlementGuard,
  SubmittedDraft,
} from "@evener/appwire-client/state/mutation";
import { createStore } from "zustand/vanilla";
import {
  type BrowserDraftKey,
  type BrowserDraftRepository,
  browserDraftKey,
  getBrowserDraftRepository,
} from "../../../stores/browserDraftRepository";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import type {
  MutationOutboxIndexedDB,
  RecoveryOrdinaryConsumption,
  RecoveryWriteGuard,
} from "../../../stores/mutationOutboxIndexedDB";
import { trackProjectionWork } from "../../../stores/projectionWork";
import type { InputAttachment } from "../../../stores/threads";
import { durableAttachments, getComposerMutationStorage } from "../../../stores/threads";
import { pushToast } from "../../../widgets/toast/store";
import {
  type AttachmentStore,
  createAttachmentOperations,
  createAttachmentStore,
  type PendingAttachment,
  type TextEditor,
  type TextEditSource,
} from "./attachments/useAttachments";
import { clearRuntimeDraft, markDraftEdited, publishRuntimeDraft, readComposerDraft, readDraftRevision } from "./draft";
import { refreshPendingTurnsProjection, subscribeComposerSubmissionCommitted } from "./queue/pendingTurnsStore";
import { parseSkillDocument, patchSelectionText, type SkillEditorValue, serializeSkillDocument } from "./skillDocument";

type RevisionRef<T> = { current: T };
type SelectionMeta = { commandNames: string[]; mentions: ComposerMention[] | undefined };

// The injected browser repository/outbox a source writes through. Production
// defaults resolve the shared module singletons; tests inject them.
export interface ComposerSourceDeps {
  repository: BrowserDraftRepository;
  key: BrowserDraftKey;
  outbox: MutationOutboxIndexedDB;
}

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
  getLocalGeneration(): number;
  getRuntimeBacking(): DraftBackingIdentity;
  prepareAnnotation(): Promise<ComposerPreparation | null>;
  ownsPreparation(preparation: ComposerPreparation): boolean;
  acknowledgeAnnotation(preparation: ComposerPreparation, token: DraftSessionToken, value: SkillEditorValue): void;
  submissionDraft(value?: SkillEditorValue): Promise<SubmittedDraft>;
  ownsSubmissionSnapshot(submitted: SubmittedDraft, items: readonly PendingAttachment[]): boolean;
  recoveryWriteGuard(submitted: SubmittedDraft): RecoveryWriteGuard;
  acknowledgeRecoveryWrite(guard: RecoveryWriteGuard): RecoveryWriteGuard;
  reconcileMissingRecovery(submitted: SubmittedDraft, items: readonly PendingAttachment[]): Promise<boolean>;
  settleDraft(submitted: SubmittedDraft): Promise<DraftSettlement>;
  dispose(): void;
}

export function sameSkillSelections(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((name, index) => name === right[index]);
}

export function restoredSkillNames(value: SkillEditorValue): string[] {
  return serializeSkillDocument(parseSkillDocument(value)).skillNames;
}

/** Original conversation work belongs to its pane, not to an editor mount. */
export function createComposerSourceState(ref: string, deps?: ComposerSourceDeps): ComposerSourceState {
  const initial = readComposerDraft(ref);
  const state = createStore<ComposerSourceSnapshot>(() => ({
    text: initial.text,
    skillNames: restoredSkillNames(initial),
    commandNames: initial.commandNames ?? [],
    mentions: initial.mentions,
    activeRecoveryId: null,
    freshRecoveryRef: null,
    restoreEpoch: 0,
    submittedAttachmentRetirementPending: false,
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
  let acceptedRecoveryWrites = 0;
  let pendingRecoveryWrite: { id: string; version: number } | null = null;
  let refreshVersion = 0;
  let persistenceScheduled = false;
  const sourceId = crypto.randomUUID();
  let repository = deps?.repository;
  let draftKey = deps?.key;
  let outbox = deps?.outbox;
  let draftToken: DraftSessionToken | null = null;
  let hydrationToken: DraftSessionToken | null = null;
  let localGeneration = 0;
  let runtimeBacking: DraftBackingIdentity = { kind: "draft" };
  const acceptedQueue: SkillEditorValue[] = [];
  let drainPromise: Promise<void> = Promise.resolve();
  let readyPromise: Promise<void> | null = null;
  let pendingRefused = false;
  let needsReconcile = false;
  let reportedDrainFailure = false;
  const retirementHolds = new Set<object>();
  let pendingRetirement: {
    items: PendingAttachment[];
    done: Promise<void>;
    resolve: () => void;
  } | null = null;
  let retirementFlushing = false;
  let retirementPersistenceAttempt: {
    id: string;
    generation: number;
    value: SkillEditorValue;
    items: PendingAttachment[];
  } | null = null;

  function cloneValue(value: SkillEditorValue): SkillEditorValue {
    return {
      text: value.text,
      skillNames: [...value.skillNames],
      ...(value.commandNames === undefined ? {} : { commandNames: [...value.commandNames] }),
      ...(value.mentions === undefined ? {} : { mentions: value.mentions.map((mention) => ({ ...mention })) }),
    };
  }
  // Synchronize the selection-metadata ref from a complete value. Publication is
  // the caller's single responsibility; this performs no store write.
  function mergeSelection(value: SkillEditorValue): void {
    selectionMetaRef.current = {
      commandNames: value.commandNames === undefined ? [] : [...value.commandNames],
      mentions: value.mentions === undefined ? undefined : value.mentions.map((mention) => ({ ...mention })),
    };
  }
  function currentValue(): SkillEditorValue {
    return {
      text: textRef.current,
      skillNames: [...skillNamesRef.current],
      ...(selectionMetaRef.current.commandNames.length
        ? { commandNames: [...selectionMetaRef.current.commandNames] }
        : {}),
      ...(selectionMetaRef.current.mentions
        ? { mentions: selectionMetaRef.current.mentions.map((m) => ({ ...m })) }
        : {}),
    };
  }
  function publishValue(value: SkillEditorValue): void {
    if (disposed) return;
    // Change detection reuses the submission-ownership semantics — text, ordered
    // skill names, normalized command names and mention presence — so absent
    // versus empty mentions stay distinct.
    if (
      textRef.current === value.text &&
      sameSkillSelections(skillNamesRef.current, value.skillNames) &&
      sameSkillSelections(selectionMetaRef.current.commandNames, value.commandNames ?? []) &&
      sameMentions(selectionMetaRef.current.mentions, value.mentions)
    ) {
      // The source store already holds this value, so no store notification is due.
      // The runtime draft projection can still be stale (hydration/transfer, or an
      // accepted edit taking over a guard-protected write), so refresh it through
      // its existing disposal/ownership guard without touching the store.
      syncProjection();
      return;
    }
    // Synchronize every ref BEFORE publication, then publish the complete accepted
    // value and its projection with one store update and one projection update.
    textRef.current = value.text;
    skillNamesRef.current = [...value.skillNames];
    mergeSelection(value);
    state.setState({
      text: textRef.current,
      skillNames: skillNamesRef.current,
      commandNames: selectionMetaRef.current.commandNames,
      mentions: selectionMetaRef.current.mentions,
    });
    syncProjection();
  }
  function storedValue(row: {
    text: string;
    skillNames: readonly string[];
    commandNames?: readonly string[];
    mentions?: readonly ComposerMention[];
  }): SkillEditorValue {
    return cloneValue({
      text: row.text,
      skillNames: [...row.skillNames],
      ...(row.commandNames === undefined ? {} : { commandNames: [...row.commandNames] }),
      ...(row.mentions === undefined ? {} : { mentions: row.mentions.map((m) => ({ ...m })) }),
    });
  }
  function recoveryValue(record: MutationRecoveryRecord): SkillEditorValue {
    const payloadInput = (record.payload as { input?: unknown }).input;
    const input = Array.isArray(payloadInput) ? (payloadInput as Array<Record<string, unknown>>) : [];
    const skillNames = input.filter((item) => item.type === "skill").map((item) => String(item.name));
    const commandNames = input.filter((item) => item.type === "command").map((item) => String(item.name));
    const mentions = record.composerMentions;
    return cloneValue({
      text: record.composerText ?? "",
      skillNames,
      ...(commandNames.length ? { commandNames } : {}),
      ...(mentions === undefined ? {} : { mentions: mentions.map((m) => ({ ...m })) }),
    });
  }
  async function ensureDeps(): Promise<void> {
    if (!repository) repository = getBrowserDraftRepository();
    if (!draftKey) draftKey = browserDraftKey(ref);
    // A retired source must not start the shared mutation runtime: auto-init
    // may still be in flight when the source disposes.
    if (!outbox && !disposed) outbox = await getComposerMutationStorage();
  }
  async function drainAccepted(): Promise<void> {
    const r = repository;
    const k = draftKey;
    if (!r || !k) return;
    // A refused stale write stays queued (so preparation reports unsaved work)
    // until a fresh deliberate edit supersedes it.
    if (pendingRefused) return;
    while (acceptedQueue.length > 0) {
      const entry = acceptedQueue[0];
      if (entry === undefined) break;
      if (needsReconcile || draftToken === null) {
        const row = await r.load(k);
        if (!row) break;
        draftToken = row.token;
        needsReconcile = false;
      }
      const expected = draftToken;
      if (expected === null) break;
      const result = await r.commitEdit(k, expected, entry, () => true);
      if (result.state === "committed") {
        draftToken = result.draft.token;
        reportedDrainFailure = false;
        acceptedQueue.shift();
        continue;
      }
      if (result.state === "conflict") {
        const row = await r.load(k);
        if (!row) break;
        if (row.token.namespaceIncarnation !== expected.namespaceIncarnation) {
          // The namespace fence refuses the captured stale write; keep it queued
          // as unsaved work rather than silently refreshing its token.
          pendingRefused = true;
          draftToken = row.token;
          break;
        }
        draftToken = row.token;
        continue;
      }
      // Storage fault: retain the entry for a bounded later retry and report it
      // once, so the user sees honest durability rather than silent loss.
      if (!reportedDrainFailure) {
        reportedDrainFailure = true;
        pushToast("error", "Couldn't save the draft. Try again.");
      }
      break;
    }
  }
  function scheduleDrain(): void {
    drainPromise = drainPromise.then(() => drainAccepted()).catch(() => undefined);
  }
  function ready(): Promise<void> {
    readyPromise ??= (async () => {
      // Captured before ANY await: explicit work accepted before ready started
      // (and any transfer/load delay) must not be overwritten by hydration.
      const hydrationGeneration = localGeneration;
      // A recovery selection changes the backing WITHOUT an edit, so the
      // generation/queue guard alone cannot see it; hydration must also require
      // the backing it started with.
      const hydrationBacking: DraftBackingIdentity = { ...runtimeBacking };
      await ensureDeps();
      const r = repository;
      const k = draftKey;
      if (!r || !k) return;
      const transfer = await r.transferSupportedDrafts(k.namespace);
      if (transfer.state !== "committed") throw new Error("Draft transfer is pending. Try again.");
      let row = await r.load(k);
      if (!row) {
        const seeded = await r.seedDraft(k, currentValue());
        row = seeded.state === "committed" ? seeded.draft : await r.load(k);
      }
      if (!row) throw new Error("Draft initialization is pending. Try again.");
      if (row) {
        hydrationToken = Object.freeze({ ...row.token });
        draftToken = row.token;
        if (
          !disposed &&
          localGeneration === hydrationGeneration &&
          sameBacking(runtimeBacking, hydrationBacking) &&
          acceptedQueue.length === 0
        ) {
          publishValue(storedValue(row));
        }
      }
      // Hydration's own drain joins the SAME ordered chain as every scheduled
      // drain, so overlapping ready/whenSettled calls cannot commit an entry
      // twice or out of order.
      scheduleDrain();
      await drainPromise;
    })().catch((error) => {
      // Keep the retained source and accepted edits, but let a later deliberate
      // attempt rerun transfer/load instead of awaiting a cached failure.
      readyPromise = null;
      throw error;
    });
    return readyPromise;
  }
  function whenSettled(): Promise<void> {
    return ready()
      .then(() => {
        // A bounded retry: each settle attempt drains whatever remains queued,
        // including entries retained after an earlier storage fault.
        scheduleDrain();
        return drainPromise;
      })
      .catch(() => undefined);
  }
  function acceptEdit(value: SkillEditorValue): void {
    if (disposed) return;
    localGeneration += 1;
    runtimeBacking = { kind: "draft" };
    if (pendingRefused) {
      acceptedQueue.length = 0;
      pendingRefused = false;
      needsReconcile = true;
    }
    acceptedQueue.push(cloneValue(value));
    // An accepted ordinary edit owns the ref's draft revision, exactly as the
    // old synchronous write did: another same-ref source's captured revision is
    // now stale, so its late detached write can no longer claim this draft.
    markDraftEdited(ref);
    ownedDraftRevisionRef.current = readDraftRevision(ref);
    publishValue(value);
    scheduleDrain();
  }
  function getDraftToken(): DraftSessionToken {
    return draftToken ?? { namespaceIncarnation: "", sessionRevision: 0 };
  }
  function sameBacking(left: DraftBackingIdentity, right: DraftBackingIdentity): boolean {
    if (left.kind !== right.kind) return false;
    if (left.kind === "recovery" && right.kind === "recovery") return left.recoveryId === right.recoveryId;
    return true;
  }
  async function prepareAnnotation(): Promise<ComposerPreparation | null> {
    if (disposed) return null;
    await ready();
    await whenSettled();
    await recoveryWrites.catch(() => undefined);
    if (disposed) return null;
    const r = repository;
    const k = draftKey;
    if (!r || !k) return null;
    if (acceptedQueue.length > 0) return null;
    const generation = localGeneration;
    const backing: DraftBackingIdentity = { ...runtimeBacking };
    // The selected recovery is part of the capture: reading it AFTER the durable
    // read would let a backing change during that read go unnoticed.
    const recoveryId = activeRecoveryIdRef.current;
    if (recoveryId !== null && pendingRecoveryWrite?.id === recoveryId) return null;
    const row = await r.load(k);
    if (!row) return null;
    // The capture must still be alive and exactly unchanged after the read,
    // before any token or local publication.
    if (disposed || localGeneration !== generation || !sameBacking(runtimeBacking, backing)) return null;
    if (recoveryId !== null) {
      const record = await outbox?.getRecovery(recoveryId);
      if (!record) return null;
      if (disposed || localGeneration !== generation || !sameBacking(runtimeBacking, backing)) return null;
      if (pendingRecoveryWrite?.id === recoveryId) return null;
      publishValue(recoveryValue(record));
      draftToken = row.token;
      return {
        key: { namespace: { ...k.namespace }, sessionRef: k.sessionRef },
        token: { ...row.token },
        backing: { kind: "recovery", recoveryId },
        localGeneration: generation,
      };
    }
    publishValue(storedValue(row));
    draftToken = row.token;
    return {
      key: { namespace: { ...k.namespace }, sessionRef: k.sessionRef },
      token: { ...row.token },
      backing: { kind: "draft" },
      localGeneration: generation,
    };
  }
  function ownsPreparation(preparation: ComposerPreparation): boolean {
    if (disposed) return false;
    if (localGeneration !== preparation.localGeneration) return false;
    if (!sameBacking(runtimeBacking, preparation.backing)) return false;
    return (
      draftToken !== null &&
      draftToken.namespaceIncarnation === preparation.token.namespaceIncarnation &&
      draftToken.sessionRevision === preparation.token.sessionRevision
    );
  }
  function acknowledgeAnnotation(
    preparation: ComposerPreparation,
    token: DraftSessionToken,
    value: SkillEditorValue,
  ): void {
    if (!ownsPreparation(preparation)) return;
    publishValue(cloneValue(value));
    draftToken = { ...token };
  }
  // Every settlement guard THIS source produced, so the submission-committed
  // listener can tell its OWN capture from another same-ref source's, whose
  // numeric generation and backing may coincide.
  const ownedCaptureGuards = new WeakMap<
    DraftSettlementGuard,
    { replacementEpoch: number; items: PendingAttachment[] }
  >();
  async function submissionDraft(value?: SkillEditorValue): Promise<SubmittedDraft> {
    const payload = cloneValue(value ?? currentValue());
    const entryGeneration = localGeneration;
    const entryBacking: DraftBackingIdentity = { ...runtimeBacking };
    const entryReplacementEpoch = recoveryReplacementEpochRef.current;
    const entryItems = [...attachments.getState().items];
    // The shared per-ref edit revision at entry: another same-ref source's
    // explicit edit since this point advances it.
    const entryEditRevision = readDraftRevision(ref);
    await ready();
    await whenSettled();
    // Accepted recovery work is part of the submission's durable state: drain it
    // before capturing the guard so the guard matches the durable row.
    await recoveryWrites.catch(() => undefined);
    const submissionRepository = repository;
    const submissionKey = draftKey;
    if (submissionRepository && submissionKey) {
      const row = await submissionRepository.load(submissionKey);
      if (row) draftToken = row.token;
    }
    const token = getDraftToken();
    const settlementGuard: DraftSettlementGuard = {
      token: { ...token },
      backing: entryBacking,
      localGeneration: entryGeneration,
    };
    ownedCaptureGuards.set(settlementGuard, { replacementEpoch: entryReplacementEpoch, items: entryItems });
    return {
      draftRevisionAtStart: entryEditRevision,
      text: payload.text,
      skillNames: payload.skillNames,
      ...(payload.commandNames === undefined ? {} : { commandNames: payload.commandNames }),
      ...(payload.mentions === undefined ? {} : { mentions: payload.mentions }),
      settlementGuard,
      stillOwnsEdit: () =>
        !disposed &&
        localGeneration === entryGeneration &&
        sameBacking(runtimeBacking, entryBacking) &&
        (draftToken === null ||
          (draftToken.namespaceIncarnation === token.namespaceIncarnation &&
            draftToken.sessionRevision === token.sessionRevision)),
    };
  }
  function recoveryWriteGuard(submitted: SubmittedDraft): RecoveryWriteGuard {
    const key = draftKey ?? browserDraftKey(ref);
    const token = submitted.settlementGuard?.token ?? { namespaceIncarnation: "", sessionRevision: 0 };
    return Object.freeze({
      key: Object.freeze({ namespace: Object.freeze({ ...key.namespace }), sessionRef: key.sessionRef }),
      token: Object.freeze({ ...token }),
      stillAuthorized: () => ownsOriginalLocalCapture(submitted) && submitted.stillOwnsEdit?.() === true,
    });
  }
  function ownsSubmissionSnapshot(submitted: SubmittedDraft, items: readonly PendingAttachment[]): boolean {
    const guard = submitted.settlementGuard;
    const currentItems = attachments.getState().items;
    return (
      ownsDraft() &&
      ownsOriginalLocalCapture(submitted) &&
      guard !== undefined &&
      submitted.stillOwnsEdit?.() === true &&
      recoveryReplacementEpochRef.current === ownedCaptureGuards.get(guard)?.replacementEpoch &&
      submitted.draftRevisionAtStart === readDraftRevision(ref) &&
      sameSubmittedFullValue(submitted) &&
      currentItems.length === items.length &&
      currentItems.every((item, index) => item === items[index])
    );
  }
  async function reconcileMissingRecovery(
    submitted: SubmittedDraft,
    items: readonly PendingAttachment[],
  ): Promise<boolean> {
    const guard = submitted.settlementGuard;
    const box = outbox;
    if (guard?.backing.kind !== "recovery" || !box) return false;
    const recoveryId = guard.backing.recoveryId;
    const capturedItems = [...items];
    const stillOwnsSnapshot = (): boolean =>
      ownsOriginalLocalCapture(submitted) &&
      recoveryReplacementEpochRef.current === ownedCaptureGuards.get(guard)?.replacementEpoch &&
      activeRecoveryIdRef.current === recoveryId &&
      submitted.draftRevisionAtStart === readDraftRevision(ref) &&
      sameSubmittedFullValue(submitted) &&
      operations.items.length === capturedItems.length &&
      operations.items.every((item, index) => item === capturedItems[index]);
    if (!stillOwnsSnapshot()) return false;
    try {
      if ((await box.getRecovery(recoveryId)) !== undefined || !stillOwnsSnapshot()) return false;
    } catch {
      // A failed presence read is not evidence that recovery is absent.
      return false;
    }
    // Current absence retires only this unchanged local copy. It proves neither
    // RPC acceptance nor permission for another durable clear or notification.
    recoveryOwnsLocalDraftRef.current = false;
    recoveryWriteVersionRef.current += 1;
    recoveryReplacementEpochRef.current += 1;
    setActiveRecoveryId(null);
    updateText("");
    updateSkillNames([]);
    updateSelectionMeta({ text: "", skillNames: [] });
    operations.reset();
    return true;
  }
  async function settleDraft(submitted: SubmittedDraft): Promise<DraftSettlement> {
    if (disposed) return { cleared: false, draftUnchanged: false, persistence: "committed" };
    const guard = submitted.settlementGuard;
    if (!guard) return { cleared: false, draftUnchanged: false, persistence: "committed" };
    if (submitted.stillOwnsEdit && !submitted.stillOwnsEdit()) {
      // An edit already present at settlement entry keeps its full value, but
      // this sender's accepted resend has consumed the old recovery backing.
      // Release it without a read, token refresh, attachment clear or draft save.
      if (
        guard.backing.kind === "recovery" &&
        isOwnCapture(submitted) &&
        recoveryReplacementEpochRef.current === ownedCaptureGuards.get(guard)?.replacementEpoch &&
        activeRecoveryIdRef.current === guard.backing.recoveryId
      ) {
        recoveryOwnsLocalDraftRef.current = false;
        setActiveRecoveryId(null);
      }
      return { cleared: false, draftUnchanged: false, persistence: "committed" };
    }
    // Local publication is permitted only while the ORIGINAL capture (its own
    // generation and backing) is still exactly current after every await. The
    // captured token is deliberately not re-tested against the advanced
    // committed token, and the original guard is never refreshed; a durable
    // clear is a separate fact from permission to publish local empty state.
    const stillOwnsLocally = (): boolean =>
      !disposed && localGeneration === guard.localGeneration && sameBacking(runtimeBacking, guard.backing);
    const r = repository;
    const k = draftKey;
    if (guard.backing.kind === "recovery") {
      const capture = ownedCaptureGuards.get(guard);
      const row = r && k ? await r.load(k) : null;
      if (capture && ownsSubmissionSnapshot(submitted, capture.items)) {
        if (row) draftToken = row.token;
        activeRecoveryIdRef.current = null;
        state.setState({ activeRecoveryId: null });
        updateText("");
        updateSkillNames([]);
        updateSelectionMeta({ text: "", skillNames: [] });
        runtimeBacking = { kind: "draft" };
      }
      return { cleared: true, draftUnchanged: true, persistence: "committed" };
    }
    if (!r || !k) return { cleared: false, draftUnchanged: false, persistence: "committed" };
    // Ordinary per-call cleanup authorization: a same-ref explicit edit that
    // landed after this submission's entry advanced the shared ref edit
    // revision past the captured one, so another source owns the draft now.
    // Refusing the durable clear here keeps its committed value; the accepted
    // input was already sent. This mirrors the revision half of
    // settleSubmittedDraft's "no edit landed while the submission was in flight"
    // check, which the durable-token fence alone cannot see when the competing
    // write carried this source's identical post-drain token.
    if (submitted.draftRevisionAtStart !== readDraftRevision(ref)) {
      return { cleared: false, draftUnchanged: false, persistence: "committed" };
    }
    const settlement = await r.clearSubmitted(k, submitted);
    if (settlement.cleared) {
      const row = await r.load(k);
      if (stillOwnsLocally()) {
        if (row) draftToken = row.token;
        updateText("");
        updateSkillNames([]);
        updateSelectionMeta({ text: "", skillNames: [] });
        runtimeBacking = { kind: "draft" };
      }
    }
    return settlement;
  }

  const ownsDraft = () => ownedDraftRevisionRef.current === readDraftRevision(ref);
  function currentDraftText(): string {
    return ownsDraft() ? textRef.current : readComposerDraft(ref).text;
  }
  function updateText(text: string): void {
    if (disposed) return;
    textRef.current = text;
    state.setState({ text });
    syncProjection();
  }
  function updateSkillNames(skillNames: string[]): void {
    if (disposed) return;
    skillNamesRef.current = skillNames;
    state.setState({ skillNames });
    syncProjection();
  }
  function updateSelectionMeta(value: SkillEditorValue): void {
    if (disposed) return;
    const next = { commandNames: value.commandNames ?? [], mentions: value.mentions };
    selectionMetaRef.current = next;
    state.setState(next);
    syncProjection();
  }
  // The runtime read projection mirrors the composer's visible value: every
  // state write republishes it, so a settle/clear is reflected too.
  function syncProjection(): void {
    // A non-owning source may still update its own visible editor (a submitted
    // cleanup or encode failure), but it must not publish over the projection a
    // newer same-ref source owns. An explicit edit reclaims ownership through
    // acceptEdit (which acquires the revision before publishing) first.
    if (disposed || !ownsDraft()) return;
    publishRuntimeDraft(ref, currentValue(), sourceId);
  }
  function markEdited(): void {
    // Any real edit invalidates a capture taken before it, including a recovery
    // edit: the generation counts local edits, and bumping it here never turns a
    // recovery selection into ordinary backing.
    localGeneration += 1;
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
    persistDetachedRecovery();
  }
  function editSkillNames(names: string[]): void {
    if (disposed) return;
    markEdited();
    updateSkillNames(names);
    persistDetachedRecovery();
  }
  function setActiveRecoveryId(activeRecoveryId: string | null): void {
    if (disposed) return;
    activeRecoveryIdRef.current = activeRecoveryId;
    runtimeBacking = activeRecoveryId === null ? { kind: "draft" } : { kind: "recovery", recoveryId: activeRecoveryId };
    state.setState({ activeRecoveryId });
  }
  function persistDraft(text: string): void {
    if (disposed) return;
    acceptEdit({
      text,
      skillNames: [...skillNamesRef.current],
      ...(selectionMetaRef.current.commandNames.length
        ? { commandNames: [...selectionMetaRef.current.commandNames] }
        : {}),
      ...(selectionMetaRef.current.mentions
        ? { mentions: selectionMetaRef.current.mentions.map((m) => ({ ...m })) }
        : {}),
    });
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
    if (mayPersist) {
      if (activeRecoveryIdRef.current === null) persistDraft(text);
      else persistDetachedRecovery();
    }
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

  function holdSubmittedAttachmentRetirement(): () => Promise<void> {
    const hold = {};
    if (!disposed) retirementHolds.add(hold);
    let released: Promise<void> | undefined;
    return () => {
      if (released) return released;
      retirementHolds.delete(hold);
      released = pendingRetirement?.done ?? Promise.resolve();
      if (!disposed && retirementHolds.size === 0 && pendingRetirement && !retirementFlushing)
        void flushSubmittedAttachmentRetirement();
      return released;
    };
  }

  function clearSubmittedAttachments(items: PendingAttachment[]): Promise<void> {
    if (disposed) return Promise.resolve();
    if (retirementHolds.size > 0 || retirementFlushing) {
      if (!pendingRetirement) {
        let resolve!: () => void;
        const done = new Promise<void>((finish) => {
          resolve = finish;
        });
        pendingRetirement = { items: [], done, resolve };
        state.setState({ submittedAttachmentRetirementPending: true });
      }
      // Copy the array at entry, retaining each exact submitted object.
      pendingRetirement.items.push(...items);
      return pendingRetirement.done;
    }
    const markers = new Set(operations.items.filter((item) => items.includes(item)).map((item) => item.marker));
    if (markers.size > 0) operations.clearSubmitted(markers);
    return Promise.resolve();
  }

  async function flushSubmittedAttachmentRetirement(): Promise<void> {
    const retirement = pendingRetirement;
    if (!retirement || retirementFlushing) return;
    retirementFlushing = true;
    try {
      while (!disposed && retirementHolds.size === 0 && retirement.items.length > 0) {
        const submitted = retirement.items.splice(0);
        const markers = new Set(operations.items.filter((item) => submitted.includes(item)).map((item) => item.marker));
        if (markers.size === 0) continue;
        operations.clearSubmitted(markers);
        const id = activeRecoveryIdRef.current;
        if (id === null) {
          if (ownsDraft()) await whenSettled();
        } else {
          const value = cloneValue(currentValue());
          retirementPersistenceAttempt = {
            id,
            generation: localGeneration,
            value,
            items: [...operations.items],
          };
          // Capture only the current frozen authority. A refused primary never
          // earns a read/rebase, another ordinary consumption or acceptance.
          await queueRecoveryPersistence(
            id,
            value.text,
            operations.toInputAttachments(),
            value.skillNames,
            value.commandNames,
            value.mentions,
          ).catch(() => undefined);
        }
      }
    } finally {
      retirementFlushing = false;
      if (pendingRetirement === retirement && retirement.items.length === 0) {
        pendingRetirement = null;
        if (!disposed) state.setState({ submittedAttachmentRetirementPending: false });
      }
      if (pendingRetirement !== retirement) retirement.resolve();
      // A detached real edit arriving during the joined write remains live.
      // The identical retirement publication is suppressed below, even on fault.
      persistDetachedRecovery();
    }
  }

  function persistRecoveryEffect(
    id: string,
    text: string,
    nextAttachments: InputAttachment[],
    names: readonly string[],
    commands: readonly string[] = selectionMetaRef.current.commandNames,
    mentions: readonly ComposerMention[] | undefined = selectionMetaRef.current.mentions,
  ): Promise<void> {
    if (pendingRetirement) return Promise.resolve();
    const attempted = retirementPersistenceAttempt;
    const items = operations.items;
    if (
      attempted &&
      attempted.id === id &&
      attempted.generation === localGeneration &&
      attempted.value.text === text &&
      sameSkillSelections(attempted.value.skillNames, names) &&
      sameSkillSelections(attempted.value.commandNames ?? [], commands) &&
      sameMentions(attempted.value.mentions, mentions) &&
      attempted.items.length === items.length &&
      items.every((item, index) => item === attempted.items[index])
    )
      return Promise.resolve();
    return queueRecoveryPersistence(id, text, nextAttachments, names, commands, mentions);
  }

  function acknowledgeRecoveryWrite(guard: RecoveryWriteGuard): RecoveryWriteGuard {
    // Guarded storage writes advance exactly once. Never load a later row here:
    // a competitor's token is not authority for the continuation of this write.
    const token = Object.freeze({ ...guard.token, sessionRevision: guard.token.sessionRevision + 1 });
    acceptedRecoveryWrites += 1;
    draftToken = token;
    return Object.freeze({ ...guard, token });
  }

  function queueRecoveryPersistence(
    clientMutationId: string,
    nextText: string,
    nextAttachments: InputAttachment[],
    nextSkillNames: readonly string[],
    nextCommandNames: readonly string[] = selectionMetaRef.current.commandNames,
    nextMentions: readonly ComposerMention[] | undefined = selectionMetaRef.current.mentions,
    consumeOrdinary?: RecoveryOrdinaryConsumption,
    onPersisted?: (guard: RecoveryWriteGuard) => void,
  ): Promise<void> {
    if (disposed) return Promise.resolve();
    // Capture the accepted value and metadata NOW, before any queued yield: a
    // caller that mutates its arrays afterwards must not change what is written.
    const acceptedText = nextText;
    const acceptedAttachments = nextAttachments.map((attachment) => ({ ...attachment }));
    const acceptedSkillNames = [...nextSkillNames];
    const acceptedCommandNames = [...nextCommandNames];
    const acceptedMentions = nextMentions === undefined ? undefined : nextMentions.map((mention) => ({ ...mention }));
    const consumption = consumeOrdinary && {
      token: { ...consumeOrdinary.token },
      value: cloneValue(consumeOrdinary.value),
      stillOwnsValue: consumeOrdinary.stillOwnsValue,
    };
    const version = ++recoveryWriteVersionRef.current;
    // A settled promise is not proof of a save: refusals and aborts leave the
    // latest accepted value unsaved until its existing public retry commits.
    pendingRecoveryWrite = { id: clientMutationId, version };
    const replacementEpoch = recoveryReplacementEpochRef.current;
    const draftRevision = readDraftRevision(ref);
    const key = draftKey ?? browserDraftKey(ref);
    const capturedKey = Object.freeze({
      namespace: Object.freeze({ ...key.namespace }),
      sessionRef: key.sessionRef,
    });
    const capturedToken = draftToken && Object.freeze({ ...draftToken });
    const commitsAtCapture = acceptedRecoveryWrites;
    const backing = { ...runtimeBacking };
    const operation = recoveryWrites
      .catch(() => undefined)
      .then(() =>
        trackProjectionWork(
          (async () => {
            // A startup activation can precede initial hydration. Establish its
            // first authority once, never reload an already captured token.
            if (!capturedToken) await ready();
            const token = capturedToken ?? hydrationToken;
            if (!token) return;
            if (disposed || activeRecoveryIdRef.current !== clientMutationId) return;
            const guard: RecoveryWriteGuard = Object.freeze({
              key: capturedKey,
              // Already accepted writes queued ahead of this one may advance its
              // own captured revision, but external reads/commits never may.
              token: Object.freeze({
                ...token,
                sessionRevision: token.sessionRevision + acceptedRecoveryWrites - commitsAtCapture,
              }),
              stillAuthorized: () =>
                !disposed &&
                ownsDraft() &&
                recoveryReplacementEpochRef.current === replacementEpoch &&
                activeRecoveryIdRef.current === clientMutationId &&
                sameBacking(runtimeBacking, backing),
            });
            if (
              acceptedText.trim() === "" &&
              acceptedAttachments.length === 0 &&
              acceptedSkillNames.length === 0 &&
              acceptedCommandNames.length === 0
            ) {
              if (
                textRef.current.trim() !== "" ||
                operations.items.length > 0 ||
                skillNamesRef.current.length > 0 ||
                selectionMetaRef.current.commandNames.length > 0
              )
                return;
              const discarded = await outbox?.discardRecovery(
                clientMutationId,
                () =>
                  !disposed &&
                  recoveryReplacementEpochRef.current === replacementEpoch &&
                  activeRecoveryIdRef.current === clientMutationId &&
                  textRef.current.trim() === "" &&
                  operations.items.length === 0 &&
                  skillNamesRef.current.length === 0 &&
                  selectionMetaRef.current.commandNames.length === 0,
                guard,
              );
              void refreshPendingTurnsProjection(ref);
              if (!discarded) return;
              const persistedGuard = acknowledgeRecoveryWrite(guard);
              if (pendingRecoveryWrite?.version === version) pendingRecoveryWrite = null;
              onPersisted?.(persistedGuard);
              if (
                !disposed &&
                activeRecoveryIdRef.current === clientMutationId &&
                readDraftRevision(ref) === draftRevision &&
                textRef.current.trim() === "" &&
                operations.items.length === 0 &&
                skillNamesRef.current.length === 0 &&
                selectionMetaRef.current.commandNames.length === 0
              ) {
                recoveryOwnsLocalDraftRef.current = false;
                setActiveRecoveryId(null);
              }
              return;
            }
            const record = await outbox?.updateRecoveryInput(
              clientMutationId,
              buildInput(acceptedText, acceptedAttachments, acceptedSkillNames, acceptedCommandNames, acceptedMentions),
              durableAttachments(acceptedAttachments),
              acceptedText,
              acceptedMentions,
              guard,
              consumption,
            );
            void refreshPendingTurnsProjection(ref);
            const updated = record !== undefined;
            if (updated) {
              const persistedGuard = acknowledgeRecoveryWrite(guard);
              if (pendingRecoveryWrite?.version === version) pendingRecoveryWrite = null;
              onPersisted?.(persistedGuard);
            }
            if (
              !disposed &&
              updated &&
              recoveryOwnsLocalDraftRef.current &&
              recoveryWriteVersionRef.current === version &&
              readDraftRevision(ref) === draftRevision &&
              activeRecoveryIdRef.current === clientMutationId
            ) {
              recoveryOwnsLocalDraftRef.current = false;
            }
          })(),
        ),
      );
    recoveryWrites = operation;
    void operation.catch((error) => {
      if (disposed || activeRecoveryIdRef.current !== clientMutationId) return;
      pushToast("error", `Couldn't save recovered message: ${error instanceof Error ? error.message : String(error)}`);
    });
    return operation;
  }

  // True when `submitted` is a capture THIS source produced, matched by exact
  // guard identity rather than by a numeric generation/backing coincidence that
  // another same-ref source can also satisfy.
  function isOwnCapture(submitted: SubmittedDraft): boolean {
    const guard = submitted.settlementGuard;
    return guard !== undefined && ownedCaptureGuards.has(guard);
  }

  // True while THIS source still holds the exact local generation and backing
  // the submission was captured from: only the original sender's own live
  // ownership may retire its local payload or clear it on a committed veto.
  function ownsOriginalLocalCapture(submitted: SubmittedDraft): boolean {
    const guard = submitted.settlementGuard;
    if (disposed || !guard || !ownedCaptureGuards.has(guard)) return false;
    return localGeneration === guard.localGeneration && sameBacking(runtimeBacking, guard.backing);
  }

  // The captured FULL value, including mention presence: an absent mention set
  // and an explicit empty one are different values.
  function sameMentions(
    left: readonly ComposerMention[] | undefined,
    right: readonly ComposerMention[] | undefined,
  ): boolean {
    if (left === undefined || right === undefined) return left === undefined && right === undefined;
    if (left.length !== right.length) return false;
    for (let index = 0; index < left.length; index += 1) {
      const a = left[index];
      const b = right[index];
      if (a === undefined || b === undefined) return false;
      if (a.kind !== b.kind || a.name !== b.name || a.offset !== b.offset) return false;
    }
    return true;
  }
  function sameSubmittedFullValue(submitted: SubmittedDraft): boolean {
    return (
      textRef.current === submitted.text &&
      sameSkillSelections(skillNamesRef.current, submitted.skillNames) &&
      sameSkillSelections(selectionMetaRef.current.commandNames, submitted.commandNames ?? []) &&
      sameMentions(selectionMetaRef.current.mentions, submitted.mentions)
    );
  }

  const unsubscribeCommitted = subscribeComposerSubmissionCommitted(
    (targetRef, _submittedText, _submittedSkillNames, recovery, _submittedCommandNames, context) => {
      if (disposed || targetRef !== ref) return;
      if (recovery && activeRecoveryIdRef.current === recovery.clientMutationId) {
        const submitted = context?.submitted;
        const capture = submitted?.settlementGuard && ownedCaptureGuards.get(submitted.settlementGuard);
        if (
          !submitted ||
          !capture ||
          context.settlement.persistence !== "committed" ||
          !ownsSubmissionSnapshot(submitted, capture.items)
        )
          return;
        // Only exact live objects from this source's original capture may retire.
        // Acceptance already persisted, this broadcast must not clear again.
        void clearSubmittedAttachments(capture.items);
        recoveryOwnsLocalDraftRef.current = false;
        recoveryWriteVersionRef.current += 1;
        recoveryReplacementEpochRef.current += 1;
        setActiveRecoveryId(null);
        updateText("");
        updateSkillNames([]);
        updateSelectionMeta({ text: "", skillNames: [] });
      } else if (
        !recovery &&
        activeRecoveryIdRef.current === null &&
        // The sixth notification argument always carries the immutable submitted
        // capture now; a missing context is never permission to clear.
        context?.submitted !== undefined &&
        // The captured FULL value - text, skills, commands AND mention presence -
        // must still match this source's current value.
        sameSubmittedFullValue(context.submitted) &&
        (context.settlement.cleared
          ? // The durable row really was cleared: any live source for this ref
            // (including a remount) publishes the cleared value, but only while
            // no newer same-ref edit advanced the shared revision. The ORIGINAL
            // capture's own source must ALSO still hold its unchanged capture,
            // so a same-text local edit landing after the clear is preserved.
            context.submitted.draftRevisionAtStart === readDraftRevision(ref) &&
            (!isOwnCapture(context.submitted) || ownsOriginalLocalCapture(context.submitted))
          : // No durable clear. A committed veto from a competing same-ref edit
            // still lets the ORIGINAL sender's own live instance clear its own
            // unchanged payload; a pending (storage-failed) cleanup clears
            // nothing. settleDraft keeps the durable veto.
            context.settlement.persistence !== "pending" && ownsOriginalLocalCapture(context.submitted))
      ) {
        // Claiming the shared revision is only correct when the durable row was
        // actually cleared. A committed-but-vetoed cleanup (another same-ref
        // pane advanced the revision) clears only THIS source's local editor
        // and must not publish an empty projection over the pane that owns it.
        if (context.settlement.cleared) ownedDraftRevisionRef.current = readDraftRevision(ref);
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
        void persistRecoveryEffect(id, textRef.current, operations.toInputAttachments(), skillNamesRef.current);
    });
  }
  const unsubscribeAttachments = attachments.subscribe(persistDetachedRecovery);

  // Production defaults start on their own: a caller that never invokes ready()
  // still gets hydration and a durable save of accepted edits. An initialization
  // fault is reported and leaves the source usable; it never rejects unobserved.
  void ready().catch(() => {
    if (!disposed) pushToast("error", "Couldn't load the draft. Try again.");
  });

  return {
    ref,
    sourceId,
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
    persistRecoveryEffect,
    holdSubmittedAttachmentRetirement,
    clearSubmittedAttachments,
    ready,
    whenSettled,
    acceptEdit,
    getDraftToken,
    getLocalGeneration: () => localGeneration,
    getRuntimeBacking: () => ({ ...runtimeBacking }),
    prepareAnnotation,
    ownsPreparation,
    acknowledgeAnnotation,
    submissionDraft,
    ownsSubmissionSnapshot,
    recoveryWriteGuard,
    acknowledgeRecoveryWrite,
    reconcileMissingRecovery,
    settleDraft,
    bindEditor(nextEditor) {
      if (disposed) return () => {};
      if (!ownsDraft() && activeRecoveryIdRef.current === null) {
        const draft = readComposerDraft(ref);
        updateText(draft.text);
        updateSkillNames(restoredSkillNames(draft));
        updateSelectionMeta(draft);
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
          markDraftEdited(ref);
          ownedDraftRevisionRef.current = readDraftRevision(ref);
        }
      }
      return true;
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      retirementHolds.clear();
      retirementPersistenceAttempt = null;
      if (pendingRetirement) {
        pendingRetirement.items.length = 0;
        pendingRetirement.resolve();
        pendingRetirement = null;
      }
      boundEditor = null;
      localGeneration += 1;
      recoveryWriteVersionRef.current += 1;
      recoveryReplacementEpochRef.current += 1;
      unsubscribeCommitted();
      unsubscribeAttachments();
      clearRuntimeDraft(ref, sourceId);
      operations.reset();
    },
  };
}
