import { type ComposerMention, NO_ACTIVE_TURN, sessionActionError } from "@evener/appwire-client";
import { ownPendingSend } from "@evener/appwire-client/state/mutation";
import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { controlsFor, liveThreadModel, pressLocalRecoveryFenced, pressRefusal } from "../../../stores/liveControls";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import {
  captureRecoveryStopBarrier,
  hasBlockedUnknown,
  hasQueuedNonSend,
  type InputAttachment,
  threadsStore,
  useThreadsStore,
} from "../../../stores/threads";
import { useToasts } from "../../../widgets";
import { createAttachmentOperations, useAttachments } from "./attachments/useAttachments";
import { STEER_RECOVERY_FENCED_REASON } from "./controlTooltips";
import { clearDraft, clearPersistedDraft, readDraftRevision, readRecoverySelection } from "./draft";
import { pendingTurnEntries, type QueueStripProps, submitWithPendingTracking, usePendingTurnEntries } from "./queue";
import {
  resendRecoveryPendingTurn,
  useBlockedMutationEntries,
  useComposerSubmitting,
  useRecoveryEntries,
} from "./queue/pendingTurnsStore";
import { mergeRecoveryComposerDraft, recoveryComposerDraft } from "./recovery/recoveryDraft";
import {
  materializeSelectionReferences,
  parseSkillDocument,
  patchSelectionText,
  serializeSkillDocument,
  skillAtomMentions,
} from "./skillDocument";
import {
  beginRecoverySubmission,
  type ComposerSourceState,
  recoverySubmissionInFlight,
  restoredSkillNames,
  sameSkillSelections,
} from "./sourceState";
import { composerAvailability } from "./submissionAvailability";
import { pushRecoveryRefusal, pushSelectionRefusal, selectionRefusal, submitDraftToStore } from "./submitProtocol";

type BusyAction = "submit" | "steer" | "interrupt" | "drain" | null;

