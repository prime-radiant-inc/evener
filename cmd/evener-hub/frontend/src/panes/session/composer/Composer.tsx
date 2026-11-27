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
  decideSteerRoute,
  type FileMentionTarget,
  markerText,
  mergeDraftText,
  mergeSlashCommands,
  NO_ACTIVE_TURN,
  SHUT_DOWN_STATUSES,
  sessionActionError,
  slashCommandInvocation,
  stripMarker,
  validMarkerRanges,
} from "@evener/appwire-client";
import {
  type FormEvent,
  memo,
  type FocusEvent as ReactFocusEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";
import { activitySidebarStore } from "../../../shell/activitybar/activitySidebarStore";
import { isAppleTouchDevice } from "../../../shell/iPhoneGlass";
import { iPhoneHost, tellIPhoneHost, useIPhoneShare } from "../../../shell/iPhoneHost";
import { alignCreateWithComposer } from "../../../shell/rail/alignCreateWithComposer";
import { paneToURL } from "../../../shell/routing";
import type { SessionMenuTurnVerbs } from "../../../shell/sessionMenu/SessionMenu";
import { useIsMobile } from "../../../shell/useIsMobile";
import { useMountAutofocus } from "../../../shell/useMountAutofocus";
import { useConnectionStore } from "../../../stores/connection";
import {
  controlsFor,
  isRecoveryFenced,
  liveThreadModel,
  pressLocalRecoveryFenced,
  pressRefusal,
  recoveryFence,
} from "../../../stores/liveControls";
import { prefsStore, usePrefsStore } from "../../../stores/prefs";
import type { ResumeOnlySignals } from "../../../stores/recoveryFence";
import { hasQueuedNonSend, useThreadsStore } from "../../../stores/threads";
import {
  Button,
  ConfirmDialog,
  Dropzone,
  InlineNotice,
  PromptCard,
  SendIcon,
  Tooltip,
  useToasts,
  VisuallyHidden,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { SessionChrome, type SessionChromeProps } from "../chrome/SessionChrome";
import { AttachmentTile } from "./AttachmentTile";
import { useAskDockPending } from "./askDockPending";
import { AttachImageControl } from "./attachments/AttachImageControl";
import { imageFilesFromClipboard } from "./attachments/clipboard";
import { ImagePickerInput } from "./attachments/ImagePickerInput";
import { type TextEditor, useAttachments } from "./attachments/useAttachments";
import { CurrentWork } from "./CurrentWork";
import styles from "./composer.module.css";
import { consumeComposerFocus, requestComposerFocus, useComposerFocusRequest } from "./composerFocus";
import {
  STEER_RECOVERY_FENCED_REASON,
  STEER_UNBACKED_IMAGE_REASON,
  steerTooltipLabel,
  submitTooltipLabel,
} from "./controlTooltips";
import { FileCompletionMenu } from "./FileCompletionMenu";
import { useDuoCoverLayout, useLandscapePhoneLayout, useNarrowComposer } from "./narrowComposer";
import {
  editQueuedPendingTurn,
  refreshPendingTurnsProjection,
  useBlockedMutationEntries,
} from "./queue/pendingTurnsStore";
import {
  consumeQueueRowRestore,
  publishActiveRecovery,
  publishComposerBusy,
  useQueueRowRestoreRequest,
} from "./queueRowInsert";
import { consumeQuoteInsert, useQuoteInsertRequest } from "./quoteInsert";
import { compositionIsEmpty } from "./recovery/recoveryDraft";
import { SkillEditor, type SkillEditorHandle } from "./SkillEditor";
import { SlashCompletionMenu } from "./SlashCompletionMenu";
import { StopIcon } from "./StopIcon";
import { maskSkillAtoms, type SkillEditorValue } from "./skillDocument";
import type { ComposerSourceState } from "./sourceState";
import { recordStoplessComposer } from "./stoplessComposer";
import { pushRecoveryRefusal, unbackedImageMarkersIn } from "./submitProtocol";
import { type ComposerSubmission, useBuiltinSubmission } from "./useBuiltinSubmission";
import { resolveCompletionTokens, useFileCompletion } from "./useFileCompletion";
import { useInlineStopReady } from "./useInlineStopReady";
import { useSlashCompletion } from "./useSlashCompletion";

export interface ComposerProps {
  ref: string;
  paneId?: string;
  // Whether this composer's pane is the workspace's focused one. Mount
  // autofocus keys on it: a session loading into a background tab must never
  // yank keyboard focus away from what the reader is doing.
  focused?: boolean;
  // The seat may suppress mount autofocus for a navigation or restore intent.
  mountAutofocus?: boolean;
  source: ComposerSourceState;
  submission?: ComposerSubmission;
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
  attachSlot: requireClass(styles.attachSlot, "composer.module.css", "attachSlot"),
  editorSlot: requireClass(styles.editorSlot, "composer.module.css", "editorSlot"),
  menuSlot: requireClass(styles.menuSlot, "composer.module.css", "menuSlot"),
  chromeSlot: requireClass(styles.chromeSlot, "composer.module.css", "chromeSlot"),
  formAnchor: requireClass(styles.formAnchor, "composer.module.css", "formAnchor"),
  submitLabel: requireClass(styles.submitLabel, "composer.module.css", "submitLabel"),
  stopLabelStack: requireClass(styles.stopLabelStack, "composer.module.css", "stopLabelStack"),
  stopMeasure: requireClass(styles.stopMeasure, "composer.module.css", "stopMeasure"),
};

// The composer re-renders on every draft keystroke and nothing in the chrome
// reads the draft, so its mounts go through this memo. It renders the imported
// SessionChrome binding at render time instead of wrapping the function itself
// (memo(SessionChrome)), so a replacement of the module export still reaches
// these mounts.
const MemoizedSessionChrome = memo(function MemoizedSessionChrome(props: SessionChromeProps) {
  return <SessionChrome {...props} />;
});

// The missing model's turnVerbs offering: one frozen object, because a fresh
// {} on every recompute would defeat MemoizedSessionChrome's
// shallow-equality bailout for a value that carries no information.
const NO_TURN_VERBS: SessionMenuTurnVerbs = Object.freeze({});

// Whether a node belongs to the composer: its own subtree, or one of the
// menus it portals outside it (the session menu, a completion list, a dialog).
// A press or focus inside keeps composer editing active.
function isComposerOwned(root: HTMLElement, node: Node | null): boolean {
  if (node === null) return false;
  if (root.contains(node)) return true;
  const element = node.nodeType === 1 ? (node as Element) : null;
  return (
    element !== null &&
    typeof element.closest === "function" &&
    element.closest('[role="menu"], [role="listbox"], [role="dialog"]') !== null
  );
}

export function Composer(props: ComposerProps) {
  if (props.submission !== undefined) return <ComposerWithSubmission {...props} submission={props.submission} />;
  return <ComposerWithOwnSubmission {...props} />;
}

function ComposerWithOwnSubmission(props: ComposerProps) {
  const submission = useBuiltinSubmission(props.source, props.paneId);
  return <ComposerWithSubmission {...props} submission={submission} />;
}

function ComposerWithSubmission({
  ref,
  paneId,
  focused = false,
  mountAutofocus = true,
  source,
  submission,
  onEditingChange,
}: ComposerProps & { submission: ComposerSubmission }) {
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
  const toasts = useToasts();
  const connectionClient = useConnectionStore((s) => s.client);
  const connectionReady = useConnectionStore((s) => s.state === "ready");
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
  const formAnchorRef = useRef<HTMLDivElement>(null);
  // A composer press (Send, Attach, a menu trigger) can blur the editor to
  // body on touch without moving focus to the pressed control. Hold the
  // editing state through that task so the footer does not move under the tap.
  const composerPointerRef = useRef(false);
  // Torn down by the next press or unmount, so a press whose click never
  // arrives cannot accumulate document listeners.
  const composerPressReleaseRef = useRef<(() => void) | null>(null);
  // Identifies the press that owns the hold, so a stale release's deferred
  // clear cannot clear a later press's hold.
  const composerPressSeqRef = useRef(0);
  // Whether the press in flight began outside the composer and is waiting for
  // its click. The card is in flow, so ending editing at the press would shrink
  // it before mouseup and move the pressed control out from under the pointer,
  // swallowing that control's click. The editing exit waits for the click instead; a
  // press that never clicks never ends editing.
  const outsidePressRef = useRef(false);
  const submitButtonRef = useRef<HTMLButtonElement>(null);
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
    adoptedImageMarkers,
    adoptedImageRanges,
    stagedImageRanges,
  } = useSyncExternalStore(source.subscribe, source.getSnapshot, source.getSnapshot);
  const sharePath = paneToURL("session", { ref });
  const [shareWriteFailure, setShareWriteFailure] = useState<{
    id: string;
    text: string;
    path: string;
    ownerId: string;
  } | null>(null);
  const compositionOwnerId = source.compositionOwnerId;
  const shareFailureIsCurrent =
    shareWriteFailure?.path === sharePath && shareWriteFailure.ownerId === compositionOwnerId;
  useEffect(() => {
    if (shareWriteFailure && (shareWriteFailure.path !== sharePath || shareWriteFailure.ownerId !== compositionOwnerId))
      setShareWriteFailure(null);
  }, [shareWriteFailure, sharePath, compositionOwnerId]);
  function acknowledgeShare(id: string, path: string): void {
    tellIPhoneHost({ type: "sharePlaced", id, path });
    setShareWriteFailure((pending) => (pending?.id === id ? null : pending));
  }
  useIPhoneShare(focused && !activeRecoveryId ? sharePath : null, (id, text) => {
    if (!sharePath) return;
    if (source.placeShare(id, text)) acknowledgeShare(id, sharePath);
    else setShareWriteFailure({ id, text, path: sharePath, ownerId: compositionOwnerId });
  });
  function retryShareWrite(): void {
    if (!shareFailureIsCurrent || !shareWriteFailure || !sharePath) return;
    const { id, text, path } = shareWriteFailure;
    if (source.placeShare(id, text)) acknowledgeShare(id, path);
  }
  // The anchors this composer read out of a draft it did not write and cannot
  // back with bytes: the source's adoption record intersected with the text as
  // it stands. Read once here so the refusal, the reason beside the draft, and
  // Send's own disabled state all describe the same draft.
  const unbackedImageMarkers = unbackedImageMarkersIn(text, adoptedImageMarkers);
  const hasUnbackedImage = unbackedImageMarkers.length > 0;
  const restoredRecoveryRef = useRef(activeRecoveryId);
  useLayoutEffect(() => {
    if (restoredRecoveryRef.current === activeRecoveryId) return;
    const activating = restoredRecoveryRef.current === null;
    restoredRecoveryRef.current = activeRecoveryId;
    // Automatic restoration starts without an owner. Replacing an existing
    // owner after peer consumption preserves the editor, including selection.
    // Explicit recovery Edit restores its caret in the queue-row restore seam.
    if (activating && activeRecoveryId !== null) scheduleCursorRestore(source.getSnapshot().text.length);
  }, [activeRecoveryId, scheduleCursorRestore, source]);
  const selectionMeta = useMemo(() => ({ commandNames, mentions }), [commandNames, mentions]);
  const {
    textRef,
    skillNamesRef,
    selectionMetaRef,
    recoveryOwnerLeaseRef,
    recoveryWriteVersionRef,
    recoveryReplacementEpochRef,
    recoveryOwnsLocalDraftRef,
  } = source.continuity;
  const {
    editText,
    updateSkillNames,
    editSkillNames,
    updateSelectionMeta,
    setActiveRecoveryId,
    setAdoptedImageRanges,
    setStagedImageRanges,
    clearAdoptedImageMarkers,
    markRestore,
    persistDraft,
    writeDraft,
  } = source;
  const { actions, sessionBuiltins, submitBuiltin } = submission;
  const { availability, busy: actionPending, showStop, submit: submitAction, stop: handleInterruptClick } = actions;
  const mountedRef = useRef(false);
  const [pendingGoalReplacement, setPendingGoalReplacement] = useState<string | null>(null);
  // Content latches the expanded shape while its editor remains in use. Focus
  // alone never sets this, so mount autofocus still gets the resting row.
  const [contentEngaged, setContentEngaged] = useState(false);
  const hasContentRef = useRef(false);
  // Steer moves into the menu below the phone-width boundary.
  // narrowComposer.ts measures the pane, not the viewport.
  const [narrow, attachNarrowRef] = useNarrowComposer();
  const duoCoverLayout = useDuoCoverLayout();
  const landscapePhoneLayout = useLandscapePhoneLayout();
  // The phone shell's control row is menu-only in every state; model and
  // effort live in Overview and Settings (design-system.md section 12).
  const phoneShellLayout = isMobile || landscapePhoneLayout;
  const appleComposer = isAppleTouchDevice() || !!iPhoneHost();
  const composerRootRef = useRef<HTMLDivElement | null>(null);
  const attachComposerRoot = useCallback(
    (element: HTMLDivElement | null) => {
      composerRootRef.current = element;
      attachNarrowRef(element);
    },
    [attachNarrowRef],
  );

  // The editor's own focus is the phone keyboard's signal. It is a DOM
  // attribute rather than React state so focus does not re-render the
  // composer; focus on a composer button alone must not set it.
  const endComposerEditingSoon = (): void => {
    // The next focus target is only known after the focusout/focusin pair, so
    // decide in a microtask. Focus staying anywhere in the composer, or in one
    // of its portaled menus, keeps editing on; anything else ends it.
    queueMicrotask(() => {
      const anchor = formAnchorRef.current;
      const root = composerRootRef.current;
      if (!anchor || !root) return;
      if (composerPointerRef.current) return;
      // An outside press holds editing until its click lands; see
      // outsidePressRef.
      if (outsidePressRef.current) return;
      const active = anchor.ownerDocument.activeElement;
      if (isComposerOwned(root, active)) return;
      anchor.removeAttribute("data-composer-editing");
    });
  };
  const reconcileContentEngagement = useCallback((): void => {
    const root = composerRootRef.current;
    if (!root || outsidePressRef.current || hasContentRef.current) return;
    if (isComposerOwned(root, root.ownerDocument.activeElement)) return;
    setContentEngaged(false);
  }, []);
  const holdComposerPress = (event: ReactPointerEvent<HTMLDivElement>): void => {
    // Only the primary press can hold editing: a right or middle press fires
    // pointerdown but no click, so a hold armed here could never be released.
    if (event.button !== 0) return;
    if (appleComposer && isEditorEvent(event.target)) {
      formAnchorRef.current?.toggleAttribute("data-touch-editing", event.pointerType === "touch");
    }
    // Drop the previous press's listeners without its deferred clear: the new
    // press owns the hold from this moment.
    composerPressReleaseRef.current?.();
    const press = ++composerPressSeqRef.current;
    composerPointerRef.current = true;
    const doc = composerRootRef.current?.ownerDocument;
    if (!doc) return;
    const release = () => {
      composerPressReleaseRef.current = null;
      doc.removeEventListener("click", release, true);
      doc.removeEventListener("pointercancel", release, true);
      // A press's focus transfer can run after the click; clear on a later
      // task so the blur that follows still sees the hold, then reconcile
      // the resting row now that the transfer has settled.
      setTimeout(() => {
        if (composerPressSeqRef.current !== press) return;
        composerPointerRef.current = false;
        reconcileContentEngagement();
      }, 0);
    };
    composerPressReleaseRef.current = release;
    doc.addEventListener("click", release, true);
    doc.addEventListener("pointercancel", release, true);
  };
  useEffect(
    () => () => {
      composerPressReleaseRef.current?.();
    },
    [],
  );
  useEffect(() => {
    const doc = composerRootRef.current?.ownerDocument;
    if (!doc) return undefined;
    // A press outside the composer (its portaled menus included) ends editing
    // when focus has already left the editor: touch may leave focus on body,
    // so no blur follows.
    //
    // The exit runs on the completed click, not the press: the card is in flow,
    // so clearing at the press shrinks it before mouseup and the control under
    // the pointer moves away before it can receive its click. widgets/internal/
    // useDismissOnOutsidePress.ts states the same rule for in-flow exits.
    // A click only fires when press and release share a target, so a drag or a
    // cancelled press leaves the composer alone (Jesse's ruling: exit only
    // ever on a click).
    let releaseTimer: ReturnType<typeof setTimeout> | null = null;
    let editingTimer: ReturnType<typeof setTimeout> | null = null;
    // Bumped by every press that can end a hold (pointerdown, pointercancel,
    // and the release timer), so a deferred exit can tell that its own press
    // was superseded and drop out.
    let outsidePressSeq = 0;
    function cancelRelease(): void {
      if (releaseTimer === null) return;
      clearTimeout(releaseTimer);
      releaseTimer = null;
    }
    function cancelEditingEnd(): void {
      if (editingTimer === null) return;
      clearTimeout(editingTimer);
      editingTimer = null;
    }
    const onPointerDown = (event: PointerEvent) => {
      const anchor = formAnchorRef.current;
      const root = composerRootRef.current;
      if (!anchor || !root || !anchor.hasAttribute("data-composer-editing")) return;
      if (event.button !== 0) return;
      const target = event.target as Node | null;
      if (isComposerOwned(root, target)) return;
      // The hold arms even while the editor still holds focus: the browser moves
      // focus as the press's default action, after this listener runs, so that
      // focus is the one the press is about to leave. Whether it left is decided
      // at the click, where it has settled.
      cancelRelease();
      outsidePressRef.current = true;
      outsidePressSeq += 1;
    };
    // The click resolves the hold and ends editing, only after the click has
    // finished dispatching. It is captured, not bubbled, because transcript
    // controls that call stopPropagation() on their own click never reach a
    // bubble listener and would otherwise leave editing active. The exit
    // itself waits for a later task: the footer is in flow, so dropping it inside
    // dispatch would move layout under the control's own handlers.
    const onClick = (event: MouseEvent) => {
      if (!outsidePressRef.current) return;
      // The pointerup timer only covers a press that never clicked, so
      // cancelling it here is harmless.
      cancelRelease();
      const press = outsidePressSeq;
      const target = event.target as Node | null;
      cancelEditingEnd();
      // The hold stays armed across the deferral, so a blur or frame
      // reconciliation that runs between here and the exit still sees it.
      editingTimer = setTimeout(() => {
        editingTimer = null;
        if (outsidePressSeq !== press) return;
        outsidePressRef.current = false;
        const anchor = formAnchorRef.current;
        const root = composerRootRef.current;
        if (!anchor || !root) return;
        // A click that somehow lands back inside the composer is not an exit,
        // and one that arrives after editing already ended has nothing to do.
        if (isComposerOwned(root, target)) return;
        if (!anchor.hasAttribute("data-composer-editing")) return;
        // Focus that still sits in the editor is a transcript pan, not an
        // editing exit; only a real focus loss ends it.
        const active = doc.activeElement;
        if (active && root.contains(active) && active.closest('[role="textbox"]')) return;
        anchor.removeAttribute("data-composer-editing");
        reconcileContentEngagement();
      }, 0);
    };
    const onPointerCancel = () => {
      outsidePressSeq += 1;
      cancelEditingEnd();
      outsidePressRef.current = false;
    };
    // A press that ends without a click (a text-selection drag, a scroll drag)
    // leaves the hold armed. The click of a real press is dispatched
    // synchronously after pointerup, so this release runs only when none came.
    const onPointerUp = () => {
      if (!outsidePressRef.current) return;
      releaseTimer = setTimeout(() => {
        releaseTimer = null;
        outsidePressSeq += 1;
        outsidePressRef.current = false;
      }, 0);
    };
    doc.addEventListener("pointerdown", onPointerDown, true);
    doc.addEventListener("click", onClick, true);
    doc.addEventListener("pointerup", onPointerUp, true);
    doc.addEventListener("pointercancel", onPointerCancel, true);
    return () => {
      doc.removeEventListener("pointerdown", onPointerDown, true);
      doc.removeEventListener("click", onClick, true);
      doc.removeEventListener("pointerup", onPointerUp, true);
      doc.removeEventListener("pointercancel", onPointerCancel, true);
      cancelRelease();
      cancelEditingEnd();
      outsidePressRef.current = false;
    };
  }, [reconcileContentEngagement]);

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

  // The composer's own single command line (2026-08-14: "the composer is
  // where you act on this session"): the session-scoped BUILT-IN registry
  // (shell/palette/commands.ts's sessionBuiltinCommands, unavailableReason-
  // resolved against THIS ref) merged with the owning session's loaded inventory
  // (slashCompletion.ts's mergeSlashCommands) - one list, one menu, whether a
  // row's provenance is a built-in, plugin, project or user command. The model
  // is keyed by ref, so loading a different target never borrows old/global rows.
  const visibleSlashCatalog = model?.diagnostics?.commands ?? [];
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
      write: (nextText, cursor, editSource, splice, restoredMentions) => {
        // Background cleanup changes this editor without claiming a shared
        // draft that another composer has edited in the meantime. Ownership is
        // the persisted revision identity, not a tab-local counter: two tabs
        // share one draft, and the counter cannot see the other tab's write.
        const mayPersist = editSource === undefined || editSource === "edit" || source.ownsDraftRevision();
        writeDraft(nextText, mayPersist, editSource, splice, restoredMentions);
        scheduleCursorRestore(cursor);
      },
    }),
    [textRef, source, writeDraft, scheduleCursorRestore],
  );
  const {
    setToken: setSlashToken,
    items: slashItems,
    open: slashOpen,
    activeKey: slashActiveKey,
    activeId: slashActiveId,
    listboxId: slashListboxId,
    commit: commitSlashCompletion,
    handleKeyDown: handleSlashKeyDown,
    reset: resetSlashCompletion,
  } = useSlashCompletion({
    catalog: slashMenuCatalog,
    editorRef,
    textRef,
    textEditor,
  });
  const askPending = useAskDockPending(ref);
  const fileTarget = useMemo<FileMentionTarget>(() => ({ kind: "session", ref }), [ref]);
  const getProtectedFileMentionRanges = useCallback(() => {
    const snap = source.getSnapshot();
    return [...snap.stagedImageRanges, ...snap.adoptedImageRanges];
  }, [source]);
  const {
    token: fileToken,
    setToken: setFileToken,
    items: fileItems,
    open: fileOpen,
    showLoader: fileShowLoader,
    truncated: fileTruncated,
    activeIndex: fileActiveIndex,
    activeId: fileActiveId,
    listboxId: fileListboxId,
    commit: commitFileCompletion,
    handleKeyDown: handleFileKeyDown,
    reset: resetFileCompletion,
  } = useFileCompletion({
    client: connectionClient,
    target: fileTarget,
    enabled: connectionReady && connectionClient !== null && !askPending,
    editorRef,
    textRef,
    draft: text,
    plainKeysOnly: true,
    getProtectedRanges: getProtectedFileMentionRanges,
  });
  useEffect(() => {
    if (text === "") resetFileCompletion();
  }, [text, resetFileCompletion]);
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

  const replaceComposerWithGoalDraft = (objective: string): void => {
    // Invalidate queued recovery work before clearing ownership. Any operation
    // already beyond its initial owner check is prevented by this version from
    // clearing the ordinary draft written below when it eventually settles.
    recoveryWriteVersionRef.current += 1;
    recoveryReplacementEpochRef.current += 1;
    recoveryOwnsLocalDraftRef.current = false;
    setActiveRecoveryId(null);
    attachments.reset();
    // A new draft ends the replaced one's adoption: its anchors are not in this
    // text, so keeping them would reserve numbers no image can ever take. The
    // record and its reservations retire together, because the editor's own
    // history is what would otherwise bring an occurrence back.
    clearAdoptedImageMarkers();
    updateSkillNames([]);
    updateSelectionMeta({ text: "", skillNames: [] });
    resetSlashCompletion();
    resetFileCompletion();
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
    // Desktop CurrentWork opens its own conversation's Tasks, including when
    // a cascade and an ordinary pane both show the same session.
    activitySidebarStore.getState().openFor(ref, "tasks", undefined, paneId);
  };

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
  // for the queued-row edit's restore path, now parameterized on the
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

  // The transcript's pending rows (the live-edge trailing region, a sibling
  // of this component) hand a queued row's text - or a refused-recovery
  // record's editing - back through the queueRowInsert seam: the same
  // one-shot monotonic-id shape the quote-insert effect above uses, because
  // the pending stack cannot reach this component's restore/recovery lease
  // any other way, and a restore must never clobber a draft the user is
  // typing (restoreTextToComposer's merge rule).
  const queueRowRestoreRequest = useQueueRowRestoreRequest(ref, paneId);
  const consumedQueueRowRestoreIdRef = useRef<number | null>(null);
  // The per-instance owner token for the seam's publications: it lets a
  // closing pane clear its own state without touching a sibling composer's
  // publication for the same ref.
  const composerSeationToken = useId();
  // The pending rows exclude the recovery record this composer is actively
  // editing (its composition is live here), so the same message never renders
  // both as a transcript row and as the composition under edit. Published on
  // every activation change, including the automatic ones no seam request
  // drives.
  useEffect(() => {
    publishActiveRecovery(ref, activeRecoveryId, composerSeationToken, paneId);
  }, [ref, activeRecoveryId, composerSeationToken, paneId]);
  // Unmount clears both publications: a pane closed mid-activation must not
  // keep excluding its recovery row or holding the busy gate for the ref.
  // The owner token keeps a closing pane from clearing a sibling composer's
  // publication for the same ref (an embedded cascade column).
  useEffect(() => {
    return () => {
      publishActiveRecovery(ref, null, composerSeationToken, paneId);
      publishComposerBusy(ref, false, composerSeationToken);
    };
  }, [ref, composerSeationToken, paneId]);
  // The shared busy gate, published beside it: the pending rows' Edit/Retry
  // affordances read the composer's live in-flight state (the retired
  // strip's busy prop).
  useEffect(() => {
    publishComposerBusy(ref, actionPending, composerSeationToken);
  }, [ref, actionPending, composerSeationToken]);
  useEffect(() => {
    const request = queueRowRestoreRequest;
    if (!request || request.id === consumedQueueRowRestoreIdRef.current) return;
    consumedQueueRowRestoreIdRef.current = request.id;
    if (request.recoveryRecord) {
      void Promise.resolve(actions.queueRowActions.onEditRecovery?.(request.recoveryRecord))
        .then(() => {
          // The retired strip's callback focused the editor after its
          // activation; an edit that restores content without focus leaves
          // typing ineffective until the user clicks the composer. The
          // outbox-record branch below carries the same contract.
          editorRef.current?.focus();
        })
        .finally(() => consumeQueueRowRestore(ref));
    } else if (request.outboxRecord) {
      // A durable outbox row's Edit: the retired strip's own sequence, run
      // where its gates live - the encoding gate and the recovery-operation
      // lease belong to the composer's own state, so the claim (the row's
      // move into recovery storage) only happens after both pass. The
      // cursor-restore wrapper around activation is the Composer's own half
      // of that contract (its restored caret lands where the edit put it).
      const record = request.outboxRecord;
      const id = record.clientMutationId;
      const releaseRecoveryOperation = source.tryAcquireRecoveryOperation();
      if (!releaseRecoveryOperation) {
        // A refused control has to say so: the lease is held by an
        // in-flight recovery operation, so the press landed mid-edit of
        // another row. The row-level busy gate usually prevents this
        // press; the toast keeps the window honest instead of swallowing
        // the request silently.
        toasts.push("error", "Wait for the current message to finish sending");
        consumeQueueRowRestore(ref);
        return;
      }
      const refusal = attachments.refusalReason();
      if (refusal) {
        toasts.push("error", refusal);
        releaseRecoveryOperation();
        consumeQueueRowRestore(ref);
        return;
      }
      void (async () => {
        try {
          const recovery = await editQueuedPendingTurn(id, record.targetRef);
          if (recovery) {
            await actions.queueRowActions.onEditRecovery?.(recovery);
            // The retired strip's callback focused the editor after its
            // activation; an edit that restores content without focus
            // leaves typing ineffective until the user clicks the composer.
            editorRef.current?.focus();
          } else {
            // An undefined claim means the row changed under the press -
            // the same refusal the retired handler reported, not a silent
            // consumption.
            pushRecoveryRefusal("changed");
          }
          if (record.targetRef !== ref) await refreshPendingTurnsProjection(ref);
        } catch (error) {
          toasts.push("error", sessionActionError("Edit failed", error));
        } finally {
          releaseRecoveryOperation();
          consumeQueueRowRestore(ref);
        }
      })();
    } else {
      actions.queueRowActions.onRestoreToComposer(
        request.text,
        undefined,
        request.skillNames,
        request.commandNames,
        request.mentions,
      );
      editorRef.current?.focus();
      consumeQueueRowRestore(ref);
    }
  }, [queueRowRestoreRequest, ref, actions.queueRowActions, source, toasts, attachments.refusalReason]);

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
  // Mentions count as content: they name a selected skill or command, and the
  // fold and Send must agree with recovery on what the composer holds.
  const hasContent = useMemo(
    () => !compositionIsEmpty({ text, attachments: attachments.items, skillNames, commandNames, mentions }),
    [text, attachments.items, skillNames, commandNames, mentions],
  );
  hasContentRef.current = hasContent;
  useEffect(() => {
    if (hasContent) {
      setContentEngaged(true);
      return;
    }
    reconcileContentEngagement();
  }, [hasContent, reconcileContentEngagement]);
  // Presence mirrors the row's capability gate. A folded empty row hides inline
  // Steer, so Session actions keeps queue draining available at every width.
  const resting = !contentEngaged && !hasContent && !mutationWriteStalled;
  const folded = !askPending && resting;
  const queueDepth = model?.queue?.depth ?? 0;
  const menuSteer = (narrow && hasContent) || ((narrow || folded) && queueDepth > 0);
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
    if (available.steer && menuSteer) {
      items.steer = {
        onSelect: () => steerClickRef.current(),
        // A refused draft cannot be steered either. The press names its reason
        // through the notice beside the draft because menu items have no tooltip.
        disabled: actionPending || hasUnbackedImage,
      };
    }
    return items;
  }, [menuSteer, model, actionPending, hasUnbackedImage]);
  const inlineStop = !!model && controlsFor(model).stop && !hasContent;
  const inlineStopReady = useInlineStopReady(ref, !model || askPending ? null : inlineStop ? "stop" : "send");
  const alignRailAction = appleComposer && !isMobile && focused && !askPending ? alignCreateWithComposer : undefined;
  const attachSubmitButton = useCallback(
    (button: HTMLButtonElement | null) => {
      submitButtonRef.current = button;
      if (!button) return;
      const release = alignRailAction?.(button);
      return () => {
        submitButtonRef.current = null;
        release?.();
      };
    },
    [alignRailAction],
  );

  if (!model) return null; // Session.tsx only mounts this once its own model is hydrated; defensive only.

  // Captured as plain consts (not read as `model.xyz` again below): a
  // closure that references `model` directly cannot inherit the `if
  // (!model) return null` narrowing above through a nested function
  // declaration (a TypeScript limitation, not a real possible-undefined
  // case - Session.tsx never mounts this component before its own model is
  // hydrated), so every handler below reads these already-narrowed values
  // instead of `model.<field>` directly.
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
  // What this session may be asked to do now: one derivation for every control
  // surface (stores/liveControls.ts), with the rationale (status alone, never
  // activeTurnId; capability is the harness's) in @evener/appwire-client's
  // submitRouting module. The press handlers below re-derive it from the store
  // at the press (pressRefusal), not from this render.
  const controls = controlsFor(model);
  const canSendWhenEnded = controls.send;
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
  const submitTooltip = submitTooltipLabel({ canQueue: availability.canQueue, enterToSend });
  const canCompose = availability.canSend || availability.canQueue;
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
  // A resting composer folds to one line below; a draft or attachment keeps
  // the writing space it needs after blur.
  const followUpEngaged = fence.fencedLocal || fence.sendDriven || contentEngaged || hasContent;
  // Focus alone never changes the composer's size: session autofocus and an
  // explicit click keep the same resting row. Content restores the full card
  // and keeps it stable until focus leaves an empty editor.
  let minLines = 2;
  if (folded || (appleComposer && phoneShellLayout)) minLines = 1;
  else if (ended) minLines = followUpEngaged ? 3 : 1;
  else if (duoCoverLayout && !followUpEngaged) minLines = 1;

  // Geometry keys stay keyed to the resting state so the folded card's
  // measurements do not move. The actions slot never sheds, so Stop stays
  // reachable while a turn runs.
  const controlsCollapsed = phoneShellLayout && resting;

  function updateCompletionTokens(
    masked: string,
    caret: number,
    isPaste: boolean,
    protectedRanges?: readonly { start: number; end: number }[],
  ): void {
    const tokens = resolveCompletionTokens(masked, caret, isPaste, protectedRanges);
    setSlashToken(tokens.slashToken);
    setFileToken(tokens.fileToken);
  }

  function handleTextChange(value: SkillEditorValue, caret: number, meta?: { isPaste?: boolean }): void {
    editSkillNames(value.skillNames);
    updateSelectionMeta(value);
    editText(value.text);
    // The document's own stagedAnchor marks are the source of truth for which
    // occurrences are unbacked: a delete takes its mark with it, and the undo
    // that restores the text restores the mark. Read before persistDraft so the
    // record this keystroke writes already agrees with what Send will see.
    setAdoptedImageRanges(value.adoptedImageRanges ?? [], value.adoptedImageMarkers ?? []);
    // Staged occurrences are serialized from the document's stagedImage marks,
    // so typing, pasting, deleting, and undo keep their exact positions without
    // rebuilding the document - see SkillEditorValue.stagedImageRanges.
    setStagedImageRanges(value.stagedImageRanges ?? []);
    if (recoveryOwnerLeaseRef.current === null) persistDraft(value.text);
    // Every keystroke re-evaluates the trailing-token match fresh - a token
    // Escape just closed (useSlashCompletion's token) reopens on the
    // very next text change rather than staying closed indefinitely.
    updateCompletionTokens(maskSkillAtoms(value), caret, Boolean(meta?.isPaste), [
      ...(value.stagedImageRanges ?? []),
      ...(value.adoptedImageRanges ?? []),
    ]);
  }

  function handleSelectionChange(caret: number): void {
    const masked = maskSkillAtoms({
      text: textRef.current,
      skillNames: skillNamesRef.current,
      ...selectionMetaRef.current,
    });
    updateCompletionTokens(masked, caret, false, getProtectedFileMentionRanges());
  }

  // The repair the notice offers. It removes the exact occurrences that carry
  // the stagedAnchor mark - not "the first [image 1] in the text" - in ONE
  // editor transaction, so an identical literal beside them is left alone and
  // an atom BETWEEN two removals keeps its identity (a single text-splice
  // covering both would read as one replacement span and drop that chip).
  //
  // A mounted editor is the authority for which occurrences are marked. A
  // source with no bound editor (a detached continuation, a test driving the
  // source directly) still has the value's own exact ranges, and those name
  // the same occurrences one splice at a time - the path used before the mark
  // carried a position.
  function handleRemoveUnbackedMarkers(): void {
    if (!hasUnbackedImage) return;
    const editor = editorRef.current;
    if (editor) {
      // The marks name exactly the occurrences to delete; `unbackedImageMarkers`
      // is only the render's "is any occurrence of this number unbacked" view.
      editor.removeStagedAnchors();
      return;
    }
    // No editor: derive the exact occurrences from the value the source holds.
    // Only the ranges the text still spells are removed, and each splice is
    // applied to the text as it stands after the previous one.
    let { text, cursor } = textEditor.read();
    let changed = false;
    for (const range of validMarkerRanges(text, adoptedImageRanges).reverse()) {
      text = `${text.slice(0, range.start)}${text.slice(range.end)}`;
      if (cursor > range.start) cursor = Math.max(range.start, cursor - (range.end - range.start));
      changed = true;
    }
    if (!changed) {
      // A source value that carries no ranges at all (a legacy record) falls
      // back to the number list, exactly as before.
      for (const marker of unbackedImageMarkers) {
        const stripped = stripMarker(text, cursor, marker);
        if (stripped.value === text) continue;
        text = stripped.value;
        cursor = stripped.cursor ?? cursor;
        changed = true;
      }
    }
    if (!changed) return;
    textEditor.write(text, cursor);
    markRestore();
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

  function handleFormSubmit(event: FormEvent): void {
    event.preventDefault();
    if (actionPending) return;
    if (!hasContent) return; // empty composer: no-op, no request, no message
    const refusal = attachments.refusalReason();
    if (refusal) {
      toasts.push("error", refusal);
      return;
    }
    // A marker whose bytes this composer does not hold is not sendable: the
    // request would carry the anchor as literal text and no image item, telling
    // the model about an image it cannot see. Refuse, keep the draft, and let
    // the notice beside it name the repair.
    if (hasUnbackedImage) return;
    if (submitBuiltin()) return;
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
    void actions.submitFromLiveState();
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
    const refusal = attachments.refusalReason();
    if (refusal) {
      toasts.push("error", refusal);
      return;
    }
    // Steer and drain carry the same composition Send does, so the same
    // unbacked anchor would reach the model by a second route. The guard stays
    // at the press for the chords, which reach here with no control to
    // disable - and it names its reason, because a press that returns in
    // silence is what made the enabled-and-inert control a bug.
    if (hasUnbackedImage) {
      toasts.push("error", STEER_UNBACKED_IMAGE_REASON);
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

  // Legacy suppresses every one of these keybindings entirely inside a
  // framed side-pane iframe (isInPane()) - verified moot, not assumed: this
  // rewrite's multi-pane layout is dockview panels in the SAME document
  // (shell/DockHost.tsx), never cross-document <iframe> panes at all (grep
  // for "iframe" across src turns up nothing) - the whole concept this
  // legacy gate defended against doesn't exist here, so there is no
  // isInPane()-equivalent check to port.
  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>): void {
    // The slash menu's keys come first: with it open, a committing Enter must not reach the Enter-to-send routing below.
    if (handleSlashKeyDown(event) || handleFileKeyDown(event)) return;
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

  function handleFollowUpFocus(event: ReactFocusEvent<HTMLDivElement>): void {
    if ((event.target as Element).closest('[role="textbox"]')) {
      formAnchorRef.current?.setAttribute("data-composer-editing", "");
    }
  }

  function handleFollowUpBlur(): void {
    requestAnimationFrame(() => {
      if (!mountedRef.current || composerPointerRef.current) return;
      reconcileContentEngagement();
    });
    endComposerEditingSoon();
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
    formAnchorRef.current?.removeAttribute("data-touch-editing");
    setSlashToken(null);
    setFileToken(null);
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

  // The leading slot's chrome carries its compact rung (one horizontal line
  // shared with attach and the activity strip); the pending-question mount
  // below keeps an automatic basis, because the composer's column is vertical
  // there and a width-oriented rung would inflate the chrome's height.
  const leadingChrome = (
    <MemoizedSessionChrome ref={ref} paneId={paneId} placement="composer" turnVerbs={turnVerbs} compactRung />
  );
  const askChrome = <MemoizedSessionChrome ref={ref} paneId={paneId} placement="composer" turnVerbs={turnVerbs} />;
  const menuChrome = (
    <div className={CLASS.menuSlot}>
      <MemoizedSessionChrome ref={ref} paneId={paneId} placement="menu" turnVerbs={turnVerbs} />
    </div>
  );
  const attachControl = (
    <div className={CLASS.attachSlot}>
      <AttachImageControl inputRef={fileInputRef} testId="composer-attach" tooltip="Attach an image" />
    </div>
  );

  // data-testid on every control in this row: two different buttons here
  // start with "Steer" (this one and the pending rows' "Steer now"), so
  // tests address controls by a stable hook instead of navigating by
  // accessible name - the naming style follows StatusRow's own
  // status-row-* testids.
  //
  // The phone shell's leading slot is Attach and the session menu in every
  // state; the menu placement keeps activity discovery following the pane,
  // and the status chrome never mounts.
  const leadingControls = (
    <div
      className={CLASS.leading}
      data-folded={folded ? "true" : undefined}
      data-compact={controlsCollapsed ? "true" : undefined}
    >
      {phoneShellLayout ? (
        <>
          {attachControl}
          {!askPending && menuChrome}
        </>
      ) : (
        <>
          {attachControl}
          {!askPending && <div className={CLASS.chromeSlot}>{leadingChrome}</div>}
        </>
      )}
    </div>
  );
  return (
    <div
      className={CLASS.composer}
      ref={attachComposerRoot}
      onFocusCapture={handleFollowUpFocus}
      onBlurCapture={handleFollowUpBlur}
      onPointerDown={holdComposerPress}
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
      {/* The queue strip no longer renders here: every undelivered user
          message renders as a transcript row in the live-edge trailing
          region (PendingRowStack, ConversationBody's trailingRow), so the
          composer's footer carries no message renderings. The queue count
          pill stays a count only (StatusRow's queue depth figure). */}
      {askPending && (phoneShellLayout ? menuChrome : askChrome)}
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
        <CurrentWork
          task={model.tasks?.current?.description}
          goal={model.goal?.objective}
          goalStatus={model.goal?.status}
          phoneShellLayout={phoneShellLayout}
          onOpenTasks={showTasks}
          onEditGoal={() => editGoal((model.goal?.objective ?? "").trim())}
        />
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
        <div
          ref={formAnchorRef}
          className={CLASS.formAnchor}
          data-composer={ref}
          data-composer-pane={paneId}
          data-iphone-composer={appleComposer ? (focused ? "focused" : "") : undefined}
        >
          {/* Anchored above the control row inside the card below, opening
              upward - see slashcompletionmenu.module.css's header comment
              for the anchored-float recipe. Mounted only
              while a token has real catalog matches (slashOpen), never for
              an empty/no-match filter. */}
          {slashOpen && (
            <SlashCompletionMenu
              id={slashListboxId}
              items={slashItems}
              activeKey={slashActiveKey}
              onSelect={commitSlashCompletion}
            />
          )}
          {fileOpen && (
            <FileCompletionMenu
              id={fileListboxId}
              query={fileToken?.query ?? ""}
              items={fileItems}
              highlightedIndex={fileActiveIndex}
              showLoader={fileShowLoader}
              truncated={fileTruncated}
              onSelect={commitFileCompletion}
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
            {/* An adopted anchor with no image behind it. Kept in the flow with
                the draft rather than toasted: Send refuses, the draft survives,
                and the reason stays readable for as long as the anchor does.
                live is "off" because this is a standing property of the draft,
                not news arriving after it rendered.
                Gated on the SAME askPending as the editor below: while a
                question is pending that card is hidden and inert, so a repair
                here would change a draft the person cannot see or reach. One
                condition, not a second parallel check that could drift. */}
            {!askPending && hasUnbackedImage && (
              <InlineNotice
                tone="danger"
                live="off"
                data-testid="composer-unbacked-image"
                action={
                  <Button size="sm" type="button" onClick={handleRemoveUnbackedMarkers}>
                    Remove the reference
                  </Button>
                }
              >
                {`The image ${unbackedImageMarkers
                  .map((marker) => markerText(marker))
                  .join(
                    ", ",
                  )} referred to here is not attached in this composer. Attach the replacement beside it first, then use Remove the reference once the preview appears, before sending.`}
              </InlineNotice>
            )}
            {!askPending && shareFailureIsCurrent && (
              <InlineNotice
                tone="danger"
                live="alert"
                data-testid="composer-share-write-error"
                action={
                  <Button size="sm" type="button" onClick={retryShareWrite}>
                    Retry
                  </Button>
                }
              >
                The share is saved on your iPhone. Retry to add it to this draft.
              </InlineNotice>
            )}
            <Dropzone onFiles={(files) => attachments.ingestFiles(files, (message) => toasts.push("error", message))}>
              <PromptCard
                data-testid="composer-input-card"
                controlsTestId="composer-controls"
                hidden={askPending}
                folded={folded}
                foldedCompact={controlsCollapsed}
                field={
                  <div className={CLASS.editorSlot} data-folded={folded ? "true" : undefined}>
                    <SkillEditor
                      ref={editorRef}
                      value={{
                        text,
                        skillNames,
                        ...selectionMeta,
                        adoptedImageMarkers,
                        adoptedImageRanges,
                        stagedImageRanges,
                      }}
                      restoreEpoch={restoreEpoch}
                      skillDetails={skillChipDetails}
                      commandDetails={(name) =>
                        visibleSlashCatalog.find((item) => slashCommandInvocation(item) === `/${name}`)?.description ??
                        `${name} — no longer in this session's command catalog`
                      }
                      onChange={handleTextChange}
                      onSelectionChange={handleSelectionChange}
                      onKeyDown={handleKeyDown}
                      onPaste={handlePaste}
                      aria-controls={slashActiveId ? slashListboxId : fileOpen ? fileListboxId : undefined}
                      aria-activedescendant={slashActiveId ?? fileActiveId ?? undefined}
                      minLines={minLines}
                      onBlur={handleEditorBlur}
                      placeholder={ended ? "Send a follow-up…" : "Message the agent…"}
                      aria-label="Message"
                    />
                  </div>
                }
                leading={leadingControls}
                actions={
                  <>
                    {/* Steer enters to the LEFT of Send/Stop when expanded, keeping the
                        primary action's right edge fixed while leaving no blank gap beside
                        Stop on an empty folded row. */}
                    {showSteer && !narrow && !folded && (
                      <Tooltip
                        label={steerTooltipLabel({
                          recoveryFenced: steerRecoveryFenced,
                          unbackedImage: hasUnbackedImage,
                          enterToSend,
                        })}
                      >
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
                          aria-disabled={steerRecoveryFenced || hasUnbackedImage || undefined}
                          disabled={actionPending}
                        >
                          Steer
                        </Button>
                      </Tooltip>
                    )}
                    {/* Distinct keys replace the actual button, so a press
                        begun on Send cannot finish on Stop. */}
                    {inlineStop ? (
                      <Tooltip key="stop" label="Stop the current turn">
                        <Button
                          variant="dangerQuiet"
                          size="xs"
                          type="button"
                          data-testid="composer-stop"
                          data-compact-action=""
                          ref={alignRailAction}
                          aria-label="Stop"
                          icon={<StopIcon />}
                          onClick={() => void handleInterruptClick()}
                          // Keep pending gates after the brief swap guard ends.
                          disabled={actionPending || !inlineStopReady}
                        >
                          <span className={CLASS.stopLabelStack}>
                            <span>Stop</span>
                            {/* Keep Stop's slot as wide as Send's existing label
                                without widening Send or moving the menu. */}
                            <span className={CLASS.stopMeasure} aria-hidden="true">
                              Send
                            </span>
                          </span>
                        </Button>
                      </Tooltip>
                    ) : (
                      <Tooltip key="send" label={submitTooltip}>
                        <Button
                          ref={attachSubmitButton}
                          type="submit"
                          variant={showSteer ? "quiet" : "primary"}
                          size="xs"
                          data-testid="composer-submit"
                          data-compact-action=""
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
                            // An adopted anchor with no bytes behind it is not
                            // sendable content, and the press is refused
                            // anyway - so do not offer a control that can only
                            // fail. The notice above the field says why and
                            // carries the repair.
                            hasUnbackedImage ||
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
                  </>
                }
              />
            </Dropzone>
            <ImagePickerInput
              inputRef={fileInputRef}
              onFiles={(files) => attachments.ingestFiles(files, (message) => toasts.push("error", message))}
            />
          </form>
        </div>
      )}
    </div>
  );
}
