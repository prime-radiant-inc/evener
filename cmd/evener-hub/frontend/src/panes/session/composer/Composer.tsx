// Composer: the session pane's input surface, mounted by Session.tsx below
// the transcript (T1 carves this slot; Session.tsx is FROZEN for the wave
// once T1 lands — every stream below edits only inside this subtree).
//
// The card itself is widgets/promptcard, shared verbatim with the spawn form:
// "message this agent" and "start an agent" are the same object, so they are
// one component with two sets of callers' buttons in it, not two lookalikes.
//
// Stop takes Send's slot when the session can stop and the draft is empty,
// at every width. A sendable draft restores Send, with Stop still available
// in the session menu. Steer stays inline on wide composers and in the menu
// on narrow ones (narrowComposer.ts owns that gate).
//
// T2 (this file): the skill editor, send-vs-steer-vs-queue-vs-drain routing via
// the shared sendQueueGate (over protocol/sendQueueAvailability's table) +
// submitRouting.ts's own steer/drain fork, Enter-to-send preference,
// per-ref drafts, attachments (paste/drag/picker), interrupt affordance.
// T3/T4 render their own subtrees inside the two marked slots below without
// ever touching the surrounding structure - see each slot's own comment.

import {
  type BuiltinMatch,
  type ComposerMention,
  decideSteerRoute,
  matchBuiltinInvocation,
  mergeDraftText,
  mergeSlashCommands,
  NO_ACTIVE_TURN,
  parseSlashToken,
  SHUT_DOWN_STATUSES,
  sessionActionError,
  slashCommandInvocation,
  type ThreadModel,
} from "@evener/appwire-client";
import { ownPendingSend } from "@evener/appwire-client/state/mutation";
import {
  type FormEvent,
  memo,
  type KeyboardEvent as ReactKeyboardEvent,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { activitySidebarStore } from "../../../shell/activitybar/activitySidebarStore";
import type { PaletteRunContext, ScopedCommand } from "../../../shell/palette/commands";
import { sessionBuiltinCommands } from "../../../shell/palette/commands";
import type { SessionMenuTurnVerbs } from "../../../shell/sessionMenu/SessionMenu";
import { useIsMobile } from "../../../shell/useIsMobile";
import { useMountAutofocus } from "../../../shell/useMountAutofocus";
import {
  controlsFor,
  isRecoveryFenced,
  liveThreadModel,
  pressLocalRecoveryFenced,
  pressRefusal,
  recoveryFence,
  sendQueueDecisionFor,
} from "../../../stores/liveControls";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import { prefsStore, usePrefsStore } from "../../../stores/prefs";
import {
  captureRecoveryStopBarrier,
  hasBlockedUnknown,
  hasQueuedNonSend,
  type InputAttachment,
  type ResumeOnlySignals,
  readRecoveryWithStopEpoch,
  threadsStore,
  useThreadsStore,
} from "../../../stores/threads";
import {
  Button,
  ConfirmDialog,
  Dropzone,
  IconButton,
  PromptCard,
  SendIcon,
  Tooltip,
  useToasts,
  VisuallyHidden,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { SessionChrome, type SessionChromeProps } from "../chrome/SessionChrome";
import { TasksPanel, type TasksPanelHandle } from "../chrome/TasksPanel";
import { AttachmentTile } from "./AttachmentTile";
import { useAskDockPending } from "./askDockPending";
import { AttachIcon } from "./attachments/AttachIcon";
import { imageFilesFromClipboard } from "./attachments/clipboard";
import { type TextEditor, useAttachments } from "./attachments/useAttachments";
import { runBuiltinCommand } from "./builtinCommand";
import { CurrentWork } from "./CurrentWork";
import styles from "./composer.module.css";
import { consumeComposerFocus, requestComposerFocus, useComposerFocusRequest } from "./composerFocus";
import { STEER_RECOVERY_FENCED_REASON, steerTooltipLabel, submitTooltipLabel } from "./controlTooltips";
import { clearDraft, clearPersistedDraft, readDraftRevision, readRecoverySelection } from "./draft";
import { useNarrowComposer } from "./narrowComposer";
import { pendingTurnEntries, QueueStrip, submitWithPendingTracking, usePendingTurnEntries } from "./queue";
import {
  resendRecoveryPendingTurn,
  useBlockedMutationEntries,
  useComposerSubmitting,
  useRecoveryEntries,
} from "./queue/pendingTurnsStore";
import { consumeQuoteInsert, useQuoteInsertRequest } from "./quoteInsert";
import { mergeRecoveryComposerDraft, recoveryComposerDraft } from "./recovery/recoveryDraft";
import { SkillEditor, type SkillEditorHandle } from "./SkillEditor";
import { SlashCompletionMenu } from "./SlashCompletionMenu";
import {
  maskSkillAtoms,
  materializeSelectionReferences,
  parseSkillDocument,
  patchSelectionText,
  type SkillEditorValue,
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
import { recordStoplessComposer } from "./stoplessComposer";
import { pushRecoveryRefusal, pushSelectionRefusal, selectionRefusal, submitDraftToStore } from "./submitProtocol";
import { useInlineStopReady } from "./useInlineStopReady";
import { useSlashCompletion } from "./useSlashCompletion";

export interface ComposerProps {
  ref: string;
  paneId?: string;
  // Whether this composer's pane is the workspace's focused one. Mount
  // autofocus keys on it: a session loading into a background tab must never
  // yank keyboard focus away from what the reader is doing.
  focused: boolean;
  // The seat may suppress mount autofocus for a navigation or restore intent.
  mountAutofocus?: boolean;
  source: ComposerSourceState;
  // An in-progress IME composition, reported for the floating attention
  // control's visibility. Focus alone never counts: a session open autofocuses
  // this editor, and hiding the control then would take it away before the
  // reader could use it. Draft content alone is never editing either.
  onEditingChange?: (editing: boolean) => void;
}

const CLASS = {
  composer: requireClass(styles.composer, "composer.module.css", "composer"),
  storageStatus: requireClass(styles.storageStatus, "composer.module.css", "storageStatus"),
  attachments: requireClass(styles.attachments, "composer.module.css", "attachments"),
  leading: requireClass(styles.leading, "composer.module.css", "leading"),
  formAnchor: requireClass(styles.formAnchor, "composer.module.css", "formAnchor"),
  submitLabel: requireClass(styles.submitLabel, "composer.module.css", "submitLabel"),
};

// The composer re-renders on every draft keystroke and nothing in the chrome
// reads the draft, so its mounts go through this memo. It renders the imported
// SessionChrome binding at render time instead of wrapping the function itself
// (memo(SessionChrome)), so a replacement of the module export still reaches
// these mounts.
const MemoizedSessionChrome = memo(function MemoizedSessionChrome(props: SessionChromeProps) {
  return <SessionChrome {...props} />;
});

// "drain" is set/cleared only by QueueStrip's own onDrainBusyChange (its
// "Steer queue now" button) - never by this component's own submitAction, which
// uses "steer" for its classic drain-as-steer route too (see submitAction's
// own setBusyAction call). Both surfaces still share this ONE piece of
// state: whichever one goes busy first disables the other's controls too,
// closing the race where both could otherwise fire drainAsSteer at once
// (w5-integration-wiring-report.md's "two Steer buttons" concern).
type BusyAction = "submit" | "steer" | "interrupt" | "drain" | null;

// The local recovery fence lives in stores/liveControls.ts (one predicate for
// every surface that owes it - this module's availability/card/Steer gates and
// QueueStrip's press gates): QueueStrip cannot import from this module
// (Composer imports QueueStrip), and a per-file copy of a fence this
// load-bearing would drift. A LOCAL session carrying a restart-blocking
// obligation (a Stop, or a snapshot the daemon reports as
// restartRequired/resumeRequired) cannot be acted on at all until the explicit
// Resume action clears the fence.

// The missing model's turnVerbs offering: one frozen object, because a fresh
// {} on every recompute would defeat MemoizedSessionChrome's
// shallow-equality bailout for a value that carries no information.
const NO_TURN_VERBS: SessionMenuTurnVerbs = Object.freeze({});

export function Composer({ ref, paneId, focused, mountAutofocus = true, source, onEditingChange }: ComposerProps) {
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
  const isMobile = useIsMobile();
  const editorRef = useRef<SkillEditorHandle>(null);
  // An in-progress IME composition drives the floating attention control's
  // editing suppression. Editor focus never counts: a session open autofocuses
  // this editor, and hiding the control then would take it away before the
  // reader could use it. Draft content alone is never "editing" either, and
  // broad footer focus (attachment buttons, the session menu) does not count.
  const composingRef = useRef(false);
  const editingReportedRef = useRef(false);
  const onEditingChangeRef = useRef(onEditingChange);
  onEditingChangeRef.current = onEditingChange;
  const reportEditing = useCallback(() => {
    const editing = composingRef.current;
    if (editingReportedRef.current === editing) return;
    editingReportedRef.current = editing;
    onEditingChangeRef.current?.(editing);
  }, []);
  // A composer that unmounts mid-composition (a loading face replaces the live
  // subtree) must not leave the seat believing a composition is still running.
  useEffect(
    () => () => {
      if (!editingReportedRef.current) return;
      editingReportedRef.current = false;
      onEditingChangeRef.current?.(false);
    },
    [],
  );
  const fileInputRef = useRef<HTMLInputElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  const submitButtonRef = useRef<HTMLButtonElement>(null);
  const tasksPanelRef = useRef<TasksPanelHandle>(null);
  // Set by scheduleCursorRestore below; consumed (and cleared) by the
  // cursor-restore layout effect in the commit that carries the edit.
  const cursorToRestoreRef = useRef<number | null>(null);
  const [cursorRestoreSeq, setCursorRestoreSeq] = useState(0);
  // Parks the caret a programmatic edit wants and forces a commit, so the
  // layout effect below applies it even when the edit left `text` unchanged.
  const scheduleCursorRestore = useCallback((cursor: number): void => {
    cursorToRestoreRef.current = cursor;
    setCursorRestoreSeq((seq) => seq + 1);
  }, []);
  const {
    text,
    skillNames,
    commandNames,
    mentions,
    restoreEpoch,
    activeRecoveryId,
    recoveryActivating,
    recoveryOperationBusy,
    freshRecoveryRef,
  } = useSyncExternalStore(source.subscribe, source.getSnapshot, source.getSnapshot);
  const selectionMeta = useMemo(() => ({ commandNames, mentions }), [commandNames, mentions]);
  const {
    lastDrainSnapshotRef,
    draftEditRevisionRef,
    ownedDraftRevisionRef,
    textRef,
    skillNamesRef,
    selectionMetaRef,
    recoveryOwnerLeaseRef,
    recoveryWriteVersionRef,
    recoveryReplacementEpochRef,
    recoveryOwnsLocalDraftRef,
  } = source.continuity;
  const {
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
    clearIfUnchanged,
    clearSubmittedAttachments,
  } = source;
  const [busyAction, setBusyAction] = useState<BusyAction>(null);
  const actionPending =
    busyAction !== null || submitting || mutationWriteStalled || recoveryActivating || recoveryOperationBusy;
  const mountedRef = useRef(false);
  const [pendingGoalReplacement, setPendingGoalReplacement] = useState<string | null>(null);
  // Track live focus too, so stopping does not collapse a focused editor.
  // React focus events follow the composer's controls and portaled menus.
  const [followUpFocused, setFollowUpFocused] = useState(false);
  const followUpFocusSequenceRef = useRef(0);

  // Steer moves into the menu below the phone-width boundary.
  // narrowComposer.ts measures the pane, not the viewport.
  const [narrow, composerRootRef] = useNarrowComposer();

  // The session menu's turn-verb items press the same handlers the row
  // buttons press. Those handlers are recreated every render (they close
  // over per-render press-time state), and this component's SessionChrome
  // mount is memoized against exactly that (#2490), so the menu's onSelect
  // closures are identity-stable and read whichever handler is current at
  // the press through these refs. The refs are assigned during render -
  // widgets/tree's own latest-ref pattern (its handlersRef) - so a press
  // can never read the previous render's handlers. The assignments sit
  // ahead of the null-model guard because hooks cannot go behind a
  // conditional return, and the function declarations they read are
  // hoisted to this function's top.
  const interruptClickRef = useRef<() => Promise<void>>(async () => {});
  const steerClickRef = useRef<() => void>(() => {});
  interruptClickRef.current = handleInterruptClick;
  steerClickRef.current = handleSteerClick;

  // The menu's turn-verb offerings. Identity-stable across draft
  // keystrokes - the deps are the model, the gate, and our own in-flight
  // request, none of which a keystroke moves - so the memoized chrome sees a
  // new prop only when the offering actually changed. Presence mirrors the
  // row buttons' own gate (controlsFor), recomputed here because the row's
  // own `controls` binding sits behind the null-model guard. Steer's
  // recovery fence is deliberately NOT a disabled state here: the press
  // re-reads the fence live and toasts the reason (kata 2f41), which a
  // disabled menu item cannot explain.
  const turnVerbs = useMemo<SessionMenuTurnVerbs>(() => {
    if (!model) return NO_TURN_VERBS;
    const available = controlsFor(model);
    const items: SessionMenuTurnVerbs = {};
    if (available.stop) {
      items.stop = {
        onSelect: () => {
          void interruptClickRef.current();
        },
        disabled: actionPending,
      };
    }
    if (available.steer && narrow) {
      items.steer = {
        onSelect: () => steerClickRef.current(),
        disabled: actionPending,
      };
    }
    return items;
  }, [narrow, model, actionPending]);

  // The composer's own single command line (2026-08-14: "the composer is
  // where you act on this session"): the session-scoped BUILT-IN registry
  // (shell/palette/commands.ts's sessionBuiltinCommands, unavailableReason-
  // resolved against THIS ref) merged with the owning session's loaded inventory
  // (slashCompletion.ts's mergeSlashCommands) - one list, one menu, whether a
  // row's provenance is a built-in, plugin, project or user command. The model
  // is keyed by ref, so loading a different target never borrows old/global rows.
  const visibleSlashCatalog = model?.diagnostics?.commands ?? [];
  const sessionBuiltins = sessionBuiltinCommands({ sessionRef: ref, onPage: "session", sessionPaneId: paneId ?? null });
  const slashMenuCatalog = mergeSlashCommands(sessionBuiltins, visibleSlashCatalog, model?.skills ?? []);

  // Attachment marker edits update controlled text, selected references and
  // the persisted draft together. Prefer a pending cursor restoration over
  // the live editor selection so two attachment gestures before React commits
  // insert consecutive markers rather than reusing the first position.
  const textEditor: TextEditor = useMemo(
    () => ({
      read: () => {
        const cursor = cursorToRestoreRef.current ?? editorRef.current?.getCursor() ?? textRef.current.length;
        const selection =
          cursorToRestoreRef.current === null && editorRef.current
            ? editorRef.current.getSelection()
            : { start: cursor, end: cursor };
        return { text: textRef.current, cursor, selection };
      },
      write: (nextText, cursor, source) => {
        // Background cleanup changes this editor without claiming a shared
        // draft that another composer has edited in the meantime.
        const mayPersist =
          source === undefined || source === "edit" || ownedDraftRevisionRef.current === readDraftRevision(ref);
        writeDraft(nextText, mayPersist, source);
        scheduleCursorRestore(cursor);
      },
    }),
    [ref, textRef, ownedDraftRevisionRef, writeDraft, scheduleCursorRestore],
  );
  // Scoped by `ref`: dockview can have several session panes - and so several
  // mounted Composers - open at once, and a bare literal id would collide.
  const slashListboxId = `composer-slash-listbox-${ref}`;
  const {
    setToken: setSlashToken,
    items: slashItems,
    open: slashOpen,
    activeIndex: slashActiveIndex,
    activeId: slashActiveId,
    commit: commitSlashCompletion,
    handleKeyDown: handleSlashKeyDown,
    reset: resetSlashCompletion,
  } = useSlashCompletion({
    catalog: slashMenuCatalog,
    listboxId: slashListboxId,
    editorRef,
    textRef,
    textEditor,
  });
  useLayoutEffect(() => {
    mountedRef.current = true;
    const unbind = source.bindEditor(textEditor);
    return () => {
      mountedRef.current = false;
      unbind();
    };
  }, [source, textEditor]);
  const attachments = useAttachments(source.editor, source.attachments);
  const attachmentItemsRef = useRef(attachments.items);
  attachmentItemsRef.current = attachments.items;
  const recoveryEntries = useRecoveryEntries(ref);

  const replaceComposerWithGoalDraft = (objective: string): void => {
    // Invalidate queued recovery work before clearing ownership. Any operation
    // already beyond its initial owner check is prevented by this version from
    // clearing the ordinary draft written below when it eventually settles.
    recoveryWriteVersionRef.current += 1;
    recoveryReplacementEpochRef.current += 1;
    recoveryOwnsLocalDraftRef.current = false;
    setActiveRecoveryId(null);
    attachments.reset();
    updateSkillNames([]);
    updateSelectionMeta({ text: "", skillNames: [] });
    resetSlashCompletion();
    lastDrainSnapshotRef.current = null;
    const command = `/goal ${objective}`;
    textEditor.write(command, command.length);
    setPendingGoalReplacement(null);
    // The request carries this seat's pane: with an ordinary pane and an
    // embedded column sharing the ref, only the composer the goal row was
    // clicked in takes focus, so typing lands in its editor alone.
    requestComposerFocus(ref, paneId ?? null);
  };

  const editGoal = (objective: string): void => {
    if (
      textRef.current !== "" ||
      attachmentItemsRef.current.length > 0 ||
      recoveryOwnerLeaseRef.current !== null ||
      skillNamesRef.current.length > 0 ||
      selectionMetaRef.current.commandNames.length > 0
    ) {
      setPendingGoalReplacement(objective);
      return;
    }
    replaceComposerWithGoalDraft(objective);
  };

  const showTasks = (): void => {
    // Desktop Tasks everywhere is the activity sidebar, preselected to its
    // Tasks tab; on mobile there is no sidebar, so the per-session Sheet
    // keeps its behavior.
    if (isMobile) tasksPanelRef.current?.open();
    else {
      // The owning pane rides along, like every other embedded-column
      // request: the sidebar opens THIS conversation's Tasks and holds its
      // scope, keeping the cascade focused even when an ordinary pane for
      // the same conversation exists.
      activitySidebarStore.getState().openFor(ref, "tasks", undefined, paneId);
    }
  };

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
    if (activeRecoveryId === null || attachments.hasPending || recoverySubmissionInFlight(activeRecoveryId)) return;
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
    attachments.hasPending,
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
      attachmentItemsRef.current.length > 0 ||
      skillNamesRef.current.length > 0 ||
      selectionMetaRef.current.commandNames.length > 0
    ) {
      return;
    }
    // An interrupt is not a draft. It carries no input, so activating one
    // loads an empty composer and then resends the user's next keystrokes as a
    // turn/start -- a Stop becoming a message. QueueStrip renders it as a
    // failure with its reason instead.
    // Prefer the row this pane selected before reload: an older rejected peer
    // row can sit beside the pane's newer preserved row, and intent order alone
    // would restore the peer's composition.
    const selectedRecoveryId = readRecoverySelection(ref, source.recoverySelectionOwnerId);
    const restorable = recoveryEntries.filter(
      (entry) => entry.recoveryKind === "rejected" && entry.method !== "turn/interrupt" && !entry.preserveComposer,
    );
    const record = restorable.find((entry) => entry.clientMutationId === selectedRecoveryId) ?? restorable[0];
    if (!record) return;
    const recovered = recoveryComposerDraft(record);
    const visible = materializeSelectionReferences({ ...recovered, skillNames: restoredSkillNames(recovered) });
    Object.assign(recovered, visible);
    recoveryOwnsLocalDraftRef.current = false;
    setActiveRecoveryId(record);
    // Restoration replaces this mount's local owner without editing the
    // shared recovery draft that an earlier mount may still be submitting.
    draftEditRevisionRef.current += 1;
    markRestore();
    updateText(recovered.text);
    updateSkillNames(restoredSkillNames(recovered));
    updateSelectionMeta(recovered);
    attachments.replaceWithSettled(recovered.attachments);
    clearPersistedDraft(ref);
    scheduleCursorRestore(recovered.text.length);
  }, [
    activeRecoveryId,
    attachments.replaceWithSettled,
    draftEditRevisionRef,
    freshRecoveryRef,
    markRestore,
    recoveryEntries,
    recoveryOwnsLocalDraftRef,
    ref,
    scheduleCursorRestore,
    setActiveRecoveryId,
    skillNamesRef,
    source.recoverySelectionOwnerId,
    selectionMetaRef,
    textRef,
    updateText,
    updateSkillNames,
    updateSelectionMeta,
  ]);

  // askPending gates hiding/inerting the input row below (AskDock's own
  // seam - see AskDock.tsx's header comment: "that is the composer's own
  // surface to show/hide, and T2 owns it") - read unconditionally alongside
  // the rest of this component's hooks, ahead of the `!model` early return
  // below, per the rules of hooks.
  const askPending = useAskDockPending(ref);

  // enterToSend drives the Steer/Send kbd-hint labels below - read via the
  // reactive usePrefsStore hook (Settings -> Display's own live toggle,
  // display.tsx's setEnterToSend) rather than a plain read, so this
  // component's hints update immediately if a user has Settings open in
  // another pane while this composer is mounted. Read unconditionally
  // alongside the rest of this component's hooks, ahead of the `!model`
  // early return below, per the rules of hooks - same rationale as
  // askPending's own doc comment above.
  const enterToSend = usePrefsStore((s) => s.enterToSend);

  // readyAnnouncement drives this component's own aria-live region below,
  // announcing "Message composer ready." the moment askPending flips
  // true -> false (parity-m5-composer.md line 118's OTHER half: AskDock's
  // own anchor already announces "Answer the agent's questions." on entry,
  // but that element unmounts entirely once its batches empty - see
  // AskDock.tsx's own header comment, "does NOT own... the mode-switch
  // status announcement... that is the composer's own surface" - so only
  // this component can announce the exit half of that same legacy
  // transition). Edge-triggered on the actual transition, not derived
  // straight from `!askPending`: a plain `!askPending ? "ready" : ""`
  // would also announce "ready" on this component's very first mount
  // (askPending starts false with no prior ask to exit from), which is not
  // an honest liveness signal - there's nothing that just became ready.
  const wasAskPendingRef = useRef(askPending);
  const [readyAnnouncement, setReadyAnnouncement] = useState("");
  useEffect(() => {
    const wasPending = wasAskPendingRef.current;
    wasAskPendingRef.current = askPending;
    if (wasPending && !askPending) setReadyAnnouncement("Message composer ready.");
  }, [askPending]);

  // A pending question removes the editor from the surface without a
  // composition end; clear the composition report so the control returns
  // automatically when the batch resolves.
  useEffect(() => {
    if (!askPending) return;
    composingRef.current = false;
    reportEditing();
  }, [askPending, reportEditing]);

  // Runs in the commit that carries a scheduled edit, after any text it
  // changed has reached the DOM through React's own controlled-value
  // reconciliation - only then is it safe to move the native cursor without
  // React clobbering it. Keyed on the schedule, not on `text`: ordinary typing
  // schedules nothing and never runs this, and an edit that left the text as
  // it was (an empty recovered draft activated into an empty composer) still
  // lands its caret in this commit instead of parking it for the next edit.
  // biome-ignore lint/correctness/useExhaustiveDependencies: cursorRestoreSeq is a deliberate trigger-only dep - the effect body reads the ref the schedule filled
  useLayoutEffect(() => {
    const cursor = cursorToRestoreRef.current;
    if (cursor === null) return;
    cursorToRestoreRef.current = null;
    editorRef.current?.setSelection(cursor);
  }, [cursorRestoreSeq]);

  // SelectionQuote's "Quote in reply" seam (quoteInsert.ts): a sibling
  // component under the transcript mounts and writes here via
  // requestQuoteInsert; this effect is the ONLY reader for this ref, and
  // consumeQuoteInsert() below is what makes the request one-shot - without
  // it, a later re-render (or this component remounting under the SAME
  // still-pending request, e.g. a fast tab switch) would replay it. Keyed on
  // the request's own monotonic id, not its text, so quoting the identical
  // line twice in a row is still two separate insertions rather than a
  // no-op the second time (quoteInsert.ts's own QuoteInsertRequest doc
  // comment). mergeDraftText is the SAME merge restoreTextToComposer uses
  // for QueueStrip's "edit a queued entry" path, now parameterized on the
  // request's own placement (mergeDraftText's own doc comment) - a quote
  // never clobbers whatever the user already typed, and a palette-inserted
  // slash command lands where it can actually parse (the draft's start)
  // instead of stranded after it. The cursor lands at the end of the merged
  // text for an append (unchanged), but right after the inserted text alone
  // for a prefix - the user's own existing draft sits AFTER the cursor in
  // that case, so jumping to the very end would land past it instead of
  // where they'd actually want to keep typing (e.g. a command's arguments).
  const quoteInsertRequest = useQuoteInsertRequest(ref, paneId);
  const consumedQuoteInsertIdRef = useRef<number | null>(null);
  useEffect(() => {
    if (!quoteInsertRequest || quoteInsertRequest.id === consumedQuoteInsertIdRef.current) return;
    consumedQuoteInsertIdRef.current = quoteInsertRequest.id;
    const merged = mergeDraftText(textRef.current, quoteInsertRequest.text, quoteInsertRequest.placement);
    const cursor = quoteInsertRequest.placement === "prefix" ? quoteInsertRequest.text.length : merged.length;
    textEditor.write(merged, cursor);
    editorRef.current?.focus();
    consumeQuoteInsert(ref);
  }, [quoteInsertRequest, ref, textEditor.write, textRef]);

  // composerFocus.ts's own seam: a global chord (owned elsewhere) asks this
  // ref's Composer to move keyboard focus into its textarea. Exactly the
  // same shape as the quote-insert effect above - keyed on the request's own
  // monotonic id so consuming a request from a previous mount under the SAME
  // still-pending request never replays it, and consumeComposerFocus() below
  // makes it one-shot.
  const composerFocusRequest = useComposerFocusRequest(ref, paneId);
  const consumedComposerFocusIdRef = useRef<number | null>(null);
  useEffect(() => {
    if (!composerFocusRequest || composerFocusRequest.id === consumedComposerFocusIdRef.current) return;
    const textarea = editorRef.current;
    if (!textarea) return;
    textarea.focus();
    consumedComposerFocusIdRef.current = composerFocusRequest.id;
    consumeComposerFocus(ref);
  });

  // Loading a session lands keyboard focus in its composer: writing the next
  // message is what the pane is for. Mount-only, gated by the seat's focus and
  // its general autofocus policy (see useMountAutofocus for the full rationale).
  useMountAutofocus(editorRef, focused && mountAutofocus);

  const hasText = text.trim() !== "";
  const hasAttachments = attachments.items.length > 0;
  const hasContent = hasText || hasAttachments || skillNames.length > 0 || selectionMeta.commandNames.length > 0;
  const inlineStop = !!model && controlsFor(model).stop && !hasContent;
  const inlineStopReady = useInlineStopReady(ref, !model || askPending ? null : inlineStop ? "stop" : "send");

  if (!model) return null; // Session.tsx only mounts this once its own model is hydrated; defensive only.

  // Captured as plain consts (not read as `model.xyz` again below): a
  // closure that references `model` directly cannot inherit the `if
  // (!model) return null` narrowing above through a nested function
  // declaration (a TypeScript limitation, not a real possible-undefined
  // case - Session.tsx never mounts this component before its own model is
  // hydrated), so every handler below reads these already-narrowed values
  // instead of `model.<field>` directly.
  const renderedModel: ThreadModel = model;
  const activeTurnId = model.activeTurnId;
  const ended = SHUT_DOWN_STATUSES.has(model.status.type);
  // The recovery fence as ONE reading (stores/liveControls.ts's recoveryFence),
  // so its three faces cannot drift:
  //   resumeOnly    - a merely-resumable local session: the hub folds the resume
  //                   into a send (turn/start is admitted while only
  //                   ResumeRequired stands), so its card keeps the Send surface
  //                   and it needs no separate Resume action.
  //   stillFenced   - the fence still blocks Send/Queue.
  //   fencedLocal   - a stopped local session that is STILL fenced (a Stop in
  //                   flight, active drain, or an incompatible restartRequired
  //                   daemon) keeps its follow-up card so the retained draft
  //                   stays reachable, but Send and Queue are not offered.
  const resumeOnlySignals: ResumeOnlySignals = {
    uncertainMessages: blockedMutations.length > 0,
    stopInFlight: stopping,
    queuedNonSend: hasQueuedNonSend(ref),
    mutationStateAuthoritative: mutationAuthoritative,
  };
  const fence = recoveryFence(model, recoveryRequired, resumeOnlySignals);
  const queueDepth = model.queue?.depth ?? 0;
  // What this session may be asked to do now: one derivation for every control
  // surface (stores/liveControls.ts), with the rationale (status alone, never
  // activeTurnId; capability is the harness's) in @evener/appwire-client's
  // submitRouting module. The press handlers below re-derive it from the store
  // at the press (pressRefusal), not from this render.
  const controls = controlsFor(model);
  const canSendWhenEnded = controls.send;
  // Tier 6: see ownPendingSend in @evener/appwire-client/state/mutation.
  const hasPendingSend = ownPendingSend(pendingSendEntries);
  // The Send/Queue availability of a model and this client's pending send,
  // projected to the composer's two booleans from the shared browser decision
  // (stores/liveControls.ts's sendQueueDecisionFor: the recovery adapter around
  // @evener/appwire-client's sendQueueGate under BROWSER_SEND_QUEUE_POLICY).
  // The gate owns the composition this body used to spell out: the recovery
  // fence's faces (Send-resumes, still fenced, folded resume), the send/queue
  // table, and the shut-down send substitution.
  //
  // Read at render for the button and its tooltip, and again at submit from the
  // stores' live model and live pending entries, so a status frame or a pending
  // send that landed (or cleared) between the two routes the submit rather than
  // the render.
  //
  // The restart-blocking obligation arrives as a parameter: the render passes
  // the subscribed value (so the availability updates with the store instead of
  // reading it behind the subscription's back), the submit passes a live store
  // read like the rest of its re-derivation. It is the raw obligation, not the
  // render's fence.fencedLocal: the fence here covers every status, active
  // included, not only the stopped one.
  const availability = sendQueueDecisionFor(model, hasPendingSend, recoveryRequired, resumeOnlySignals);
  const showStop = controls.stop;
  const showSteer = controls.steer;
  // The Steer control's own reading of the recovery fence (see
  // the send/queue gate's fence above): the hub refuses turn/steer for the
  // obligation's whole window, so the control must not offer a press that
  // could only park durable intent. Read from the subscribed obligation here
  // for the render; the press re-reads it live.
  const steerRecoveryFenced = isRecoveryFenced(recoveryRequired);
  // The one state kata 5gdv is about, described by the only code that can see
  // it happen. Diagnostic only -- see stoplessComposer.ts for why a breadcrumb
  // rather than another attempt to provoke it.
  if (model.status.type === "active" && !showStop) {
    recordStoplessComposer({
      ref,
      status: model.status.type,
      activeTurnId,
      capabilities: model.capabilities,
      capabilitySource: model.capabilitySource ?? "none",
      showSteer,
      ended,
    });
  }
  // The strip's queue depth is what shows the effect of a queued Send.
  const submitTooltip = submitTooltipLabel({ canQueue: availability.action === "queue", enterToSend });
  const canCompose = availability.action === "send" || availability.action === "queue";
  // Whether the follow-up card renders at all is the capability's call, for the
  // same reason the substitution above is: gating it on the table renders no
  // card for exactly the sessions the hub says are resumable. When the wire
  // really advertises no send, no card is rendered at all - an unusable field
  // is worse than no field.
  // The Send-resumes face is a stopped (ended) snapshot too, and its Send face
  // must keep the card reachable for its retained draft - the canonical
  // wire shape is notLoaded + send:false, which the capability alone would not
  // render a card for.
  const showFollowUpCard = ended && (canSendWhenEnded || fence.fencedLocal || fence.sendDriven);
  // Only the editor shrinks at rest. Controls stay available without typing.
  // Drafts, attachments and recovery keep enough writing space after blur.
  const followUpEngaged = fence.fencedLocal || fence.sendDriven || followUpFocused || hasContent;

  function handleTextChange(value: SkillEditorValue, caret: number): void {
    editSkillNames(value.skillNames);
    updateSelectionMeta(value);
    editText(value.text);
    if (recoveryOwnerLeaseRef.current === null) persistDraft(value.text);
    // Every keystroke re-evaluates the trailing-token match fresh - a token
    // Escape just closed (useSlashCompletion's token) reopens on the
    // very next text change rather than staying closed indefinitely.
    setSlashToken(parseSlashToken(maskSkillAtoms(value), caret));
  }

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

  // A chip's details: the skill's own description, or - when the live catalog
  // report no longer backs the selection - the name plus why it cannot be
  // found. Command rows never render here, so these details are skill-only by
  // construction. The daemon publishes only available, user-invocable skills
  // (agent/status.go), so a present entry is always usable and there is no
  // unavailable/not-invocable diagnostic to report.
  function skillChipDetails(name: string): string {
    const info = model?.skills?.find((skill) => skill.name === name);
    if (!info) return `${name} — no longer in this session's skill catalog`;
    return info.description ?? name;
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
    textEditor.write(text, text.length);
    editorRef.current?.focus();
  }

  async function activateRecovery(record: MutationRecoveryRecord): Promise<void> {
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
      scheduleCursorRestore(source.getSnapshot().text.length);
      editorRef.current?.focus();
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
  async function submitAction(
    kind: "send" | "queue" | "steer" | "drain",
    options: { text?: string; clearDraftText?: string } = {},
  ): Promise<void> {
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
    const pressedRecoveryTargetRef = source.recoveryTargetRef;
    const pressedRecoveryBarrier =
      submittedRecoveryId === null ? undefined : captureRecoveryStopBarrier(pressedRecoveryTargetRef);
    // Request the durable target and its Stop epoch now, in the press's
    // synchronous prefix. Storage orders this readonly snapshot before any
    // Stop the user presses while the Send waits on persistence or hydration.
    const pressedRecoveryDurable =
      submittedRecoveryId === null ? undefined : readRecoveryWithStopEpoch(submittedRecoveryId);
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
      model?.capabilities,
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
              // The press-time snapshot dates the fence. It may already name a
              // peer's retarget, and a Stop during the waits below must fall
              // after this press rather than inside a later capture.
              const pressedDurable = await pressedRecoveryDurable;
              const durableTargetRef = pressedDurable?.record?.targetRef ?? pressedRecoveryTargetRef;
              const travelingBarrier =
                pressedDurable === undefined || durableTargetRef === pressedRecoveryTargetRef
                  ? pressedRecoveryBarrier
                  : Promise.resolve({ stopEpoch: pressedDurable.stopEpoch });
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
              const sendTargetRef = source.recoveryTargetRef;
              if (sendTargetRef !== durableTargetRef) {
                // The row moved again while this Send was persisting. No fence
                // we hold dates that destination's Stop history, so fail
                // closed rather than let the Send cross a Stop.
                recoveryResendRefused = true;
                throw recoveryWriteRefusal;
              }
              // Destinations this tab never read need their model for
              // expectedInstanceId and capability gates. Hold that foreground
              // claim only for this resend.
              let destinationClaim = false;
              try {
                const targetState = threadsStore.getState();
                if (
                  targetState.threads.get(sendTargetRef) === undefined &&
                  targetState.watchedThreads.get(sendTargetRef) === undefined
                ) {
                  destinationClaim = true;
                  await targetState.ensureThread(sendTargetRef);
                }
                const outcome = await resendRecoveryPendingTurn(
                  submittedRecoveryId,
                  sendTargetRef,
                  kind,
                  submittedText,
                  payload,
                  submittedSkillNames,
                  submittedCommandNames,
                  submittedMentions,
                  travelingBarrier,
                  { [submittedRecoveryId]: committedRevision },
                  source.compositionOwnerId,
                );
                if (outcome.status !== "sent") {
                  recoveryResendRefused = outcome.status === "refused";
                  recoveryAlreadySent = outcome.status === "consumed";
                  throw recoveryResendRefused ? recoveryWriteRefusal : recoveryAlreadySentRefusal;
                }
              } finally {
                if (destinationClaim) threadsStore.getState().releaseThread(sendTargetRef);
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
      // A typed message-producing built-in submits its body while the editor
      // still holds the invocation. Settlement preserves that literal command,
      // which changes the edit revision, so the revision-guarded clear above
      // can decline. Clear the persisted invocation by content instead when the
      // editor still holds exactly what was submitted.
      if (
        options.clearDraftText !== undefined &&
        options.text !== undefined &&
        options.text !== options.clearDraftText &&
        submittedEditorMatches
      ) {
        if (!cleared) {
          updateText("");
          updateSkillNames([]);
          updateSelectionMeta({ text: "", skillNames: [] });
        }
        if (ownedDraftRevisionRef.current === readDraftRevision(ref)) {
          clearDraft(ref);
          ownedDraftRevisionRef.current = readDraftRevision(ref);
        }
      }
      clearSubmittedAttachments(submittedAttachments);
    } catch (error) {
      submissionFailed = true;
      // The local durable write failed. The submitted composer payload stays
      // untouched and no network request was eligible to start.
      if (source.alive && recoveryResendRefused) pushRecoveryRefusal("changed");
      else if (source.alive && recoveryAlreadySent) pushRecoveryRefusal("consumed");
      else if (error !== recoveryWriteRefusal && error !== recoveryAlreadySentRefusal) return;
    } finally {
      if (mountedRef.current) setBusyAction(null);
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

  // handleBuiltinSubmit is the Slack-model half of Enter/submit
  // interception (2026-08-14 decision: "a literal message starting with a
  // known /command executes instead of sending, matching Slack/Discord
  // muscle memory"): runs the matched built-in's RPC instead of routing
  // through send/queue, via builtinCommand.ts's runBuiltinCommand - which
  // ALSO carries the toast/friendlyErrorMessage feedback and the
  // no-double-toast guard, so this handler's only job is the composer-local
  // half: busy-gating, and clearing vs. preserving the draft.
  //
  // submittedText is snapshotted the same way submitAction's own
  // clearIfUnchanged is, so a clear on success never clobbers an edit made
  // while the RPC was still in flight.
  async function handleBuiltinSubmit(match: BuiltinMatch<ScopedCommand>): Promise<void> {
    // The recovery fence, re-read live at the press (the same render-vs-press
    // rule as handleSteerClick): a fenced command's run mints a durable
    // mutation the hub's recovery admission refuses for the obligation's
    // whole window, so running it here could only mint intent that parks
    // until the explicit Resume action clears the fence - the same harm the
    // Send/Steer/queue-strip fences exist to prevent, reached by typing
    // instead of clicking. Which commands carry the fence is declared on the
    // command itself (commands.ts's recoveryFenced); the refusal names the
    // Resume path and preserves the draft, ahead of the busy churn so a
    // refusal never reports busy state.
    if (match.command.recoveryFenced && pressLocalRecoveryFenced(ref)) {
      toasts.push("error", `/${match.command.id} isn't available until this session is resumed`);
      return;
    }
    // A message-producing built-in typed over an active recovery is the same
    // submit the user could have made with Send/Queue/Steer: route its parsed
    // body through the one recovery transaction instead of enqueueing a second
    // mutation beside the recovery. The literal command text is what gets
    // cleared from the editor when the resend succeeds.
    if (
      (match.command.id === "queue" || match.command.id === "steer") &&
      activeRecoveryId !== null &&
      match.argsText.trim()
    ) {
      if (match.command.unavailableReason) {
        toasts.push("error", `/${match.command.id} is ${match.command.unavailableReason}`);
        return;
      }
      const unavailable = match.command.available?.(liveThreadModel(ref) ?? renderedModel);
      if (unavailable !== undefined) {
        toasts.push("error", unavailable);
        return;
      }
      const literal = textRef.current;
      await submitAction(match.command.id, { text: match.argsText, clearDraftText: literal });
      return;
    }
    const submittedText = textRef.current;
    const submittedSkillNames = [...skillNamesRef.current];
    const submittedCommandNames = [...selectionMetaRef.current.commandNames];
    const submittedRevision = draftEditRevisionRef.current;
    const submittedDraftRevision = readDraftRevision(ref);
    setBusyAction("submit");
    const ctx: PaletteRunContext = {
      sessionRef: ref,
      sessionPaneId: paneId ?? null,
      onPage: "session",
      toasts,
      // Neither method is reachable here: both belong to app-global palette
      // commands (/search, /help), never to a session-scoped built-in - see
      // commandSurface's own doc comment on why the two never overlap.
      ui: { clearToSearch: () => {}, showHelp: () => {} },
    };
    const outcome = await runBuiltinCommand(match, ctx);
    setBusyAction(null);
    if (outcome.ok)
      clearIfUnchanged(
        submittedText,
        submittedRevision,
        submittedDraftRevision,
        submittedSkillNames,
        submittedCommandNames,
      );
    // On failure: the draft is left exactly as typed (clearIfUnchanged is
    // simply never called) - runBuiltinCommand has already toasted why.
  }

  function handleFormSubmit(event: FormEvent): void {
    event.preventDefault();
    if (actionPending) return;
    if (!hasContent) return; // empty composer: no-op, no request, no message
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    // The Slack-model interception: a draft with no attachments or skill
    // selections that parses as a known BUILT-IN session command
    // (sessionBuiltins, above) runs that command instead of sending the text
    // as a message. A message carrying an attachment is never read as a
    // command, regardless of its text; neither is one carrying a staged skill
    // selection - that draft falls through to the ordinary routing below so
    // the selection survives as part of the request. Everything that does NOT
    // match - an unknown "/foo", or a plugin catalog command
    // (matchBuiltinInvocation only ever matches sessionBuiltins, never the
    // catalog - see that function's own doc comment) - falls straight through
    // to the ordinary routing below, unchanged: that's the escape hatch.
    if (!hasAttachments && skillNames.length === 0 && selectionMeta.commandNames.length === 0) {
      const match = matchBuiltinInvocation(text, sessionBuiltins);
      if (match) {
        void handleBuiltinSubmit(match);
        return;
      }
    }
    // The same decision the button and its tooltip rendered from
    // (the send/queue decision, substitution included), re-derived at the press over
    // the store's live model: the two disagree only when a status frame landed
    // after the render, and then the frame is what the submit has to follow.
    // The gate's send/queue action is the route; none is the refusal below.
    const decision = sendQueueDecisionFor(
      liveThreadModel(ref) ?? renderedModel,
      ownPendingSend(pendingTurnEntries(ref, "send")),
      threadsStore.getState().restartBlockingObligations.has(ref),
      {
        // Read live, like stopInFlight and queuedNonSend beside it: the
        // render-time blockedMutations snapshot cannot have caught a row
        // published between the render and the press, while the store-wide
        // predicate resumeOnlyModel (which enqueue and dispatch use) reads
        // hasBlockedUnknown fresh. Routing both sides through the same live
        // read keeps the press from folding a resume ahead of an uncertain row.
        uncertainMessages: hasBlockedUnknown(ref),
        stopInFlight: threadsStore.getState().stoppingRefs.has(ref),
        queuedNonSend: hasQueuedNonSend(ref),
        // Read live like the signals beside it: the render passes this too, so
        // a snapshot that became authoritative between the render and the
        // press routes to the Send-resumes face rather than reading as an
        // owner-retained snapshot and refusing.
        mutationStateAuthoritative: threadsStore.getState().mutationAuthorityRefs.has(ref),
      },
    );
    const route = decision.action;
    if (route === "none") {
      toasts.push("error", "Send is not available for this session");
      return;
    }
    // Hand off before Send becomes disabled. Never refocus on completion:
    // the user may have moved to another control or session while submitting.
    const initiator = submitButtonRef.current;
    if (
      initiator &&
      !initiator.disabled &&
      (event.nativeEvent as SubmitEvent).submitter === initiator &&
      initiator.ownerDocument.activeElement === initiator
    ) {
      editorRef.current?.focus();
    }
    void submitAction(route);
  }

  function handleSteerClick(): void {
    if (actionPending) return;
    // The recovery fence, re-read live at the press: a Stop can arm it after
    // the render that offered this button, and the hub refuses turn/steer
    // (and turn/drainAsSteer on the drain route) for the whole window, so an
    // offered press could only mint durable intent that parks until the
    // explicit Resume action clears the fence.
    if (pressLocalRecoveryFenced(ref)) {
      toasts.push("error", STEER_RECOVERY_FENCED_REASON);
      return;
    }
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    const route = decideSteerRoute({
      hasText,
      hasAttachments,
      hasSkills: skillNames.length > 0 || selectionMeta.commandNames.length > 0,
      queueDepth: liveThreadModel(ref)?.queue?.depth ?? queueDepth,
    });
    if (route === "none") {
      editorRef.current?.focus();
      return;
    }
    // Readiness is sessionControls' (submitRouting.ts), read from the store at
    // the press (stores/liveControls.ts): a status frame folded after the
    // render that offered the button decides the press. It is checked here as
    // well as on the button because Shift+Enter reaches this handler with no
    // button on screen.
    const reason = pressRefusal(ref, route === "drain" ? "drain" : "steer");
    if (reason !== undefined) {
      const verb = route === "drain" ? "Drain" : "Steer";
      toasts.push("error", reason === NO_ACTIVE_TURN ? `${verb} failed: ${reason}` : reason);
      return;
    }
    void submitAction(route);
  }

  async function handleInterruptClick(): Promise<void> {
    if (actionPending) return;
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

  // Legacy suppresses every one of these keybindings entirely inside a
  // framed side-pane iframe (isInPane()) - verified moot, not assumed: this
  // rewrite's multi-pane layout is dockview panels in the SAME document
  // (shell/DockHost.tsx), never cross-document <iframe> panes at all (grep
  // for "iframe" across src turns up nothing) - the whole concept this
  // legacy gate defended against doesn't exist here, so there is no
  // isInPane()-equivalent check to port.
  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>): void {
    // The slash menu's keys come first: with it open, a committing Enter must not reach the Enter-to-send routing below.
    if (handleSlashKeyDown(event)) return;
    // SHOULD-FIX (product decision): "/" as the first character of an EMPTY
    // composer used to preventDefault and open the MODAL command palette
    // instead of typing (floor §2.1, legacy renderer.js:6914) - which made
    // the inline slash menu above unreachable in its single most common
    // case, an empty composer. "/" is now always a literal keystroke here;
    // typing it lets it land in the draft and reach handleTextChange below,
    // which is what opens the inline menu (parseSlashToken/slashOpen) the
    // same way it would for "/" typed anywhere else. The modal palette
    // remains reachable via Mod+K (AppShell.tsx) regardless.
    if (event.key !== "Enter") return;
    // An IME composition's own confirm keystroke also fires as a plain
    // "Enter" keydown (e.g. finishing a Japanese/Chinese candidate) - that
    // is the IME committing text, not the user asking to submit, so it must
    // never be read as one.
    if (event.nativeEvent.isComposing) return;
    if (event.metaKey || event.ctrlKey) {
      event.preventDefault();
      formRef.current?.requestSubmit();
      return;
    }
    const enterToSendNow = prefsStore.getState().enterToSend; // fresh, not the render-time `enterToSend` closure
    if (event.shiftKey) {
      if (enterToSendNow) return; // literal newline - avoids doubling up enterToSend's own Enter-submits meaning
      event.preventDefault();
      handleSteerClick();
      return;
    }
    if (!event.altKey && enterToSendNow) {
      event.preventDefault();
      formRef.current?.requestSubmit();
    }
    // else: literal newline (enterToSend off, or an unhandled modifier combo)
  }

  function handlePaste(event: { clipboardData: DataTransfer | null }): void {
    const files = imageFilesFromClipboard(event.clipboardData);
    if (files.length === 0) return; // text-only paste: never preventDefault, let the browser insert it
    attachments.ingestFiles(files, (message) => toasts.push("error", message));
    // Never preventDefault, even for an image+text paste: the text portion
    // still needs the browser's own default insertion (parity-m5-
    // composer.md §G).
  }

  function handleFilePickerChange(event: { target: HTMLInputElement }): void {
    const files = Array.from(event.target.files ?? []);
    if (files.length > 0) attachments.ingestFiles(files, (message) => toasts.push("error", message));
    event.target.value = ""; // re-picking the identical file must re-fire change
  }

  function handleFollowUpFocus(): void {
    followUpFocusSequenceRef.current += 1;
    setFollowUpFocused(true);
  }

  function handleFollowUpBlur(): void {
    const sequence = ++followUpFocusSequenceRef.current;
    // Native focus transfers run microtasks between blur and destination focus.
    // Wait until the next frame so a control or portal keeps the empty editor
    // expanded without shrinking it during a focus transfer.
    requestAnimationFrame(() => {
      if (mountedRef.current && followUpFocusSequenceRef.current === sequence) setFollowUpFocused(false);
    });
  }

  // Slash options prevent mousedown's default focus transfer, so only leaving
  // the editor closes its completion menu. The card owns collapse separately.
  function isEditorEvent(target: EventTarget | null): boolean {
    // Realm-independent: Dockview *moves* an existing editor into a popout
    // window, which changes its ownerDocument but keeps its original realm and
    // prototype, so any `instanceof Element` - the opener's or the popout's -
    // rejects it. nodeType and closest survive adoption.
    const element =
      target !== null && typeof target === "object" && (target as Node).nodeType === 1 ? (target as Element) : null;
    return element !== null && element.closest('[role="textbox"]') !== null && !!formRef.current?.contains(element);
  }

  function handleEditorBlur(): void {
    setSlashToken(null);
  }

  function handleEditorCompositionStart(event: { target: EventTarget | null }): void {
    if (!isEditorEvent(event.target)) return;
    composingRef.current = true;
    reportEditing();
  }

  function handleEditorCompositionEnd(event: { target: EventTarget | null }): void {
    if (!isEditorEvent(event.target)) return;
    composingRef.current = false;
    reportEditing();
  }

  return (
    <div
      className={CLASS.composer}
      ref={composerRootRef}
      onFocusCapture={handleFollowUpFocus}
      onBlurCapture={handleFollowUpBlur}
    >
      {mutationWriteStalled && (
        <div className={CLASS.storageStatus} role="status" aria-label="Message storage">
          Browser storage has stalled. A message update is still pending; keep this tab open while Evener waits for
          confirmation.
        </div>
      )}
      {/* The ask dock no longer renders here: pending questions are the
          transcript's trailing row (Session.tsx passes AskDock as
          TranscriptBody's trailingRow), so the answering surface scrolls
          with the content instead of covering the footer. This component's
          own half of the contract is unchanged: useAskDockPending(ref)
          (askPending, above) hides/inerts the input row below while a
          question is pending. */}
      {/* Screen-reader-only: announces the OTHER half of parity-m5-
          composer.md line 118's status-region transition - AskDock's own
          anchor announces "Answer the agent's questions." on entry but
          unmounts entirely on resolve, so only this component can announce
          exiting ask-pending mode (readyAnnouncement's own doc comment
          above). Visually hidden, not a persistent visible banner - once
          the textarea itself is visibly back, there is nothing left for a
          permanent "ready" line to usefully say (honest-liveness: only
          announce a real transition, never a static claim). */}
      <VisuallyHidden>
        <span role="status" aria-live="polite" data-testid="composer-ready-announcement">
          {readyAnnouncement}
        </span>
      </VisuallyHidden>
      {/* T3: queue strip - the queue preview (model.queue) above the input
          row; getComposerText/onRestoreToComposer/onDrainSuccess are this
          integration's own seam implementations, see each one's own doc
          comment above. busy/onDrainBusyChange share this component's own
          busyAction gate both ways (BusyAction's own "drain" doc comment). */}
      <QueueStrip
        ref={ref}
        getComposerText={getComposerText}
        onRestoreToComposer={restoreTextToComposer}
        activeRecoveryId={activeRecoveryId ?? undefined}
        activeRecoveryRevision={recoveryOwnerLeaseRef.current?.compositionRevision}
        getRecoveryRevision={getRecoveryRevisionForDrain}
        onEditRecovery={activateRecovery}
        tryAcquireRecoveryOperation={source.tryAcquireRecoveryOperation}
        onDrainSuccess={handleDrainSuccess}
        busy={actionPending}
        onDrainBusyChange={(draining) => {
          if (mountedRef.current) setBusyAction(draining ? "drain" : null);
        }}
      />
      {/* Staged attachments. One rendering for every state, so nothing here
          swaps element types under a user mid-gesture - AttachmentTile.tsx's
          own header comment has the mechanism and the bug it closed. */}
      {hasAttachments && (
        <div className={CLASS.attachments} hidden={askPending} inert={askPending}>
          {attachments.items.map((item) => (
            <AttachmentTile key={item.marker} item={item} onRemove={() => attachments.removeItem(item.marker)} />
          ))}
        </div>
      )}
      {!askPending && (
        <>
          <TasksPanel ref={tasksPanelRef} sessionRef={ref} model={model} hideTrigger />
          <CurrentWork
            task={model.tasks?.current?.description}
            goal={model.goal?.objective}
            goalStatus={model.goal?.status}
            onOpenTasks={showTasks}
            onEditGoal={() => editGoal((model.goal?.objective ?? "").trim())}
          />
        </>
      )}
      {pendingGoalReplacement !== null && (
        <ConfirmDialog
          open
          title="Replace draft?"
          confirmLabel="Replace draft"
          cancelLabel="Keep draft"
          onConfirm={() => {
            replaceComposerWithGoalDraft(pendingGoalReplacement);
          }}
          onCancel={() => setPendingGoalReplacement(null)}
        >
          This will discard the current composer contents.
        </ConfirmDialog>
      )}
      {(!ended || showFollowUpCard) && (
        // data-composer is the hold-hints overlay's anchor (shell/holdhints):
        // the composer.focus chip positions itself above this wrapper. The
        // value carries this pane's session ref, and data-composer-pane its
        // owning pane id, so the overlay can anchor to the FOCUSED pane's
        // composer when several are mounted - including an ordinary pane and a
        // cascade column showing the same conversation.
        <div className={CLASS.formAnchor} data-composer={ref} data-composer-pane={paneId}>
          {/* Anchored above the control row inside the card below, opening
              upward - see slashcompletionmenu.module.css's header comment
              for the anchored-float recipe. Mounted only
              while a token has real catalog matches (slashOpen), never for
              an empty/no-match filter. */}
          {slashOpen && (
            <SlashCompletionMenu
              id={slashListboxId}
              items={slashItems}
              highlightedIndex={slashActiveIndex}
              onSelect={commitSlashCompletion}
            />
          )}
          <form
            ref={formRef}
            onSubmit={handleFormSubmit}
            data-testid="composer-form"
            data-mode={ended ? "follow-up" : "message"}
            onCompositionStart={handleEditorCompositionStart}
            onCompositionEnd={handleEditorCompositionEnd}
          >
            <Dropzone onFiles={(files) => attachments.ingestFiles(files, (message) => toasts.push("error", message))}>
              <PromptCard
                data-testid="composer-input-card"
                hidden={askPending}
                field={
                  <SkillEditor
                    ref={editorRef}
                    value={{ text, skillNames, ...selectionMeta }}
                    restoreEpoch={restoreEpoch}
                    skillDetails={skillChipDetails}
                    commandDetails={(name) =>
                      visibleSlashCatalog.find((item) => slashCommandInvocation(item) === `/${name}`)?.description ??
                      `${name} — no longer in this session's command catalog`
                    }
                    onChange={handleTextChange}
                    onKeyDown={handleKeyDown}
                    onPaste={handlePaste}
                    aria-controls={slashActiveId ? slashListboxId : undefined}
                    aria-activedescendant={slashActiveId ?? undefined}
                    minLines={ended ? (followUpEngaged ? 3 : 1) : undefined}
                    onBlur={handleEditorBlur}
                    placeholder={ended ? "Send a follow-up…" : "Message the agent…"}
                    aria-label="Message"
                  />
                }
                leading={
                  /* data-testid on every control in this row: two different
                     buttons here start with "Steer" (this one and
                     QueueStrip's "Steer queue now"), so tests address
                     controls by a stable hook instead of navigating by
                     accessible name - the naming style follows StatusRow's
                     own status-row-* testids. */
                  <div className={CLASS.leading}>
                    <Tooltip label="Attach an image">
                      <IconButton
                        label="Attach image"
                        icon={<AttachIcon />}
                        variant="quiet"
                        size="xs"
                        type="button"
                        data-testid="composer-attach"
                        onClick={() => fileInputRef.current?.click()}
                      />
                    </Tooltip>
                    <MemoizedSessionChrome
                      ref={ref}
                      paneId={paneId}
                      placement="composer"
                      discoverActivity
                      turnVerbs={turnVerbs}
                    />
                  </div>
                }
                actions={
                  <>
                    {/* Distinct keys replace the actual button, so a press
                        begun on Send cannot finish on Stop. */}
                    {inlineStop ? (
                      <Tooltip key="stop" label="Stop the current turn">
                        <Button
                          variant="dangerQuiet"
                          size="xs"
                          type="button"
                          data-testid="composer-stop"
                          onClick={() => void handleInterruptClick()}
                          // Keep pending gates after the brief swap guard ends.
                          disabled={actionPending || !inlineStopReady}
                        >
                          Stop
                        </Button>
                      </Tooltip>
                    ) : (
                      <Tooltip key="send" label={submitTooltip}>
                        <Button
                          ref={submitButtonRef}
                          type="submit"
                          variant={showSteer ? "quiet" : "primary"}
                          size="xs"
                          data-testid="composer-submit"
                          aria-label="Send"
                          icon={<SendIcon />}
                          // canCompose comes from the availability table, which
                          // reports both-false for an idle finished session: it
                          // answers "can this turn be sent to right now", and a
                          // follow-up to a finished session resumes it first
                          // (only once that resume is in flight does the table
                          // have an answer of its own). The capability is
                          // the authority there, the same way it is for whether
                          // this card renders at all - otherwise a session the hub
                          // will happily resume shows a permanently dead Send.
                          // The capability alone does not lift the recovery fence:
                          // the send/queue gate refuses every fenced status, so a
                          // fenced session whose snapshot still
                          // advertises send:true (the hub stamps it on closed
                          // frames too) renders a disabled Send, not a refusal
                          // toast.
                          disabled={
                            actionPending ||
                            !hasContent ||
                            !(ended
                              ? (canSendWhenEnded || fence.sendDriven) && canCompose && !fence.stillFenced
                              : canCompose)
                          }
                        >
                          <span className={CLASS.submitLabel}>Send</span>
                        </Button>
                      </Tooltip>
                    )}
                    {/* Steer rides in the menu when the row cannot fit it. */}
                    {showSteer && !narrow && (
                      <Tooltip label={steerTooltipLabel({ recoveryFenced: steerRecoveryFenced, enterToSend })}>
                        <Button
                          variant="primary"
                          size="xs"
                          type="button"
                          data-testid="composer-steer"
                          onClick={handleSteerClick}
                          // Same as Stop above: busy + the steer capability
                          // already gate this control's existence. The recovery
                          // fence gates the press the same way the Send button's
                          // does, and the tooltip says why (kata 2f41) instead of
                          // describing an action the fence refuses. The fence is
                          // an aria-disabled refusal, not the native attribute, so
                          // the button keeps focus and hover and that reason
                          // stays reachable.
                          aria-disabled={steerRecoveryFenced || undefined}
                          disabled={actionPending}
                        >
                          Steer
                        </Button>
                      </Tooltip>
                    )}
                  </>
                }
              />
            </Dropzone>
            <input ref={fileInputRef} type="file" accept="image/*" multiple hidden onChange={handleFilePickerChange} />
          </form>
        </div>
      )}
    </div>
  );
}
