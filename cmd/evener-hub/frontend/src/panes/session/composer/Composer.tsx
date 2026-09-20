// Composer: the session pane's input surface, mounted by Session.tsx below
// the transcript (T1 carves this slot; Session.tsx is FROZEN for the wave
// once T1 lands — every stream below edits only inside this subtree).
//
// The card itself is widgets/promptcard, shared verbatim with the spawn form:
// "message this agent" and "start an agent" are the same object, so they are
// one component with two sets of callers' buttons in it, not two lookalikes.
//
// The control row is state-responsive, never disabled-in-place: with a turn in
// flight it reads Stop · Send · Steer (Steer primary - interrupt and redirect
// now; Send quiet - queue until the agent stops), idle it is Send alone, and a
// finished session collapses the whole card to a one-line follow-up
// invitation. Stop is pinned leftmost so it never trades places with the verbs
// that come and go. Keyboard chords live in each control's Tooltip rather than
// as boxed <kbd> runs inside the buttons.
//
// T2 (this file): the skill editor, send-vs-steer-vs-queue-vs-drain routing via
// protocol/sendQueueAvailability's deriveSendQueueAvailability +
// submitRouting.ts's own steer/drain fork, Enter-to-send preference,
// per-ref drafts, attachments (paste/drag/picker), interrupt affordance.
// T3/T4 render their own subtrees inside the two marked slots below without
// ever touching the surrounding structure - see each slot's own comment.