/** Shared actions retain source ownership and the thread store's mutation policy. */
export function useComposerActions(source: ComposerSourceState, externalBusy = false) {
  const ref = source.ref;
  const model = useThreadsStore((s) => s.threads.get(ref));
  const recoveryRequired = useThreadsStore((s) => s.restartBlockingObligations.has(ref));
  // Delivery-uncertain rows do NOT fence Send here: they clear the resume-only
  // fold's uncertainMessages clause (isResumeOnly, read from
  // hasBlockedUnknown), so a local fenced session falls to the Send-resumes
  // face, where the press parks its send and the store drives the resume.
  const blockedMutations = useBlockedMutationEntries(ref);
  // A Force stop this page started is still draining: the hub refuses even
  // turn/start for that window, so the resume-only carve-out does not apply.
  const stopping = useThreadsStore((s) => s.stoppingRefs.has(ref));
  // The mutation-recency mark the Send-resumes face reads (stores/liveControls
  // .ts's isSendResumes): a local snapshot whose parent still owns its
  // uncertain rows is not Send-driven. Read reactively, like the fence around
  // it, so the badge clears when a resume makes the snapshot authoritative.
  const mutationAuthoritative = useThreadsStore((s) => s.mutationAuthorityRefs.has(ref));
  const mutationWriteStalled = useThreadsStore((s) => s.mutationWriteStalled);
  const submitting = useComposerSubmitting(ref);
  const pendingSendEntries = usePendingTurnEntries(ref, "send");
  const toasts = useToasts();
  const {
    text,
    skillNames,
    commandNames,
    mentions,
    activeRecoveryId,
    recoveryActivating,
    recoveryOperationBusy,
    builtinOperationBusy,
    freshRecoveryRef,
  } = useSyncExternalStore(source.subscribe, source.getSnapshot, source.getSnapshot);
  const {
    lastDrainSnapshotRef,
    draftEditRevisionRef,
    ownedDraftRevisionRef,
    textRef,
    skillNamesRef,
    selectionMetaRef,
    recoveryOwnerLeaseRef,
    recoveryOwnsLocalDraftRef,
  } = source.continuity;
  const {
    updateText,
    updateSkillNames,
    editSkillNames,
    updateSelectionMeta,
    setActiveRecoveryId,
    markRestore,
    queueRecoveryPersistence,
    clearIfUnchanged,
    clearSubmittedAttachments,
  } = source;
  const observedAttachments = useAttachments(source.editor, source.attachments);
  const attachments = useMemo(() => createAttachmentOperations(source.editor, source.attachments), [source]);
  const recoveryEntries = useRecoveryEntries(ref);
  const selectionMeta = useMemo(() => ({ commandNames, mentions }), [commandNames, mentions]);
  const [busyAction, updateBusyAction] = useState<BusyAction>(null);
  const busyActionRef = useRef<BusyAction>(null);
  const externalBusyRef = useRef(externalBusy);
  externalBusyRef.current = externalBusy;
  const submittingRef = useRef(submitting);
  submittingRef.current = submitting;
  const mountedRef = useRef(false);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  function setBusyAction(action: BusyAction): void {
    busyActionRef.current = action;
    if (mountedRef.current) updateBusyAction(action);
  }
  function isBusy(): boolean {
    const snapshot = source.getSnapshot();
    return (
      !source.alive ||
      externalBusyRef.current ||
      busyActionRef.current !== null ||
      submittingRef.current ||
      threadsStore.getState().mutationWriteStalled ||
      snapshot.recoveryActivating ||
      snapshot.recoveryOperationBusy ||
      snapshot.builtinOperationBusy
    );
  }
  const busy =
    externalBusy ||
    busyAction !== null ||
    submitting ||
    mutationWriteStalled ||
    recoveryActivating ||
    recoveryOperationBusy ||
    builtinOperationBusy;
  const availability = model
    ? composerAvailability(model, ownPendingSend(pendingSendEntries), recoveryRequired, {
        uncertainMessages: blockedMutations.length > 0,
        stopInFlight: stopping,
        queuedNonSend: hasQueuedNonSend(ref),
        mutationStateAuthoritative: mutationAuthoritative,
      })
    : { canSend: false, canQueue: false };
  useEffect(() => {
    source.refreshRecovery();
  }, [source]);

  // A store-driven resume failure (the Send-resumes face) surfaces as a toast
  // naming the reason: a store cannot push one, so it publishes the failure and
  // this composer - the press surface, mounted whenever a session pane is -
  // reports it. The toast therefore needs a mounted composer; with none, the
  // failure is only in the store. The pressed text is already a durable outbox
  // row and Send stays offered, so a later press re-drives the resume.
  const resumeFailure = useThreadsStore((s) => s.resumeFailures.get(ref));
  const toastedResumeFailureSeqRef = useRef<number | null>(null);
  useEffect(() => {
    const seq = resumeFailure?.seq ?? 0;
    // Seed from the store on the first run: a failure already published when
    // this composer mounted has been reported, and a remount (dockview unmounts
    // inactive panes routinely) must not re-toast it. Only a failure published
    // after mount is new to this instance.
    if (toastedResumeFailureSeqRef.current === null) {
      toastedResumeFailureSeqRef.current = seq;
      return;
    }
    if (seq <= toastedResumeFailureSeqRef.current) return;
    toastedResumeFailureSeqRef.current = seq;
    if (resumeFailure) toasts.push("error", `Resume failed: ${resumeFailure.message}`);
  }, [resumeFailure, toasts]);

  useEffect(() => {
    if (activeRecoveryId === null || observedAttachments.hasPending || recoverySubmissionInFlight(activeRecoveryId))
      return;
    void queueRecoveryPersistence(
      activeRecoveryId,
      text,
      attachments.toInputAttachments(),
      skillNames,
      selectionMeta.commandNames,
      selectionMeta.mentions,
    );
  }, [
    activeRecoveryId,
    observedAttachments.hasPending,
    attachments.toInputAttachments,
    queueRecoveryPersistence,
    text,
    skillNames,
    selectionMeta,
  ]);

  useEffect(() => {
    if (
      freshRecoveryRef !== ref ||
      activeRecoveryId !== null ||
      textRef.current.trim() !== "" ||
      attachments.items.length > 0 ||
      skillNamesRef.current.length > 0 ||
      selectionMetaRef.current.commandNames.length > 0
    ) {
      return;
    }
    // An interrupt is not a draft. It carries no input, so activating one
    // loads an empty composer and then resends the user's next keystrokes as a
    // turn/start -- a Stop becoming a message. QueueStrip renders it as a
    // failure with its reason instead.
    // This tab's stored pick names the conversation's newest choice; prefer
    // it so a reopen restores the user's row, not just the first restorable
    // one.
    const pick = readRecoverySelection(ref)?.clientMutationId ?? null;
    const restorable = (entry: MutationRecoveryRecord) =>
      entry.recoveryKind === "rejected" && entry.method !== "turn/interrupt" && !entry.preserveComposer;
    const record =
      recoveryEntries.find((entry) => entry.clientMutationId === pick && restorable(entry)) ??
      recoveryEntries.find(restorable);
    if (!record) return;
    const recovered = recoveryComposerDraft(record);
    const visible = materializeSelectionReferences({ ...recovered, skillNames: restoredSkillNames(recovered) });
    Object.assign(recovered, visible);
    recoveryOwnsLocalDraftRef.current = false;
    // A mount-restore is a background activation: it must not overwrite a
    // fresh user pick with its fallback row.
    setActiveRecoveryId(record, { automatic: true });
    // Restoration replaces this mount's local owner without editing the
    // shared recovery draft that an earlier mount may still be submitting.
    draftEditRevisionRef.current += 1;
    markRestore();
    updateText(recovered.text);
    updateSkillNames(restoredSkillNames(recovered));
    updateSelectionMeta(recovered);
    attachments.replaceWithSettled(recovered.attachments);
    clearPersistedDraft(ref);
  }, [
    activeRecoveryId,
    attachments.replaceWithSettled,
    draftEditRevisionRef,
    freshRecoveryRef,
    markRestore,
    recoveryEntries,
    recoveryOwnsLocalDraftRef,
    ref,
    setActiveRecoveryId,
    skillNamesRef,
    selectionMetaRef,
    textRef,
    updateText,
    updateSkillNames,
    updateSelectionMeta,
    attachments.items.length,
  ]);
  // QueueStrip captures this mount's payload and edit revision through
  // getComposerText before it starts the drain's durable write.
  function handleDrainSuccess(): void {
    const snapshot = lastDrainSnapshotRef.current;
    if (!snapshot || !source.alive) return;
    clearIfUnchanged(
      snapshot.text,
      snapshot.revision,
      snapshot.draftRevision,
      snapshot.skillNames,
      snapshot.commandNames,
    );
    clearSubmittedAttachments(snapshot.attachments);
  }

  async function getRecoveryRevisionForDrain(): Promise<number | undefined> {
    await source.flushRecoveryWrites();
    if (!source.alive || source.getSnapshot().activeRecoveryId !== activeRecoveryId) return undefined;
    return recoveryOwnerLeaseRef.current?.compositionRevision;
  }
  // restoreTextToComposer implements the shared "put text back into the
  // composer without clobbering what's already typed there" merge (parity-
  // m5-composer.md line 101, byte-ported from renderer.js:6823-6837's own
  // restoreTextToComposer): existing text is right-trimmed then kept, the
  // incoming text is appended after a blank line, textEditor.write() keeps
  // the draft in sync (mirrors the legacy synthetic `input` event) and
  // parks the cursor at the very end, and the textarea is refocused - same
  // as legacy's own ta.focus() call.
  //
  // QueueStrip uses this behavior when a queued entry is moved back into
  // the composer for editing.
  //
  // `attachments` is accepted for signature symmetry but never restored: a
  // queued entry's edit keeps image attachments out of the composer per
  // parity (contracts-composer-queue-pending.md:122, parity-m5-
  // composer.md:102) - dropped image attachments surface via QueueStrip's
  // own durable queue state. `skillNames` restores the entry's selections
  // as chips (union with the current draft's chips, deduped by name): a
  // queued skill selection must survive the edit round-trip, not silently
  // drop.
  function restoreTextToComposer(
    restoredText: string,
    _attachments?: InputAttachment[],
    restoredNames?: readonly string[],
    restoredCommands?: readonly string[],
    restoredMentions?: readonly ComposerMention[],
  ): void {
    const current = patchSelectionText(
      { text: textRef.current, skillNames: skillNamesRef.current, ...selectionMetaRef.current },
      textRef.current.trim() === "" ? "" : textRef.current.replace(/\s+$/, ""),
    );
    // An entry can carry a selection with no prose of its own. Its chip has to
    // be visible in the sentence either way, so spell the reference out rather
    // than let the restore drop what the user chose.
    const restored = materializeSelectionReferences({
      text: restoredText,
      skillNames: [...(restoredNames ?? [])],
      commandNames: [...(restoredCommands ?? [])],
      ...(restoredMentions ? { mentions: [...restoredMentions] } : {}),
    });
    const merged = mergeRecoveryComposerDraft(
      current.text,
      [],
      { ...restored, attachments: [], mentions: skillAtomMentions(parseSkillDocument(restored)) },
      current.skillNames,
      current.commandNames,
      skillAtomMentions(parseSkillDocument(current)),
    );
    const value = serializeSkillDocument(parseSkillDocument(merged));
    const text = value.text;
    editSkillNames(value.skillNames);
    updateSelectionMeta(value);
    // A queued entry's selections are named, not spelled out, so the value that
    // carries them is authoritative here exactly as a recovery activation's is:
    // without this the merge is a partial append, the references land as plain
    // text, and the request would carry activations the user cannot see.
    markRestore();
    updateText(text);
    source.editor.write(text, text.length);
  }

  async function activateRecovery(record: MutationRecoveryRecord): Promise<void> {
    // QueueStrip already holds the shared operation lease for this callback.
    if (!source.alive) return;
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    try {
      if (!(await source.activateRecovery(record))) {
        if (source.alive) pushRecoveryRefusal("changed");
        return;
      }
      if (!source.alive) return;
    } catch (error) {
      if (source.alive) toasts.push("error", sessionActionError("Edit failed", error));
    }
  }

  // getComposerText is QueueStrip's own seam for reading this composer's
  // CURRENT text/attachments/hasPending at the moment its drain affordance
  // is used - textRef.current (not the `text` state closure) for the
  // identical liveness reason textEditor.read() above reads it, and
  // toInputAttachments() for the same wire shape submitAction's own payload
  // already uses. `hasPending` mirrors this component's own attachments.
  // hasPending so QueueStrip's "Steer queue now" button can block with the SAME
  // "still processing" toast this component's classic submit paths already
  // use, instead of silently sending a drain payload with a not-yet-encoded
  // image missing (toInputAttachments() itself only ever filters incomplete
  // items without signaling it - see that function's own doc comment) - see
  // w5-integration-wiring-report.md Concern #3. Also stashes a snapshot into
  // lastDrainSnapshotRef (text, edit revision, and the currently-staged attachments) so
  // handleDrainSuccess can later tell whether the composer changed between
  // THIS read and the drain actually resolving - QueueStrip only ever calls
  // this once per handleDrain invocation, immediately before starting the
  // request, so the snapshot always reflects exactly what that drain sent.
  function getComposerText() {
    lastDrainSnapshotRef.current = {
      text: textRef.current,
      attachments: attachments.items,
      skillNames: [...skillNamesRef.current],
      commandNames: [...selectionMetaRef.current.commandNames],
      mentions: selectionMetaRef.current.mentions,
      revision: draftEditRevisionRef.current,
      draftRevision: readDraftRevision(ref),
    };
    return {
      text: textRef.current,
      attachments: attachments.toInputAttachments(),
      hasPending: attachments.hasPending,
      skillNames: [...skillNamesRef.current],
      commandNames: [...selectionMetaRef.current.commandNames],
      mentions: selectionMetaRef.current.mentions,
    };
  }
  // submitAction wraps every method in submitWithPendingTracking (the wave's
  // own beyond-parity decision - w5-task-3-report.md: "T2's own send/steer
  // submissions should wrap their threadsStore.send/.steer calls in
  // submitWithPendingTracking the same way QueueStrip's own drain handler
  // wraps drainAsSteer... that's what makes optimistic pending genuinely
  // uniform across all four methods"), exactly mirroring QueueStrip.tsx's
  // own handleDrain shape.
  //
  // Toast/bookkeeping split, reconciled against pendingTurnsStore's own
  // documented contract (its submitWithPendingTracking doc comment: onFailure
  // is for toasting, "rethrowing so the caller's OWN catch can still run its
  // own non-toast bookkeeping"): the toast lives ENTIRELY inside onFailure
  // (same as QueueStrip's own handleDrain), and the outer catch below only
  // ever re-runs the isQueuedDrainPartial state-clearing (never a second
  // toast) - so a failure surfaces exactly once regardless of which branch
  // it takes. Before this reconciliation, both sides pushed a toast for the
  // SAME failure; this is the fix, not a pre-existing split.
  async function submit(
    kind: "send" | "queue" | "steer" | "drain",
    options: { text?: string; clearDraftText?: string } = {},
  ): Promise<void> {
    if (isBusy()) return;
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    const model = liveThreadModel(ref);
    if (!model) return;
    if (kind === "send" || kind === "queue") {
      const allowed = liveAvailability();
      if (!(kind === "send" ? allowed.canSend : allowed.canQueue)) {
        toasts.push("error", "Send is not available for this session");
        return;
      }
    } else {
      if (pressLocalRecoveryFenced(ref)) {
        toasts.push("error", STEER_RECOVERY_FENCED_REASON);
        return;
      }
      const reason = pressRefusal(ref, kind);
      if (reason !== undefined) {
        const verb = kind === "drain" ? "Drain" : "Steer";
        toasts.push("error", reason === NO_ACTIVE_TURN ? `${verb} failed: ${reason}` : reason);
        return;
      }
    }
    const releaseRecoveryOperation = source.tryAcquireRecoveryOperation();
    if (!releaseRecoveryOperation) return;
    if (source.getSnapshot().recoveryActivating) {
      releaseRecoveryOperation();
      return;
    }
    const submittedText = options.text ?? textRef.current;
    const clearDraftText = options.clearDraftText ?? submittedText;
    const submittedAttachments = attachments.items;
    const submittedSkillNames = [...skillNamesRef.current];
    const submittedCommandNames = [...selectionMetaRef.current.commandNames];
    const submittedMentions = selectionMetaRef.current.mentions;
    const submittedRevision = draftEditRevisionRef.current;
    const submittedDraftRevision = readDraftRevision(ref);
    const payload = attachments.toInputAttachments();
    const submittedLease = recoveryOwnerLeaseRef.current;
    const submittedRecoveryId = submittedLease?.clientMutationId ?? null;
    let releaseRecoveryInFlight: (() => void) | null = null;
    const releaseInFlightRecovery = () => {
      releaseRecoveryInFlight?.();
      releaseRecoveryInFlight = null;
    };
    const recoveryBarrier = submittedRecoveryId === null ? undefined : captureRecoveryStopBarrier(ref);
    let recoveryResendRefused = false;
    let recoveryAlreadySent = false;
    const recoveryWriteRefusal = new Error("Recovery persistence was refused");
    const recoveryAlreadySentRefusal = new Error("Recovery source was already consumed");
    // The UI-side half of the skillInput gate (threads.ts's composerMutationIntent
    // is the store-side half, and Task 12 gates the hub's forwarding too): a
    // target that never advertised the capability keeps the draft and hears
    // why, instead of minting durable intent the wire would refuse.
    const refusal = selectionRefusal(
      { skillNames: submittedSkillNames, commandNames: submittedCommandNames },
      model.capabilities,
    );
    if (refusal) {
      pushSelectionRefusal(refusal);
      releaseRecoveryOperation();
      return;
    }
    setBusyAction(kind === "send" || kind === "queue" ? "submit" : "steer");
    if (submittedRecoveryId !== null) releaseRecoveryInFlight = beginRecoverySubmission(submittedRecoveryId);
    let submissionFailed = false;
    try {
      await submitWithPendingTracking(
        {
          ref,
          text: submittedText,
          attachments: payload,
          skillNames: submittedSkillNames,
          commandNames: submittedCommandNames,
          recoveryId: submittedRecoveryId ?? undefined,
          onFailure: (err) => {
            if (err === recoveryWriteRefusal || err === recoveryAlreadySentRefusal) return;
            const label = kind === "send" ? "Send" : kind === "queue" ? "Queue" : kind === "steer" ? "Steer" : "Drain";
            toasts.push("error", sessionActionError(`${label} failed`, err));
          },
        },
        async () => {
          if (submittedRecoveryId !== null) {
            await source.runRecoveryOperation(async () => {
              const persisted = await queueRecoveryPersistence(
                submittedRecoveryId,
                submittedText,
                payload,
                submittedSkillNames,
                submittedCommandNames,
                submittedMentions,
                clearDraftText,
              );
              if (!persisted) {
                throw recoveryWriteRefusal;
              }
              const committedRevision = recoveryOwnerLeaseRef.current?.compositionRevision;
              if (committedRevision === undefined) {
                throw recoveryWriteRefusal;
              }
              const outcome = await resendRecoveryPendingTurn(
                submittedRecoveryId,
                ref,
                kind,
                submittedText,
                payload,
                submittedSkillNames,
                submittedCommandNames,
                submittedMentions,
                recoveryBarrier,
                { [submittedRecoveryId]: committedRevision },
                source.compositionOwnerId,
              );
              if (outcome.status !== "sent") {
                recoveryResendRefused = outcome.status === "refused";
                recoveryAlreadySent = outcome.status === "consumed";
                throw recoveryResendRefused ? recoveryWriteRefusal : recoveryAlreadySentRefusal;
              }
              // The resend transaction consumed the recovery row, so a remount
              // can no longer restore it. Keep the guard until here rather than
              // until the network settles.
              releaseInFlightRecovery();
            });
            return;
          }
          return submitDraftToStore(kind, ref, {
            text: submittedText,
            attachments: payload,
            skillNames: submittedSkillNames,
            commandNames: submittedCommandNames,
            mentions: submittedMentions,
          });
        },
      );
      if (!source.alive) return;
      const submittedEditorMatches =
        textRef.current === clearDraftText &&
        sameSkillSelections(skillNamesRef.current, submittedSkillNames) &&
        sameSkillSelections(selectionMetaRef.current.commandNames, submittedCommandNames);
      const cleared = clearIfUnchanged(
        clearDraftText,
        submittedRevision,
        submittedDraftRevision,
        submittedSkillNames,
        submittedCommandNames,
      );
      // Recovery settlement can persist the literal built-in invocation with
      // a new stored revision. It does not advance the source's edit revision,
      // so only a successful guarded clear may retire that owned stored draft.
      // Equal content alone cannot distinguish an edit away and back.
      if (
        cleared &&
        options.clearDraftText !== undefined &&
        options.text !== undefined &&
        options.text !== options.clearDraftText &&
        submittedEditorMatches
      ) {
        if (ownedDraftRevisionRef.current === readDraftRevision(ref)) {
          clearDraft(ref);
          ownedDraftRevisionRef.current = readDraftRevision(ref);
        }
      }
      clearSubmittedAttachments(submittedAttachments);
    } catch (error) {
      submissionFailed = true;
      // A refused recovery resend keeps its composer payload. Ordinary sends
      // retain the store's existing eligible direct-dispatch fallback policy.
      if (source.alive && recoveryResendRefused) pushRecoveryRefusal("changed");
      else if (source.alive && recoveryAlreadySent) pushRecoveryRefusal("consumed");
      else if (error !== recoveryWriteRefusal && error !== recoveryAlreadySentRefusal) return;
    } finally {
      setBusyAction(null);
      releaseInFlightRecovery();
      if (submissionFailed && source.alive && draftEditRevisionRef.current !== submittedRevision) {
        // The recovery autosave effect is suppressed while the guard is held.
        // Persist edits typed during the failed send through the recovery
        // chain so reloading does not merge them with the older record as two
        // separate copies.
        const activeRecoveryId = source.getSnapshot().activeRecoveryId;
        if (activeRecoveryId === null) {
          source.persistDraft(textRef.current);
        } else {
          void source.queueRecoveryPersistence(
            activeRecoveryId,
            textRef.current,
            attachments.toInputAttachments(),
            skillNamesRef.current,
            selectionMetaRef.current.commandNames,
            selectionMetaRef.current.mentions,
          );
        }
      }
      releaseRecoveryOperation();
    }
  }
  async function stop(): Promise<void> {
    if (isBusy()) return;
    // Same press-time rule as Steer: the turn this button was rendered for can
    // have ended by the time the press lands.
    const reason = pressRefusal(ref, "stop");
    if (reason !== undefined) {
      toasts.push("error", reason === NO_ACTIVE_TURN ? `Interrupt failed: ${reason}` : reason);
      return;
    }
    setBusyAction("interrupt");
    try {
      await threadsStore.getState().interrupt(ref);
    } catch (err) {
      toasts.push("error", sessionActionError("Interrupt failed", err));
    } finally {
      setBusyAction(null);
    }
  }

  function liveAvailability() {
    const target = liveThreadModel(ref);
    if (!target) return { canSend: false, canQueue: false };
    const state = threadsStore.getState();
    return composerAvailability(
      target,
      ownPendingSend(pendingTurnEntries(ref, "send")),
      state.restartBlockingObligations.has(ref),
      {
        uncertainMessages: hasBlockedUnknown(ref),
        stopInFlight: state.stoppingRefs.has(ref),
        queuedNonSend: hasQueuedNonSend(ref),
        mutationStateAuthoritative: state.mutationAuthorityRefs.has(ref),
      },
    );
  }

  async function submitFromLiveState(): Promise<void> {
    if (isBusy()) return;
    const current = source.getSnapshot();
    if (
      !current.text.trim() &&
      attachments.items.length === 0 &&
      current.skillNames.length === 0 &&
      current.commandNames.length === 0
    )
      return;
    const allowed = liveAvailability();
    if (!allowed.canSend && !allowed.canQueue) {
      toasts.push("error", "Send is not available for this session");
      return;
    }
    await submit(allowed.canQueue ? "queue" : "send");
  }

  const queueProps: QueueStripProps = {
    ref,
    getComposerText,
    onRestoreToComposer: restoreTextToComposer,
    activeRecoveryId: activeRecoveryId ?? undefined,
    activeRecoveryRevision: recoveryOwnerLeaseRef.current?.compositionRevision,
    getRecoveryRevision: getRecoveryRevisionForDrain,
    onEditRecovery: activateRecovery,
    tryAcquireRecoveryOperation: () => (isBusy() ? null : source.tryAcquireRecoveryOperation()),
    onDrainSuccess: handleDrainSuccess,
    busy,
    onDrainBusyChange: (draining) => setBusyAction(draining ? "drain" : null),
  };
  return {
    availability,
    busy,
    showStop: !!model && controlsFor(model).stop,
    submitFromLiveState,
    submit,
    stop,
    queueProps,
  };
}

export type ComposerActions = ReturnType<typeof useComposerActions>;