import {
  type BuiltinMatch,
  decideSteerRoute,
  decideSubmitRoute,
  deriveSendQueueAvailability,
  filterSlashMenuItems,
  matchBuiltinInvocation,
  mergeSlashCommands,
  NO_ACTIVE_TURN,
  parseSlashToken,
  type SlashMenuItem,
  type SlashToken,
  sessionActionError,
  sessionPluginNames,
  spliceSlashCommand,
  type ThreadModel,
} from "@evener/appwire-client";
import {
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type { PaletteRunContext, ScopedCommand } from "../../../shell/palette/commands";
import { sessionBuiltinCommands, visibleCatalogCommands } from "../../../shell/palette/commands";
import { useIsMobile } from "../../../shell/useIsMobile";
import { useMountAutofocus } from "../../../shell/useMountAutofocus";
import { workspaceStore } from "../../../shell/workspace";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import {
  controlsFor,
  isLocalRecoveryFenced,
  liveThreadModel,
  pressLocalRecoveryFenced,
  pressRefusal,
} from "../../../stores/liveControls";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import { prefsStore, usePrefsStore } from "../../../stores/prefs";
import { type InputAttachment, threadsStore, useThreadsStore } from "../../../stores/threads";
import {
  Button,
  ConfirmDialog,
  chordLabel,
  Dropzone,
  IconButton,
  PromptCard,
  SendIcon,
  Tooltip,
  useToasts,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { SessionChrome } from "../chrome/SessionChrome";
import { TasksPanel, type TasksPanelHandle } from "../chrome/TasksPanel";
import { AttachmentTile } from "./AttachmentTile";
import { useAskDockPending } from "./askDockPending";
import { AttachIcon } from "./attachments/AttachIcon";
import { imageFilesFromClipboard } from "./attachments/clipboard";
import { type PendingAttachment, type TextEditor, useAttachments } from "./attachments/useAttachments";
import { runBuiltinCommand } from "./builtinCommand";
import { CurrentWork } from "./CurrentWork";
import styles from "./composer.module.css";
import { consumeComposerFocus, requestComposerFocus, useComposerFocusRequest } from "./composerFocus";
import {
  clearDraft,
  clearPersistedDraft,
  markDraftEdited,
  readComposerDraft,
  readDraft,
  readDraftRevision,
  writeComposerDraft,
} from "./draft";
import {
  type PendingTurnEntry,
  pendingTurnEntries,
  QueueStrip,
  submitWithPendingTracking,
  usePendingTurnEntries,
} from "./queue";
import {
  discardRecoveryPendingTurn,
  refreshPendingTurnsProjection,
  resendRecoveryPendingTurn,
  subscribeComposerSubmissionCommitted,
  updateRecoveryPendingTurn,
  useComposerSubmitting,
  useRecoveryEntries,
} from "./queue/pendingTurnsStore";
import { consumeQuoteInsert, type QuoteInsertPlacement, useQuoteInsertRequest } from "./quoteInsert";
import { RepoLocation } from "./RepoLocation";
import { mergeRecoveryComposerDraft, recoveryComposerDraft } from "./recovery/recoveryDraft";
import { SkillEditor, type SkillEditorHandle } from "./SkillEditor";
import { SlashCompletionMenu, optionId as slashOptionId } from "./SlashCompletionMenu";
import {
  maskSkillAtoms,
  materializeSkillReferences,
  parseSkillDocument,
  type SkillEditorValue,
  serializeSkillDocument,
} from "./skillDocument";
import { recordStoplessComposer } from "./stoplessComposer";

export interface ComposerProps {
  ref: string;
  // Whether this composer's pane is the workspace's focused one. Mount
  // autofocus keys on it: a session loading into a background tab must never
  // yank keyboard focus away from what the reader is doing.
  focused: boolean;
}

const CLASS = {
  composer: requireClass(styles.composer, "composer.module.css", "composer"),
  storageStatus: requireClass(styles.storageStatus, "composer.module.css", "storageStatus"),
  attachments: requireClass(styles.attachments, "composer.module.css", "attachments"),
  leading: requireClass(styles.leading, "composer.module.css", "leading"),
  visuallyHidden: requireClass(styles.visuallyHidden, "composer.module.css", "visuallyHidden"),
  formAnchor: requireClass(styles.formAnchor, "composer.module.css", "formAnchor"),
  submitLabel: requireClass(styles.submitLabel, "composer.module.css", "submitLabel"),
};

// Shared by restoreTextToComposer (QueueStrip's "edit a queued entry" path)
// and the quote-insert effect below (SelectionQuote's "Quote in reply" path,
// and the command palette's slash-command insert, via requestQuoteInsert's
// own placement param - quoteInsert.ts's own header comment). placement
// "append" (the default, and every existing caller's behavior, byte-
// identical to before this param existed): existing text is right-trimmed
// then kept, the incoming text is appended after a blank line - "put text
// into the composer without clobbering what's already typed there", byte-
// ported from renderer.js's own restoreTextToComposer (see
// restoreTextToComposer's own doc comment for the fuller history).
// placement "prefix" (the palette's own slash-command insert): the addition
// goes FIRST, with no separator inserted - a slash command only parses at
// the very start of the draft, and the addition already carries its own
// trailing space (CommandPalette.tsx's activateCommand), so simple
// concatenation is exactly right. A module-level function, not a closure,
// so it can be called from the quote-insert effect below, which (like every
// hook in this component) must run unconditionally ahead of the `if
// (!model) return null` narrowing - restoreTextToComposer itself is
// declared after that point and closes over already-narrowed locals it
// doesn't need here.
function mergeDraftText(existing: string, addition: string, placement: QuoteInsertPlacement = "append"): string {
  if (placement === "prefix") return `${addition}${existing}`;
  return existing.trim() === "" ? addition : `${existing.replace(/\s+$/, "")}\n\n${addition}`;
}

// Selections compare by exact ordered content: same names, same order, no
// extra. A changed chip list is a changed draft even when the text is
// byte-identical, so both halves of a submitted snapshot must still match
// before a delayed commit may clear what the user is holding.
function sameSkillSelections(left: readonly string[], right: readonly string[]): boolean {
  return left.length === right.length && left.every((name, index) => name === right[index]);
}

// Stored selections can outlive their visible references. Restore only names
// represented by complete chips in the document; never reconstruct missing text.
function restoredSkillNames(value: SkillEditorValue): string[] {
  return serializeSkillDocument(parseSkillDocument(value)).skillNames;
}

function settledInputAttachments(items: PendingAttachment[]): InputAttachment[] {
  return items.flatMap((item) =>
    item.data === undefined
      ? []
      : [{ marker: item.marker, name: item.name, mediaType: item.mediaType, data: item.data }],
  );
}

// "drain" is set/cleared only by QueueStrip's own onDrainBusyChange (its
// "Steer queue now" button) - never by this component's own submitAction, which
// uses "steer" for its classic drain-as-steer route too (see submitAction's
// own setBusyAction call). Both surfaces still share this ONE piece of
// state: whichever one goes busy first disables the other's controls too,
// closing the race where both could otherwise fire drainAsSteer at once
// (w5-integration-wiring-report.md's "two Steer buttons" concern).
type BusyAction = "submit" | "steer" | "interrupt" | "drain" | null;

// The wire statuses that mean this session's story is over. "notLoaded" is the
// shape a cold exited evener session actually arrives in (cmd/evener-hub/
// app_threadread.go's pastEntryThread stamps it) and "closed" is a live session
// that shut down in front of us; both are appwire's own vocabulary
// (appwire/types.go's ThreadStatus* constants). "ended" is not one of them -
// it never crosses the wire - but deriveSendQueueAvailability already treats it
// as terminal, so it is matched here too rather than leaving the two modules
// disagreeing about the same word.
const ENDED_STATUSES: ReadonlySet<string> = new Set(["ended", "closed", "notLoaded"]);

// Why a Steer press is refused while the local recovery fence stands: the
// explicit Resume action is the only thing that clears it, so the refusal
// names that path instead of a generic unavailability.
const STEER_RECOVERY_FENCED_REASON = "Steer isn't available until this session is resumed";

// The local recovery fence lives in stores/liveControls.ts (one predicate for
// every surface that owes it - this module's availability/card/Steer gates and
// QueueStrip's press gates): QueueStrip cannot import from this module
// (Composer imports QueueStrip), and a per-file copy of a fence this
// load-bearing would drift. A LOCAL session carrying a restart-blocking
// obligation (a Stop, or a snapshot the daemon reports as
// restartRequired/resumeRequired) cannot be acted on at all until the explicit
// Resume action clears the fence.

export function Composer({ ref, focused }: ComposerProps) {
  const model = useThreadsStore((s) => s.threads.get(ref));
  const recoveryRequired = useThreadsStore((s) => s.restartBlockingObligations.has(ref));
  const mutationWriteStalled = useThreadsStore((s) => s.mutationWriteStalled);
  const submitting = useComposerSubmitting(ref);
  const pendingSendEntries = usePendingTurnEntries(ref, "send");
  const toasts = useToasts();
  const isMobile = useIsMobile();
  const editorRef = useRef<SkillEditorHandle>(null);
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
  // Set by getComposerText() below, the moment QueueStrip's own drain
  // affordance actually reads this composer's text/attachments; consumed by
  // handleDrainSuccess to decide whether a strip-triggered drain should
  // still clear the composer (only if unchanged since THAT read, mirroring
  // clearIfUnchanged's own submittedText snapshot for the classic drain
  // path below).
  const lastDrainSnapshotRef = useRef<{
    text: string;
    attachments: PendingAttachment[];
    skillNames: string[];
    revision: number;
    draftRevision: number;
  } | null>(null);
  // Tracks edits within this mount, including recovery drafts whose persistence
  // writes can finish without an edit. Commit notifications only update the
  // display; they must still let this mount remove its submitted attachments.
  const draftEditRevisionRef = useRef(0);
  const ownedDraftRevisionRef = useRef(readDraftRevision(ref));

  // Restore-on-mount is unconditional, not leak-guarded: under dockview a
  // session pane's `ref` never changes across a mounted Composer's
  // lifetime (shell/paneRegistry.ts marks "session" non-singleton, so a
  // different ref is always a DIFFERENT pane/mount, never this same
  // instance re-parented - see draft.ts's own header comment for the full
  // trail). A fresh mount's React state starts empty by construction, so
  // there is no "stale text from a different ref" a lazy initializer could
  // ever observe here, unlike the legacy DOM-morph world drafts.ts's own
  // isOtherSessionsDraft guarded against.
  const [text, setText] = useState(() => readDraft(ref));
  // The canonical skill selections staged for this request (chips). Same
  // sticky-draft contract as `text`: restored per-ref on mount, persisted in
  // the one structured v2 draft record, and snapshotted before every submit.
  const [skillNames, setSkillNames] = useState<string[]>(() => restoredSkillNames(readComposerDraft(ref)));

  // Bumped whenever `text`/`skillNames` are replaced by a value that came from
  // somewhere other than this composer's own editor - a stored draft, a
  // recovery, a queued entry. SkillEditor rebuilds its document from such a
  // value, so the selections it names become chips again; a programmatic edit
  // (an attachment marker, a goal command) is not marked, and what it inserts
  // stays prose. See SkillEditor's restoreEpoch contract.
  const [restoreEpoch, setRestoreEpoch] = useState(0);
  const markRestore = useCallback((): void => setRestoreEpoch((epoch) => epoch + 1), []);

  const [activeRecoveryId, setActiveRecoveryIdState] = useState<string | null>(null);
  const [freshRecoveryRef, setFreshRecoveryRef] = useState<string | null>(null);
  const activeRecoveryIdRef = useRef<string | null>(null);
  const recoveryWrites = useRef<Promise<void>>(Promise.resolve());
  const recoveryWriteVersionRef = useRef(0);
  // Unlike write versions, this changes only when canonical replacement exits
  // recovery ownership. Durable delete predicates capture it so a discard
  // already awaiting storage cannot remove a row after replacement.
  const recoveryReplacementEpochRef = useRef(0);
  const recoveryOwnsLocalDraftRef = useRef(false);
  const [busyAction, setBusyAction] = useState<BusyAction>(null);
  const actionPending = busyAction !== null || submitting || mutationWriteStalled;
  const mountedRef = useRef(false);
  const [pendingGoalReplacement, setPendingGoalReplacement] = useState<string | null>(null);
  // Whether a FINISHED session's collapsed follow-up field currently has focus,
  // which is what expands it from its one-line resting state. Only read on that
  // path (see the ended card's minLines below); harmless everywhere else.
  const [followUpFocused, setFollowUpFocused] = useState(false);

  // Inline slash-command completion (slashCompletion.ts's own header
  // comment - ported from Beautiful UI's prompt-bar). slashToken is the
  // trailing-token match recomputed on every keystroke (handleTextChange
  // below); null means no menu, regardless of what the draft's text
  // actually contains - Escape closes the menu by setting this to null
  // directly, and typing further reopens it because the very next keystroke
  // recomputes the match fresh. slashHighlighted is the ArrowUp/Down cursor
  // over whatever the CURRENT filtered list is; reset to 0 whenever the
  // token itself changes (new match, or the query narrowed/widened) rather
  // than persisted across it - an index into a list that just changed shape
  // is not a meaningful position to keep.
  const slashCatalog = useCommandCatalog((s) => s.commands);
  const [slashToken, setSlashToken] = useState<SlashToken | null>(null);
  const [slashHighlighted, setSlashHighlighted] = useState(0);
  // The composer's own single command line (2026-08-14: "the composer is
  // where you act on this session"): the session-scoped BUILT-IN registry
  // (shell/palette/commands.ts's sessionBuiltinCommands, unavailableReason-
  // resolved against THIS ref) merged with the plugin catalog
  // (slashCompletion.ts's mergeSlashCommands) - one list, one menu, whether a
  // row's provenance is a built-in or a plugin.
  const activePluginNames = useMemo(() => sessionPluginNames(model?.diagnostics), [model?.diagnostics]);
  const visibleSlashCatalog = useMemo(
    () => visibleCatalogCommands(slashCatalog, activePluginNames),
    [activePluginNames, slashCatalog],
  );
  const sessionBuiltins = sessionBuiltinCommands({ sessionRef: ref, onPage: "session" });
  const slashMenuCatalog = mergeSlashCommands(sessionBuiltins, visibleSlashCatalog, model?.skills ?? []);
  // The menu is only ever open when a token matched AND the merged catalog
  // has at least one fuzzy label hit for it - a matched-but-empty token (e.g.
  // "/zzz" against a real catalog) shows no menu at all, same as no token
  // matching.
  const slashItems = slashToken ? filterSlashMenuItems(slashMenuCatalog, slashToken.query) : [];
  const slashOpen = slashToken !== null && slashItems.length > 0;
  // Scoped by `ref`: dockview can have several session panes - and so
  // several mounted Composers - open at once, and a bare literal id would
  // collide across them.
  const slashListboxId = `composer-slash-listbox-${ref}`;
  const slashActiveIndex = slashOpen ? Math.min(slashHighlighted, slashItems.length - 1) : -1;
  const slashActiveId = slashActiveIndex >= 0 ? slashOptionId(slashListboxId, slashActiveIndex) : null;

  // A freshly (re)matched token always starts highlighted at its first
  // option - an index carried over from the PREVIOUS token's list is not a
  // meaningful position once the list itself has changed shape.
  // biome-ignore lint/correctness/useExhaustiveDependencies: slashToken's start/query are deliberate trigger-only deps - the effect body only calls setSlashHighlighted(0), but must still re-run whenever the token identity actually changes (a new match, or the same match with a different query), same idiom as the cursor-restore layout effect below
  useEffect(() => {
    setSlashHighlighted(0);
  }, [slashToken?.start, slashToken?.query]);

  // textRef mirrors `text`, updated SYNCHRONOUSLY by updateText() below -
  // unlike `text` itself (a plain per-render const) or an editor read
  // before React commits a programmatic replacement,
  // textRef.current is correct the INSTANT any text-changing path runs,
  // regardless of which render's closure is asking or whether React has
  // had a chance to re-render yet. Both properties matter: useAttachments'
  // decode-failure callback (useAttachments.ts) can resume long after the
  // render that registered it - a closure over plain `text` would read
  // however stale that render's value was, correctly stripping the marker
  // from it but then overwriting whatever the user has typed SINCE with
  // that same stale text (a real, reproduced bug: paste an image whose
  // decode later fails, type before it settles, watch the typed text get
  // silently reverted - Composer.test.tsx's own regression test). And two
  // attachment gestures fired back-to-back with no intervening render
  // (also tested) would see the SAME staleness from a DOM read, since
  // React hasn't committed the first gesture's `setText` yet by the time
  // the second one asks.
  const textRef = useRef(text);
  const skillNamesRef = useRef(skillNames);

  const setActiveRecoveryId = useCallback((clientMutationId: string | null): void => {
    activeRecoveryIdRef.current = clientMutationId;
    setActiveRecoveryIdState(clientMutationId);
  }, []);

  const updateText = useCallback((nextText: string): void => {
    textRef.current = nextText;
    setText(nextText);
  }, []);

  const editText = useCallback(
    (nextText: string): void => {
      draftEditRevisionRef.current += 1;
      if (activeRecoveryIdRef.current !== null) {
        markDraftEdited(ref);
        ownedDraftRevisionRef.current = readDraftRevision(ref);
      }
      updateText(nextText);
    },
    [ref, updateText],
  );

  // The selection-list twin of updateText/editText: a chip change is a draft
  // edit (draftEditRevisionRef, recovery ownership) even though the prose is
  // untouched, which is what keeps a delayed commit from clearing a draft
  // whose chips changed while it was in flight.
  const updateSkillNames = useCallback((nextSkillNames: string[]): void => {
    skillNamesRef.current = nextSkillNames;
    setSkillNames(nextSkillNames);
  }, []);

  const editSkillNames = useCallback(
    (nextSkillNames: string[]): void => {
      draftEditRevisionRef.current += 1;
      if (activeRecoveryIdRef.current !== null) {
        markDraftEdited(ref);
        ownedDraftRevisionRef.current = readDraftRevision(ref);
      }
      updateSkillNames(nextSkillNames);
    },
    [ref, updateSkillNames],
  );

  const persistDraft = useCallback(
    (nextText: string): void => {
      writeComposerDraft(ref, { text: nextText, skillNames: skillNamesRef.current });
      ownedDraftRevisionRef.current = readDraftRevision(ref);
    },
    [ref],
  );

  useLayoutEffect(() => {
    mountedRef.current = true;
    // Re-read at subscription time so a commit between render and mount
    // cannot leave an already-cleared sticky draft in a fresh composer.
    const draft = readComposerDraft(ref);
    markRestore();
    updateText(draft.text);
    updateSkillNames(restoredSkillNames(draft));
    ownedDraftRevisionRef.current = readDraftRevision(ref);
    const unsubscribe = subscribeComposerSubmissionCommitted(
      (targetRef, submittedText, submittedSkillNames, recovery) => {
        if (targetRef !== ref) return;
        if (recovery && activeRecoveryIdRef.current === recovery.clientMutationId) {
          if (recovery.draftUnchanged) ownedDraftRevisionRef.current = readDraftRevision(ref);
          const ownsDraft = ownedDraftRevisionRef.current === readDraftRevision(ref);
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
          // Restoring the same recovery in another mount recreates its items.
          // Match the submitted payload under that recovery owner; newly staged
          // items have distinct markers, and replacing the draft exits ownership.
          const markers = new Set(
            attachmentItemsRef.current
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
          clearSubmittedAttachmentsRef.current(markers);
          if (ownsDraft) persistDraft(textRef.current);
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
    return () => {
      mountedRef.current = false;
      unsubscribe();
    };
  }, [ref, setActiveRecoveryId, updateText, updateSkillNames, persistDraft, markRestore]);

  // Attachment marker edits update controlled text, selected references and
  // the persisted draft together. Prefer a pending cursor restoration over
  // the live editor selection so two attachment gestures before React commits
  // insert consecutive markers rather than reusing the first position.
  const textEditor: TextEditor = {
    read: () => {
      const cursor = cursorToRestoreRef.current ?? editorRef.current?.getCursor() ?? textRef.current.length;
      const selection =
        cursorToRestoreRef.current === null && editorRef.current
          ? editorRef.current.getSelection()
          : { start: cursor, end: cursor };
      return { text: textRef.current, cursor, selection };
    },
    write: (nextText, cursor, source) => {
      // Submission cleanup retires this mount's markers without claiming a
      // shared draft that another composer has edited in the meantime.
      const mayPersist = source !== "submission" || ownedDraftRevisionRef.current === readDraftRevision(ref);
      const next = restoredSkillNames({ text: nextText, skillNames: skillNamesRef.current });
      updateSkillNames(next);
      if (source === "submission") updateText(nextText);
      else editText(nextText);
      if (mayPersist && activeRecoveryIdRef.current === null) persistDraft(nextText);
      scheduleCursorRestore(cursor);
    },
  };
  const attachments = useAttachments(textEditor);
  const attachmentItemsRef = useRef(attachments.items);
  attachmentItemsRef.current = attachments.items;
  const clearSubmittedAttachmentsRef = useRef(attachments.clearSubmitted);
  clearSubmittedAttachmentsRef.current = attachments.clearSubmitted;
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
    setSlashToken(null);
    setSlashHighlighted(0);
    lastDrainSnapshotRef.current = null;
    const command = `/goal ${objective}`;
    textEditor.write(command, command.length);
    setPendingGoalReplacement(null);
    requestComposerFocus(ref);
  };

  const editGoal = (objective: string): void => {
    if (
      textRef.current !== "" ||
      attachmentItemsRef.current.length > 0 ||
      activeRecoveryIdRef.current !== null ||
      skillNamesRef.current.length > 0
    ) {
      setPendingGoalReplacement(objective);
      return;
    }
    replaceComposerWithGoalDraft(objective);
  };

  const showTasks = (): void => {
    if (isMobile) tasksPanelRef.current?.open();
    else workspaceStore.getState().openPane("sessionTasks", { ref }, { slot: "secondary" });
  };

  const toggleTasks = (): void => {
    if (isMobile) tasksPanelRef.current?.open();
    else workspaceStore.getState().togglePane("sessionTasks", { ref });
  };

  // A shared projection can outlive a Composer remount while its durable
  // discard is still being projected. Only auto-activate after this mount
  // has observed a successful IndexedDB refresh for the current session.
  useEffect(() => {
    let mounted = true;
    setFreshRecoveryRef(null);
    void refreshPendingTurnsProjection(ref).then((refreshed) => {
      if (mounted && refreshed) setFreshRecoveryRef(ref);
    });
    return () => {
      mounted = false;
    };
  }, [ref]);

  const queueRecoveryPersistence = useCallback(
    (
      clientMutationId: string,
      nextText: string,
      nextAttachments: ReturnType<typeof attachments.toInputAttachments>,
      nextSkillNames: readonly string[],
    ): Promise<void> => {
      const version = ++recoveryWriteVersionRef.current;
      const replacementEpoch = recoveryReplacementEpochRef.current;
      const draftRevision = readDraftRevision(ref);
      const operation = recoveryWrites.current
        .catch(() => undefined)
        .then(async () => {
          if (activeRecoveryIdRef.current !== clientMutationId) return;
          if (nextText.trim() === "" && nextAttachments.length === 0 && nextSkillNames.length === 0) {
            if (
              textRef.current.trim() !== "" ||
              attachmentItemsRef.current.length > 0 ||
              skillNamesRef.current.length > 0
            )
              return;
            await discardRecoveryPendingTurn(
              clientMutationId,
              ref,
              () =>
                recoveryReplacementEpochRef.current === replacementEpoch &&
                activeRecoveryIdRef.current === clientMutationId &&
                textRef.current.trim() === "" &&
                attachmentItemsRef.current.length === 0 &&
                skillNamesRef.current.length === 0,
            );
            if (
              activeRecoveryIdRef.current === clientMutationId &&
              readDraftRevision(ref) === draftRevision &&
              textRef.current.trim() === "" &&
              attachmentItemsRef.current.length === 0 &&
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
      recoveryWrites.current = operation;
      void operation.catch((error) => {
        if (activeRecoveryIdRef.current !== clientMutationId) return;
        toasts.push(
          "error",
          `Couldn't save recovered message: ${error instanceof Error ? error.message : String(error)}`,
        );
      });
      return operation;
    },
    [ref, setActiveRecoveryId, toasts],
  );

  useEffect(() => {
    if (activeRecoveryId === null || attachments.hasPending) return;
    void queueRecoveryPersistence(activeRecoveryId, text, attachments.toInputAttachments(), skillNames);
  }, [
    activeRecoveryId,
    attachments.hasPending,
    attachments.toInputAttachments,
    queueRecoveryPersistence,
    text,
    skillNames,
  ]);

  useEffect(() => {
    if (
      freshRecoveryRef !== ref ||
      activeRecoveryId !== null ||
      textRef.current.trim() !== "" ||
      attachmentItemsRef.current.length > 0 ||
      skillNamesRef.current.length > 0
    ) {
      return;
    }
    // An interrupt is not a draft. It carries no input, so activating one
    // loads an empty composer and then resends the user's next keystrokes as a
    // turn/start -- a Stop becoming a message. QueueStrip renders it as a
    // failure with its reason instead.
    const record = recoveryEntries.find(
      (entry) => entry.recoveryKind === "rejected" && entry.method !== "turn/interrupt",
    );
    if (!record) return;
    const recovered = recoveryComposerDraft(record);
    recoveryOwnsLocalDraftRef.current = false;
    setActiveRecoveryId(record.clientMutationId);
    // Restoration replaces this mount's local owner without editing the
    // shared recovery draft that an earlier mount may still be submitting.
    draftEditRevisionRef.current += 1;
    markRestore();
    updateText(recovered.text);
    updateSkillNames(restoredSkillNames(recovered));
    attachments.replaceWithSettled(recovered.attachments);
    clearPersistedDraft(ref);
    scheduleCursorRestore(recovered.text.length);
  }, [
    activeRecoveryId,
    attachments.replaceWithSettled,
    freshRecoveryRef,
    markRestore,
    recoveryEntries,
    ref,
    scheduleCursorRestore,
    setActiveRecoveryId,
    updateText,
    updateSkillNames,
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
  const quoteInsertRequest = useQuoteInsertRequest(ref);
  const consumedQuoteInsertIdRef = useRef<number | null>(null);
  useEffect(() => {
    if (!quoteInsertRequest || quoteInsertRequest.id === consumedQuoteInsertIdRef.current) return;
    consumedQuoteInsertIdRef.current = quoteInsertRequest.id;
    const merged = mergeDraftText(textRef.current, quoteInsertRequest.text, quoteInsertRequest.placement);
    const cursor = quoteInsertRequest.placement === "prefix" ? quoteInsertRequest.text.length : merged.length;
    textEditor.write(merged, cursor);
    editorRef.current?.focus();
    consumeQuoteInsert(ref);
  }, [quoteInsertRequest, ref, textEditor.write]);

  // composerFocus.ts's own seam: a global chord (owned elsewhere) asks this
  // ref's Composer to move keyboard focus into its textarea. Exactly the
  // same shape as the quote-insert effect above - keyed on the request's own
  // monotonic id so consuming a request from a previous mount under the SAME
  // still-pending request never replays it, and consumeComposerFocus() below
  // makes it one-shot.
  const composerFocusRequest = useComposerFocusRequest(ref);
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
  // message is what the pane is for. Mount-only, gated on the pane being
  // focused at mount (see useMountAutofocus for the full rationale).
  useMountAutofocus(editorRef, focused);

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
  const ended = ENDED_STATUSES.has(model.status.type);
  // A stopped local session is recovery-fenced. It keeps its follow-up card so
  // the retained draft and the recovery notice's explicit Resume action stay
  // reachable, but Send and Queue are NOT offered: turn/start no longer carries
  // an implicit resume on this branch, and the wire already advertises
  // send=false for this snapshot. The explicit Resume action is what resumes it.
  const recoveryFencedLocal = isLocalRecoveryFenced(ref, recoveryRequired) && model.status.type === "notLoaded";
  const queueDepth = model.queue?.depth ?? 0;
  // What this session may be asked to do now: one derivation for every control
  // surface (stores/liveControls.ts), with the rationale (status alone, never
  // activeTurnId; capability is the harness's) in @evener/appwire-client's
  // submitRouting module. The press handlers below re-derive it from the store
  // at the press (pressRefusal), not from this render.
  const controls = controlsFor(model);
  const canSendWhenEnded = controls.send;
  // The target's skillInput capability, same narrowing rule: submission is
  // refused client-side (before any durable write) when a selection is staged
  // and the target never advertised that it consumes skill items.
  const skillInputSupported = model.capabilities.skillInput === true;
  // A turn/start THIS COMPOSER already submitted, before any status frame for
  // it has come back. Without it a fast second message is composed while the
  // thread still reads idle, routed to turn/start, and refused by the daemon
  // with Conflict("turn is already active") - see tier 6 in
  // deriveSendQueueAvailability.
  //
  // Someone else's pending send is excluded deliberately, and tier 6 does not
  // work without that. usePendingTurnEntries also surfaces
  // model.pendingMutations, which is the DAEMON's session-wide mutation
  // projection: it covers every client on the session, reducer.ts writes it only
  // at hydrate, and no notification ever refreshes it. Feeding it to a routing
  // decision would reroute this composer on another tab's or the TUI's in-flight
  // send, from a snapshot that may be arbitrarily old - exactly the "daemon's
  // state arriving late" that tier 6's own justification rests on not being.
  //
  // The question is whose send it is, which is what fromThisClient answers. It
  // is deliberately not entry.source: that names the projection describing the
  // row, and a hydrate landing mid-send re-describes THIS client's own
  // unsettled send as "authoritative" (pendingReconcile's own doc comment).
  // Reading routing off the presentation source therefore lost tier 6 for the
  // sender at exactly the moment the daemon confirmed it had the send - the
  // next message went to turn/start and bounced.
  //
  // blockedUnknown counts too: it is this client's own send whose response was
  // lost, so the turn may already be running. Dropping it dropped tier 6 for
  // exactly the uncertain window, and the next message bounced on the turn
  // that send had applied.
  //
  // A canceled row does not count: Stop wrote its cancellation before dispatch
  // (stop-cancellation-outbox §4), so it is provably not in flight and no turn
  // can be running because of it. Counting it parked the next message in queue
  // mode behind a turn that never started.
  const ownPendingSend = (entries: readonly PendingTurnEntry[]) =>
    entries.some((entry) => entry.fromThisClient && entry.state !== "canceled");
  const hasPendingSend = ownPendingSend(pendingSendEntries);
  // The Send/Queue availability of a model and this client's pending send: read
  // at render for the button and its tooltip, and again at submit from the
  // stores' live model and live pending entries, so a status frame or a
  // pending send that landed (or cleared) between the two routes the submit
  // rather than the render.
  //
  // The restart-blocking obligation arrives as a parameter: the render passes
  // the subscribed value (so the availability updates with the store instead of
  // reading it behind the subscription's back), the submit passes a live store
  // read like the rest of its re-derivation. It is the raw obligation, not the
  // render's recoveryFencedLocal: the fence here covers every status, active
  // included, not only the stopped one.
  function availabilityFor(
    target: ThreadModel,
    pendingSend: boolean,
    restartObligated: boolean,
  ): { canSend: boolean; canQueue: boolean } {
    // A recovery-fenced local session has no send/queue until the user resumes
    // it, in WHATEVER status the snapshot carries - active included. The
    // fence's own window is exactly one where an ACTIVE snapshot can carry
    // it: a live read during a Stop relays the daemon's still-active status
    // while the hub overlays resumeRequired beside it
    // (applyThreadResumeRequirement), and the store arms the obligation on
    // that very hydration. The hub's recovery admission then refuses
    // turn/start AND turn/queue for the whole window
    // (sessionActionRecoveryError keys on the resume locks, never the
    // projected status), so the availability table's queue-mode answer for
    // the still-running turn could only mint durable intent that parks
    // until the explicit Resume action clears the fence. The explicit
    // Resume action is the only thing that resumes it.
    if (isLocalRecoveryFenced(target.ref, restartObligated)) {
      return { canSend: false, canQueue: false };
    }
    const tableAvailability = deriveSendQueueAvailability({
      statusType: target.status.type,
      capabilities: target.capabilities,
      hasPendingSend: pendingSend,
    });
    // A finished session can still be sent to when the source says so: the hub
    // advertises Send for an exited evener thread and auto-resumes it on the first
    // message (turn/start alone carries that resume loop - app_rpc.go). The
    // CAPABILITY is the authority for THAT question, not the availability table,
    // which reports both-false for a finished session with nothing pending,
    // because no turn is in flight to send to or queue behind.
    //
    // It only substitutes when the table has nothing to offer, which is what
    // keeps it clear of tier 6. Overriding unconditionally turned every finished
    // status' SECOND message back into the turn/start that bounces - the table
    // answers queue-mode there, for the whole time the resume takes to produce a
    // status frame, which for a session that has to spawn a daemon is seconds.
    return ENDED_STATUSES.has(target.status.type) &&
      controlsFor(target).send &&
      !tableAvailability.canSend &&
      !tableAvailability.canQueue
      ? { canSend: true, canQueue: false }
      : tableAvailability;
  }
  const availability = availabilityFor(model, hasPendingSend, recoveryRequired);
  const hasText = text.trim() !== "";
  const hasAttachments = attachments.items.length > 0;
  const hasContent = hasText || hasAttachments || skillNames.length > 0;
  const showStop = controls.stop;
  const showSteer = controls.steer;
  // The Steer control's own reading of the recovery fence (see
  // availabilityFor's fence above): the hub refuses turn/steer for the
  // obligation's whole window, so the control must not offer a press that
  // could only park durable intent. Read from the subscribed obligation here
  // for the render; the press re-reads it live.
  const steerRecoveryFenced = isLocalRecoveryFenced(ref, recoveryRequired);
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
  // Send keeps ONE label in every state. While a turn runs it queues rather
  // than sending now, but that is a change of TIMING, not of verb - a label
  // that flips to "Queue" made the same button mean two different things
  // depending on when you looked, and Steer beside it is what now carries
  // "act on this turn immediately". The tooltip says which timing applies,
  // and the strip's queue depth is what shows the effect.
  const submitChord: string[] = enterToSend ? ["Enter"] : ["Mod", "Enter"];
  const submitTooltip = availability.canQueue
    ? `Queue until the agent stops · ${chordLabel(submitChord)}`
    : `Send now · ${chordLabel(submitChord)}`;
  const canCompose = availability.canSend || availability.canQueue;
  // Whether the follow-up card renders at all is the capability's call, for the
  // same reason the substitution above is: gating it on the table renders no
  // card for exactly the sessions the hub says are resumable. When the wire
  // really advertises no send, no card is rendered at all - an unusable field
  // is worse than no field.
  const showFollowUpCard = ended && (canSendWhenEnded || recoveryFencedLocal);
  // A finished session's card earns its control row once the user engages with
  // it - focused, or holding text or an attachment. Content matters as well as
  // focus: a restored draft, or a blur with text still in the field, must not
  // strand a typed message with no visible way to send it. The one session
  // engaged from the start is a recovery-fenced local one: its card keeps the
  // control row reachable while the fence stands, which is the whole point of
  // keeping the card at all. Every OTHER local notLoaded snapshot rests exactly
  // like a non-local one.
  const followUpEngaged = recoveryFencedLocal || followUpFocused || hasContent;
  // While the card rests, its control row - and with it the composer chrome
  // that opts into initial activity discovery - is not mounted. A saved
  // notLoaded session with send enabled is exactly that shape, so mount a
  // chrome-less discovery owner for the interval instead; once the card is
  // engaged the chrome above owns discovery. Session.tsx's own menu/discovery
  // mount is gated on !controlsFor(model).send (alongside its notLoaded /
  // local / no-owner / !restartPending conditions), so it does not double up
  // with this one; the #1335 intent is exactly one discovery owner at a time.
  const discoveryOnlyChrome = ended && !followUpEngaged && canSendWhenEnded;

  function handleTextChange(value: SkillEditorValue, caret: number): void {
    editSkillNames(value.skillNames);
    editText(value.text);
    if (activeRecoveryIdRef.current === null) persistDraft(value.text);
    // Every keystroke re-evaluates the trailing-token match fresh - a token
    // Escape just closed (slashToken's own doc comment above) reopens on the
    // very next text change rather than staying closed indefinitely.
    setSlashToken(parseSlashToken(maskSkillAtoms(value), caret));
  }

  // commitSlashCompletion is Tab/Enter's (handleKeyDown below) and a mouse
  // click's (SlashCompletionMenu's own onSelect) shared "the user chose
  // this command" path: splices the item's own invocation (slashCompletion.ts's
  // mergeSlashCommands - "/plugin:name" for a plugin command via
  // shell/palette/commands.ts's slashCommandInvocation, bare "/id" for a
  // built-in) in at the token's own start (never the caret, when the caret
  // was left mid-token by an earlier Escape-then-retype - spliceSlashCommand's
  // own doc comment), through the SAME textEditor.write() seam every other
  // programmatic edit in this file uses (draft persistence, cursor restore),
  // then closes the menu and returns focus to the field - mirrors
  // restoreTextToComposer's own "write, then focus" shape. This only ever
  // INSERTS the invocation text - whether it goes on to execute as a
  // built-in RPC or simply sends as a message is handleFormSubmit's own
  // interception, below.
  function commitSlashCompletion(item: SlashMenuItem): void {
    if (!slashToken) return;
    // The editor replaces this range with one atomic mention and records the
    // text and activation metadata together in its undo history.
    if (item.kind === "skill" && item.canonicalName !== undefined) {
      // The editor refuses the insertion while an IME composition is live, so
      // only dismiss the menu for a skill that actually landed: closing it over
      // a token left as prose would tell the user something was staged that is
      // not in the request at all.
      if (!editorRef.current?.insertSkill(slashToken.start, slashToken.end, item.canonicalName)) return;
      setSlashToken(null);
      editorRef.current?.focus();
      return;
    }
    const spliced = spliceSlashCommand(textRef.current, slashToken, item.invocation);
    textEditor.write(spliced.text, spliced.caret);
    setSlashToken(null);
    editorRef.current?.focus();
  }

  // Equal text can belong to a newer edit, including a reused image marker.
  // A commit notification may already have cleared the display without editing
  // the draft; its original attachment cleanup still belongs to this revision.
  function clearIfUnchanged(
    submittedText: string,
    submittedRevision: number,
    submittedDraftRevision: number,
    submittedSkillNames: readonly string[],
  ): boolean {
    if (!mountedRef.current || draftEditRevisionRef.current !== submittedRevision) return false;
    if (textRef.current === submittedText && sameSkillSelections(skillNamesRef.current, submittedSkillNames)) {
      updateText("");
      updateSkillNames([]);
      if (readDraftRevision(ref) === submittedDraftRevision) {
        clearDraft(ref);
        ownedDraftRevisionRef.current = readDraftRevision(ref);
      }
    }
    return true;
  }

  // QueueStrip captures this mount's payload and edit revision through
  // getComposerText before it starts the drain's durable write.
  function handleDrainSuccess(): void {
    const snapshot = lastDrainSnapshotRef.current;
    if (!snapshot || !mountedRef.current) return;
    clearIfUnchanged(snapshot.text, snapshot.revision, snapshot.draftRevision, snapshot.skillNames);
    clearSubmittedAttachments(snapshot.attachments);
  }

  function clearSubmittedAttachments(submitted: PendingAttachment[]): void {
    // Text edits do not replace an attachment. Object identity distinguishes
    // the submitted item from a replacement that reuses its marker number.
    const markers = new Set(
      attachmentItemsRef.current.filter((item) => submitted.includes(item)).map((item) => item.marker),
    );
    attachments.clearSubmitted(markers);
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
  // parity (contracts-composer-queue-pending.md:70, parity-m5-
  // composer.md:102) - dropped image attachments surface via QueueStrip's
  // own durable queue state. `skillNames` restores the entry's selections
  // as chips (union with the current draft's chips, deduped by name): a
  // queued skill selection must survive the edit round-trip, not silently
  // drop.
  function restoreTextToComposer(
    restoredText: string,
    _attachments?: InputAttachment[],
    restoredNames?: readonly string[],
  ): void {
    const merged = mergeDraftText(textRef.current, restoredText);
    const wanted = [...new Set([...skillNamesRef.current, ...(restoredNames ?? [])])];
    // An entry can carry a selection with no prose of its own. Its chip has to
    // be visible in the sentence either way, so spell the reference out rather
    // than let the restore drop what the user chose.
    const text = materializeSkillReferences(merged, wanted);
    if (restoredNames?.length) editSkillNames(wanted);
    // A queued entry's selections are named, not spelled out, so the value that
    // carries them is authoritative here exactly as a recovery activation's is:
    // without this the merge is a partial append, the references land as plain
    // text, and the request would carry activations the user cannot see.
    markRestore();
    textEditor.write(text, text.length);
    editorRef.current?.focus();
  }

  function activateRecovery(record: MutationRecoveryRecord): void {
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    const merged = mergeRecoveryComposerDraft(
      textRef.current,
      attachments.items,
      recoveryComposerDraft(record),
      skillNamesRef.current,
    );
    const currentRecoveryId = activeRecoveryIdRef.current;
    if (currentRecoveryId === null) {
      recoveryOwnsLocalDraftRef.current = true;
      setActiveRecoveryId(record.clientMutationId);
    }
    const nextSkillNames = restoredSkillNames(merged);
    markRestore();
    editText(merged.text);
    editSkillNames(nextSkillNames);
    attachments.replaceWithSettled(merged.attachments);
    scheduleCursorRestore(merged.text.length);
    editorRef.current?.focus();

    const ownerId = currentRecoveryId ?? record.clientMutationId;
    const replacementEpoch = recoveryReplacementEpochRef.current;
    const persistence = queueRecoveryPersistence(
      ownerId,
      merged.text,
      settledInputAttachments(merged.attachments),
      nextSkillNames,
    );
    if (currentRecoveryId !== null && currentRecoveryId !== record.clientMutationId) {
      void persistence
        .then(async () => {
          await discardRecoveryPendingTurn(
            record.clientMutationId,
            ref,
            () => recoveryReplacementEpochRef.current === replacementEpoch,
          );
        })
        .catch(() => undefined);
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
      revision: draftEditRevisionRef.current,
      draftRevision: readDraftRevision(ref),
    };
    return {
      text: textRef.current,
      attachments: attachments.toInputAttachments(),
      hasPending: attachments.hasPending,
      skillNames: [...skillNamesRef.current],
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
  async function submitAction(kind: "send" | "queue" | "steer" | "drain"): Promise<void> {
    const submittedText = textRef.current;
    const submittedAttachments = attachments.items;
    const submittedSkillNames = [...skillNamesRef.current];
    const submittedRevision = draftEditRevisionRef.current;
    const submittedDraftRevision = readDraftRevision(ref);
    const payload = attachments.toInputAttachments();
    const submittedRecoveryId = activeRecoveryIdRef.current;
    let wonRecoveryResend = true;
    // The UI-side half of the skillInput gate (threads.ts's composerMutationIntent
    // is the store-side half, and Task 12 gates the hub's forwarding too): a
    // target that never advertised the capability keeps the draft and hears
    // why, instead of minting durable intent the wire would refuse.
    if (submittedSkillNames.length > 0 && !skillInputSupported) {
      toasts.push("error", "Skill selections aren't supported on this session yet; your draft is kept");
      return;
    }
    setBusyAction(kind === "send" || kind === "queue" ? "submit" : "steer");
    try {
      await submitWithPendingTracking(
        {
          ref,
          text: submittedText,
          attachments: payload,
          skillNames: submittedSkillNames,
          recoveryId: submittedRecoveryId ?? undefined,
          onFailure: (err) => {
            const label = kind === "send" ? "Send" : kind === "queue" ? "Queue" : kind === "steer" ? "Steer" : "Drain";
            toasts.push("error", sessionActionError(`${label} failed`, err));
          },
        },
        async () => {
          if (submittedRecoveryId !== null) {
            await queueRecoveryPersistence(submittedRecoveryId, submittedText, payload, submittedSkillNames);
            wonRecoveryResend = await resendRecoveryPendingTurn(
              submittedRecoveryId,
              ref,
              kind,
              submittedText,
              payload,
              submittedSkillNames,
            );
            return;
          }
          if (kind === "send") return threadsStore.getState().send(ref, submittedText, payload, submittedSkillNames);
          if (kind === "queue") return threadsStore.getState().queue(ref, submittedText, payload, submittedSkillNames);
          if (kind === "steer") return threadsStore.getState().steer(ref, submittedText, payload, submittedSkillNames);
          return threadsStore.getState().drainAsSteer(ref, submittedText, payload, submittedSkillNames);
        },
      );
      if (!mountedRef.current) return;
      if (!wonRecoveryResend) toasts.push("info", "This message was already sent in another tab.");
      clearIfUnchanged(submittedText, submittedRevision, submittedDraftRevision, submittedSkillNames);
      clearSubmittedAttachments(submittedAttachments);
    } catch {
      // The local durable write failed. The submitted composer payload stays
      // untouched and no network request was eligible to start.
    } finally {
      if (mountedRef.current) setBusyAction(null);
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
    const submittedText = textRef.current;
    const submittedSkillNames = [...skillNamesRef.current];
    const submittedRevision = draftEditRevisionRef.current;
    const submittedDraftRevision = readDraftRevision(ref);
    setBusyAction("submit");
    const ctx: PaletteRunContext = {
      sessionRef: ref,
      onPage: "session",
      toasts,
      // Neither method is reachable here: both belong to app-global palette
      // commands (/search, /help), never to a session-scoped built-in - see
      // commandSurface's own doc comment on why the two never overlap.
      ui: { clearToSearch: () => {}, showHelp: () => {} },
    };
    const outcome = await runBuiltinCommand(match, ctx);
    setBusyAction(null);
    if (outcome.ok) clearIfUnchanged(submittedText, submittedRevision, submittedDraftRevision, submittedSkillNames);
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
    if (!hasAttachments && skillNames.length === 0) {
      const match = matchBuiltinInvocation(text, sessionBuiltins);
      if (match) {
        void handleBuiltinSubmit(match);
        return;
      }
    }
    // The same derivation the button and its tooltip rendered from
    // (availabilityFor, substitution included), over the store's live model:
    // the two disagree only when a status frame landed after the render, and
    // then the frame is what the submit has to follow.
    const route = decideSubmitRoute({
      hasContent,
      availability: availabilityFor(
        liveThreadModel(ref) ?? renderedModel,
        ownPendingSend(pendingTurnEntries(ref, "send")),
        threadsStore.getState().restartBlockingObligations.has(ref),
      ),
    });
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
      hasSkills: skillNames.length > 0,
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
    // Inline slash-completion's own keyboard mechanics, ported from
    // Beautiful UI's prompt-bar (slashCompletion.ts's own header comment):
    // ArrowUp/Down move the highlighted option (wrapping at both ends) OVER
    // the caret rather than moving the caret itself, Tab OR Enter commits
    // the highlighted option, Escape dismisses without touching the draft.
    // Every branch here returns before falling through to the rest of this
    // function - in particular, the committing Enter never reaches the
    // Enter-to-send routing below it, which is the whole point of checking
    // this FIRST: with the menu open, this function's own routing must not
    // fire at all for that keystroke.
    if (slashOpen) {
      if (event.key === "ArrowDown") {
        event.preventDefault();
        setSlashHighlighted((i) => (i + 1) % slashItems.length);
        return;
      }
      if (event.key === "ArrowUp") {
        event.preventDefault();
        setSlashHighlighted((i) => (i - 1 + slashItems.length) % slashItems.length);
        return;
      }
      if (event.key === "Tab" || (event.key === "Enter" && !event.nativeEvent.isComposing)) {
        event.preventDefault();
        event.stopPropagation();
        const chosen = slashItems[slashActiveIndex] ?? slashItems[0];
        if (chosen) commitSlashCompletion(chosen);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        setSlashToken(null);
        return;
      }
    }
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

  // Blur is the slash menu's own "clicked/tabbed away entirely" close, on
  // top of whatever the ended-session follow-up card already does with a
  // blur (collapsing back to one line). SlashCompletionMenu's own options
  // preventDefault() on their mousedown specifically so a MOUSE click on an
  // option never reaches this handler in the first place - see that
  // component's own comment - so this only ever fires for a genuine
  // "focus left the field" (Tab away, click elsewhere, blur()).
  function handleEditorBlur(): void {
    if (ended) setFollowUpFocused(false);
    setSlashToken(null);
  }

  return (
    <div className={CLASS.composer}>
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
      <div className={CLASS.visuallyHidden} role="status" aria-live="polite">
        {readyAnnouncement}
      </div>
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
        onEditRecovery={activateRecovery}
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
        // value carries this pane's session ref so the overlay can anchor to
        // the FOCUSED pane's composer when several are mounted.
        <div className={CLASS.formAnchor} data-composer={ref}>
          {/* Anchored above the control row inside the card below, opening
              upward the same way GoalControl's own popover does - see
              slashcompletionmenu.module.css's header comment. Mounted only
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
          <form ref={formRef} onSubmit={handleFormSubmit}>
            <Dropzone onFiles={(files) => attachments.ingestFiles(files, (message) => toasts.push("error", message))}>
              <PromptCard
                data-testid="composer-input-card"
                hidden={askPending}
                verbs={1 + (showStop ? 1 : 0) + (showSteer ? 1 : 0)}
                field={
                  <SkillEditor
                    ref={editorRef}
                    value={{ text, skillNames }}
                    restoreEpoch={restoreEpoch}
                    skillDetails={skillChipDetails}
                    onChange={handleTextChange}
                    onKeyDown={handleKeyDown}
                    onPaste={handlePaste}
                    aria-controls={slashActiveId ? slashListboxId : undefined}
                    aria-activedescendant={slashActiveId ?? undefined}
                    minLines={ended ? (followUpEngaged ? 3 : 1) : undefined}
                    onFocus={ended ? () => setFollowUpFocused(true) : undefined}
                    onBlur={handleEditorBlur}
                    placeholder={ended ? "Send a follow-up…" : "Message the agent…"}
                    aria-label="Message"
                  />
                }
                // An ended session's card is a bare invitation UNTIL it is
                // engaged: at rest it is one line with no control row, because
                // chrome around an empty invitation is noise. Once it has focus
                // or content it grows a real control row, because a field you
                // can type into and cannot visibly send is a dead end - the
                // ⌘/Ctrl+Enter chord alone is not an affordance anyone can see.
                leading={
                  ended && !followUpEngaged ? undefined : (
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
                      <SessionChrome ref={ref} placement="composer" onOpenTasks={toggleTasks} discoverActivity />
                    </div>
                  )
                }
                actions={
                  ended && !followUpEngaged ? undefined : (
                    <>
                      {/* Stop leads the cluster, always in the same place: it is
                        the one control here whose misfire cannot be undone, so
                        it must never trade positions with Send or Steer as
                        those come and go. The word, not a glyph - "Stop" is
                        chrome, and chrome speaks. */}
                      {showStop && (
                        <Tooltip label="Stop the current turn">
                          <Button
                            variant="dangerQuiet"
                            size="xs"
                            type="button"
                            data-testid="composer-stop"
                            onClick={() => void handleInterruptClick()}
                            // busy + the interrupt capability are already what
                            // makes this render at all, so only an in-flight
                            // request of our own is left to gate on.
                            disabled={actionPending}
                          >
                            Stop
                          </Button>
                        </Tooltip>
                      )}
                      {/* Send is quiet while a turn runs and primary when
                        nothing does: with a turn in flight the immediate
                        action is Steer, and Send's job is the patient one. */}
                      <Tooltip label={submitTooltip}>
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
                          // availabilityFor refuses every fenced status, so a
                          // fenced session whose snapshot still
                          // advertises send:true (the hub stamps it on closed
                          // frames too) renders a disabled Send, not a refusal
                          // toast.
                          disabled={
                            actionPending ||
                            !hasContent ||
                            !(ended ? canSendWhenEnded && !isLocalRecoveryFenced(ref, recoveryRequired) : canCompose)
                          }
                        >
                          <span className={CLASS.submitLabel}>Send</span>
                        </Button>
                      </Tooltip>
                      {showSteer && (
                        <Tooltip
                          label={
                            steerRecoveryFenced
                              ? STEER_RECOVERY_FENCED_REASON
                              : enterToSend
                                ? "Interrupt and redirect now"
                                : `Interrupt and redirect now · ${chordLabel(["Shift", "Enter"])}`
                          }
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
                            // describing an action the fence refuses.
                            disabled={actionPending || steerRecoveryFenced}
                          >
                            Steer
                          </Button>
                        </Tooltip>
                      )}
                    </>
                  )
                }
              />
            </Dropzone>
            <input ref={fileInputRef} type="file" accept="image/*" multiple hidden onChange={handleFilePickerChange} />
          </form>
        </div>
      )}
      {/* The card's own chrome is the discovery opt-in, so while the card rests
          something has to own initial discovery for it - otherwise the
          transcript's entity ids stay plain text until the card is engaged.
          Renders nothing visible (the panel's only control is hidden and its
          sheet is closed). */}
      {discoveryOnlyChrome && <SessionChrome ref={ref} discoveryOnly />}
      {/* The session's working dir and git branch, in one quiet line under the
          card. Reference material, not a control: it stays put across every
          composer state (including an ended session's collapsed card and the
          ask-pending input swap), so "where is this agent working" never
          disappears with the input row. Only a local session's cwd is looked up
          for a branch: a source-backed session's cwd is another host's path. */}
      <RepoLocation cwd={model.cwd} local={ref.startsWith("local:")} />
    </div>
  );
}
