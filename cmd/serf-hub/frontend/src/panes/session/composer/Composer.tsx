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
// T2 (this file): the Textarea, send-vs-steer-vs-queue-vs-drain routing via
// protocol/sendQueueAvailability's deriveSendQueueAvailability +
// submitRouting.ts's own steer/drain fork, Enter-to-send preference,
// per-ref drafts, attachments (paste/drag/picker), interrupt affordance.
// T3/T4 render their own subtrees inside the two marked slots below without
// ever touching the surrounding structure - see each slot's own comment.
import {
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { sessionActionError } from "../../../protocol/errors";
import { deriveSendQueueAvailability } from "../../../protocol/sendQueueAvailability";
import type { PaletteRunContext } from "../../../shell/palette/commands";
import { sessionBuiltinCommands } from "../../../shell/palette/commands";
import { useCommandCatalog } from "../../../stores/commandCatalog";
import type { MutationRecoveryRecord } from "../../../stores/mutationOutbox";
import { prefsStore, usePrefsStore } from "../../../stores/prefs";
import { type InputAttachment, threadsStore, useThreadsStore } from "../../../stores/threads";
import {
  Button,
  chordLabel,
  Dropzone,
  IconButton,
  PromptCard,
  SendIcon,
  Textarea,
  Tooltip,
  useToasts,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { SessionChrome } from "../chrome/SessionChrome";
import { AttachmentTile } from "./AttachmentTile";
import { AskDock, useAskDockPending } from "./askDock";
import { AttachIcon } from "./attachments/AttachIcon";
import { imageFilesFromClipboard } from "./attachments/clipboard";
import { type PendingAttachment, type TextEditor, useAttachments } from "./attachments/useAttachments";
import { type BuiltinMatch, matchBuiltinInvocation, runBuiltinCommand } from "./builtinCommand";
import styles from "./composer.module.css";
import { consumeComposerFocus, useComposerFocusRequest } from "./composerFocus";
import { clearDraft, readDraft, writeDraft } from "./draft";
import { QueueStrip, submitWithPendingTracking, usePendingTurnEntries } from "./queue";
import {
  discardRecoveryPendingTurn,
  refreshPendingTurnsProjection,
  resendRecoveryPendingTurn,
  updateRecoveryPendingTurn,
  useRecoveryEntries,
} from "./queue/pendingTurnsStore";
import { consumeQuoteInsert, type QuoteInsertPlacement, useQuoteInsertRequest } from "./quoteInsert";
import { mergeRecoveryComposerDraft, recoveryComposerDraft } from "./recovery/recoveryDraft";
import { SlashCompletionMenu, optionId as slashOptionId } from "./SlashCompletionMenu";
import {
  filterSlashMenuItems,
  mergeSlashCommands,
  parseSlashToken,
  type SlashMenuItem,
  type SlashToken,
  spliceSlashCommand,
} from "./slashCompletion";
import { recordStoplessComposer } from "./stoplessComposer";
import { decideSteerRoute, decideSubmitRoute, isTurnActive } from "./submitRouting";

export interface ComposerProps {
  ref: string;
}

const CLASS = {
  composer: requireClass(styles.composer, "composer.module.css", "composer"),
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
// shape a cold exited serf session actually arrives in (cmd/serf-hub/
// app_threadread.go's pastEntryThread stamps it) and "closed" is a live session
// that shut down in front of us; both are appwire's own vocabulary
// (appwire/types.go's ThreadStatus* constants). "ended" is not one of them -
// it never crosses the wire - but deriveSendQueueAvailability already treats it
// as terminal, so it is matched here too rather than leaving the two modules
// disagreeing about the same word.
const ENDED_STATUSES: ReadonlySet<string> = new Set(["ended", "closed", "notLoaded"]);

export function Composer({ ref }: ComposerProps) {
  const model = useThreadsStore((s) => s.threads.get(ref));
  const pendingSendEntries = usePendingTurnEntries(ref, "send");
  const toasts = useToasts();
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const formRef = useRef<HTMLFormElement>(null);
  // Set by textEditor.write() below; consumed (and cleared) by the
  // cursor-restore layout effect once `text`'s new value has committed.
  const cursorToRestoreRef = useRef<number | null>(null);
  // Set by getComposerText() below, the moment QueueStrip's own drain
  // affordance actually reads this composer's text/attachments; consumed by
  // handleDrainSuccess to decide whether a strip-triggered drain should
  // still clear the composer (only if unchanged since THAT read, mirroring
  // clearIfUnchanged's own submittedText snapshot for the classic drain
  // path below).
  const lastDrainSnapshotRef = useRef<{ text: string; markers: Set<number> } | null>(null);

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
  const [activeRecoveryId, setActiveRecoveryIdState] = useState<string | null>(null);
  const [freshRecoveryRef, setFreshRecoveryRef] = useState<string | null>(null);
  const activeRecoveryIdRef = useRef<string | null>(null);
  const recoveryWrites = useRef<Promise<void>>(Promise.resolve());
  const recoveryWriteVersionRef = useRef(0);
  const recoveryOwnsLocalDraftRef = useRef(false);
  const [busyAction, setBusyAction] = useState<BusyAction>(null);
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
  const sessionBuiltins = sessionBuiltinCommands({ sessionRef: ref, onPage: "session" });
  const slashMenuCatalog = mergeSlashCommands(sessionBuiltins, slashCatalog);
  // The menu is only ever open when a token matched AND the merged catalog
  // has at least one startsWith hit for it - a matched-but-empty token (e.g.
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

  // Textarea (widgets/textarea) takes no aria-activedescendant/aria-controls
  // prop - it's a shared widget outside this stream's manifest - so this
  // component sets both directly on the native node it already refs for
  // cursor restoration below, the same imperative-DOM idiom the cursor-
  // restore layout effect already uses on the identical ref.
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    if (slashActiveId) {
      el.setAttribute("aria-controls", slashListboxId);
      el.setAttribute("aria-activedescendant", slashActiveId);
    } else {
      el.removeAttribute("aria-controls");
      el.removeAttribute("aria-activedescendant");
    }
  }, [slashActiveId, slashListboxId]);

  // A freshly (re)matched token always starts highlighted at its first
  // option - an index carried over from the PREVIOUS token's list is not a
  // meaningful position once the list itself has changed shape.
  // biome-ignore lint/correctness/useExhaustiveDependencies: slashToken's start/query are deliberate trigger-only deps - the effect body only calls setSlashHighlighted(0), but must still re-run whenever the token identity actually changes (a new match, or the same match with a different query), same idiom as the cursor-restore layout effect below
  useEffect(() => {
    setSlashHighlighted(0);
  }, [slashToken?.start, slashToken?.query]);

  // textRef mirrors `text`, updated SYNCHRONOUSLY by updateText() below -
  // unlike `text` itself (a plain per-render const) or the textarea DOM
  // node's own `.value` (only updated once React actually commits),
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

  const setActiveRecoveryId = useCallback((clientMutationId: string | null): void => {
    activeRecoveryIdRef.current = clientMutationId;
    setActiveRecoveryIdState(clientMutationId);
  }, []);

  const updateText = useCallback((nextText: string): void => {
    textRef.current = nextText;
    setText(nextText);
  }, []);

  // Bridges useAttachments' pure string-splice logic to this component's
  // own controlled `text` state, instead of a direct DOM `.value` mutation
  // - see useAttachments.ts's TextEditor doc comment for the React
  // controlled-input restoration bug that direct mutation ran into. Also
  // keeps the draft in sync with attachment-driven edits (marker insert on
  // ingest, marker strip on remove/decode-failure), not just typing -
  // otherwise a decode failure's stripped marker would leave a stale,
  // now-invalid "[image N]" fragment sitting in the stored draft even
  // though the visible textarea correctly no longer shows it.
  //
  // read()'s cursor prefers cursorToRestoreRef.current (this component's
  // OWN pending, not-yet-committed cursor intent) over the DOM's live
  // selectionStart - reusing that ref rather than adding a parallel one,
  // since it already means exactly "the last write() call's intended
  // cursor, whenever the layout effect hasn't applied it to the DOM yet".
  // Needed for the identical reason textRef is: a second ingestFiles call
  // landing before any render (e.g. two attachment gestures fired back to
  // back - Composer.test.tsx's own regression test) would otherwise read
  // the DOM's selectionStart, which the browser hasn't moved yet because
  // the layout effect that moves it hasn't run - inserting the second
  // marker at the FIRST marker's stale pre-insertion position instead of
  // chaining after it. Once the layout effect actually applies a cursor
  // and clears this ref (back to null), read() correctly falls back to the
  // live DOM value - which is what must be trusted for genuine user-driven
  // cursor movement (clicking, arrow keys) that this component has no
  // other hook into.
  const textEditor: TextEditor = {
    read: () => ({
      text: textRef.current,
      cursor: cursorToRestoreRef.current ?? textareaRef.current?.selectionStart ?? textRef.current.length,
    }),
    write: (nextText, cursor) => {
      updateText(nextText);
      if (activeRecoveryIdRef.current === null) writeDraft(ref, nextText);
      cursorToRestoreRef.current = cursor;
    },
  };
  const attachments = useAttachments(textEditor);
  const attachmentItemsRef = useRef(attachments.items);
  attachmentItemsRef.current = attachments.items;
  const recoveryEntries = useRecoveryEntries(ref);

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
    ): Promise<void> => {
      const version = ++recoveryWriteVersionRef.current;
      const operation = recoveryWrites.current
        .catch(() => undefined)
        .then(async () => {
          if (activeRecoveryIdRef.current !== clientMutationId) return;
          if (nextText.trim() === "" && nextAttachments.length === 0) {
            if (textRef.current.trim() !== "" || attachmentItemsRef.current.length > 0) return;
            await discardRecoveryPendingTurn(clientMutationId, ref);
            if (
              activeRecoveryIdRef.current === clientMutationId &&
              textRef.current.trim() === "" &&
              attachmentItemsRef.current.length === 0
            ) {
              recoveryOwnsLocalDraftRef.current = false;
              setActiveRecoveryId(null);
              clearDraft(ref);
            }
            return;
          }
          const updated = await updateRecoveryPendingTurn(clientMutationId, ref, nextText, nextAttachments);
          if (
            updated &&
            recoveryOwnsLocalDraftRef.current &&
            recoveryWriteVersionRef.current === version &&
            activeRecoveryIdRef.current === clientMutationId
          ) {
            recoveryOwnsLocalDraftRef.current = false;
            clearDraft(ref);
          }
        });
      recoveryWrites.current = operation;
      void operation.catch((error) => {
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
    void queueRecoveryPersistence(activeRecoveryId, text, attachments.toInputAttachments());
  }, [activeRecoveryId, attachments.hasPending, attachments.toInputAttachments, queueRecoveryPersistence, text]);

  useEffect(() => {
    if (
      freshRecoveryRef !== ref ||
      activeRecoveryId !== null ||
      textRef.current.trim() !== "" ||
      attachmentItemsRef.current.length > 0
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
    updateText(recovered.text);
    attachments.replaceWithSettled(recovered.attachments);
    clearDraft(ref);
    cursorToRestoreRef.current = recovered.text.length;
  }, [
    activeRecoveryId,
    attachments.replaceWithSettled,
    freshRecoveryRef,
    recoveryEntries,
    ref,
    setActiveRecoveryId,
    updateText,
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

  // Runs after `text`'s new value has committed to the DOM (via React's own
  // controlled-value reconciliation) - only then is it safe to move the
  // native cursor without React clobbering it. Keyed on `text` so it fires
  // once per actual text change, not on every unrelated re-render (e.g. a
  // live model update); a no-op whenever textEditor.write() wasn't the
  // cause of this particular change (ordinary typing has no ref to
  // restore).
  // biome-ignore lint/correctness/useExhaustiveDependencies: text is a deliberate trigger-only dep - the effect body only reads cursorToRestoreRef, but must still re-run on every text change to pick up a write() that just landed, same idiom as widgets/textarea's own autoGrow effect
  useLayoutEffect(() => {
    const cursor = cursorToRestoreRef.current;
    if (cursor === null) return;
    cursorToRestoreRef.current = null;
    const el = textareaRef.current;
    if (el) {
      el.selectionStart = cursor;
      el.selectionEnd = cursor;
    }
  }, [text]);

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
    textareaRef.current?.focus();
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
    consumedComposerFocusIdRef.current = composerFocusRequest.id;
    textareaRef.current?.focus();
    consumeComposerFocus(ref);
  }, [composerFocusRequest, ref]);

  if (!model) return null; // Session.tsx only mounts this once its own model is hydrated; defensive only.

  // Captured as plain consts (not read as `model.xyz` again below): a
  // closure that references `model` directly cannot inherit the `if
  // (!model) return null` narrowing above through a nested function
  // declaration (a TypeScript limitation, not a real possible-undefined
  // case - Session.tsx never mounts this component before its own model is
  // hydrated), so every handler below reads these already-narrowed values
  // instead of `model.<field>` directly.
  const activeTurnId = model.activeTurnId;
  const ended = ENDED_STATUSES.has(model.status.type);
  // Read here rather than inside the handlers below, which close over `model`
  // outside the narrowing this component does at its top (see that block's own
  // comment on why every handler reads a pre-narrowed local).
  const canSendWhenEnded = model.capabilities.send;
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
  const hasPendingSend = pendingSendEntries.some((entry) => entry.fromThisClient);
  const tableAvailability = deriveSendQueueAvailability({
    statusType: model.status.type,
    capabilities: model.capabilities,
    hasPendingSend,
  });
  // A finished session can still be sent to when the source says so: the hub
  // advertises Send for an exited serf thread and auto-resumes it on the first
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
  const availability =
    ended && canSendWhenEnded && !tableAvailability.canSend && !tableAvailability.canQueue
      ? { canSend: true, canQueue: false }
      : tableAvailability;
  const busy = isTurnActive(model.status.type, activeTurnId);
  const queueDepth = model.queue?.depth ?? 0;
  const hasText = text.trim() !== "";
  const hasAttachments = attachments.items.length > 0;
  const hasContent = hasText || hasAttachments;

  // Stop is SESSION-scoped and Steer is not, so they do not share a gate.
  //
  // Stop asks the session to stop working. turn/interrupt names no turn
  // (appwire v3 dropped expectedTurnId from every control mutation) and the
  // daemon answers on the session's own quiescence, so the only question the
  // composer has to answer is "is this session working" -- which is the status
  // alone. It deliberately does NOT use `busy`: isTurnActive additionally
  // requires activeTurnId, and gating the BUTTON on an id the REQUEST does not
  // carry can only ever withhold a Stop the daemon would have accepted.
  //
  // Active-with-no-id is a state the wire really reaches -- a session holding
  // queued work reports active with no turn running, for one. (An earlier
  // version of this comment attributed it to a turn reservation the daemon
  // takes at turn/start; that reservation has no production callers and is not
  // the cause. The gate is wrong for the reason above, which does not depend on
  // how the state is reached.)
  //
  // Steer keeps `busy`. It redirects a turn in flight, and with none running
  // Send already covers "say something now" -- a presentation choice rather
  // than a precondition, since an idle session would accept a steer and land
  // it in the next turn.
  const showStop = model.status.type === "active" && model.capabilities.interrupt;
  const showSteer = busy && model.capabilities.steer;
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
  const showFollowUpCard = ended && canSendWhenEnded;
  // A finished session's card earns its control row once the user engages with
  // it - focused, or holding text or an attachment. Content matters as well as
  // focus: a restored draft, or a blur with text still in the field, must not
  // strand a typed message with no visible way to send it.
  const followUpEngaged = followUpFocused || hasContent;

  function handleTextChange(event: { target: { value: string; selectionStart?: number | null } }): void {
    updateText(event.target.value);
    if (activeRecoveryIdRef.current === null) writeDraft(ref, event.target.value);
    // Every keystroke re-evaluates the trailing-token match fresh - a token
    // Escape just closed (slashToken's own doc comment above) reopens on the
    // very next text change rather than staying closed indefinitely.
    const caret = event.target.selectionStart ?? event.target.value.length;
    setSlashToken(parseSlashToken(event.target.value, caret));
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
    const spliced = spliceSlashCommand(textRef.current, slashToken, item.invocation);
    textEditor.write(spliced.text, spliced.caret);
    setSlashToken(null);
    textareaRef.current?.focus();
  }

  // clearIfUnchanged mirrors clearComposerDraftIfUnchanged (parity-m5-
  // composer.md §A): reads textRef.current, not `submittedText` (a `const`
  // closed over by this async handler at call time, which never changes
  // after that point regardless of later renders) - textRef.current is
  // kept synchronously current by updateText() regardless of which
  // render's closure this particular submitAction call started from.
  function clearIfUnchanged(submittedText: string): void {
    if (textRef.current === submittedText) {
      updateText("");
      clearDraft(ref);
    }
  }

  // handleDrainSuccess is QueueStrip's own onDrainSuccess seam (its "Steer
  // now" button, a SEPARATE trigger from this component's own classic
  // steer/drain path below): mirrors the legacy "the textarea clears" rule
  // after the drain is durably recorded, gated the SAME way
  // clearIfUnchanged gates this
  // component's own drain path - only if the text is unchanged since the
  // drain was TRIGGERED (lastDrainSnapshotRef, populated by getComposerText
  // below at the moment QueueStrip actually read it), not unconditionally.
  // QueueStripProps.onDrainSuccess itself takes no arguments, so this ref is
  // the seam's own way of recovering a submitted-snapshot to compare
  // against - see w5-integration-wiring-report.md Concern #2, previously an
  // unconditional clear that could silently discard an edit made while the
  // strip's own drain request was still in flight.
  function handleDrainSuccess(): void {
    const snapshot = lastDrainSnapshotRef.current;
    if (snapshot === null) return; // defensive only: onDrainSuccess never fires without a prior getComposerText() call
    if (textRef.current === snapshot.text) {
      updateText("");
      clearDraft(ref);
    }
    // Unconditional, like submitAction's own clearSubmitted call below: safe
    // regardless of the text-unchanged check above, since it only ever
    // removes the SPECIFIC markers this drain's own snapshot captured - an
    // attachment staged after that snapshot survives untouched either way.
    attachments.clearSubmitted(snapshot.markers);
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
  // Deliberately typed to accept only `restoredText`, not the second
  // `attachments` parameter QueueStripProps.onRestoreToComposer's own
  // signature allows for - a queued entry's edit is a text-only recompose
  // per parity (contracts-composer-queue-pending.md:70, parity-m5-
  // composer.md:102): dropped image attachments surface via QueueStrip's
  // own durable queue state, never restored here. Any attachments argument a
  // caller passes is simply extra to a JS/TS call and never reaches this
  // function's body.
  function restoreTextToComposer(restoredText: string): void {
    const merged = mergeDraftText(textRef.current, restoredText);
    textEditor.write(merged, merged.length);
    textareaRef.current?.focus();
  }

  function activateRecovery(record: MutationRecoveryRecord): void {
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    const merged = mergeRecoveryComposerDraft(textRef.current, attachments.items, recoveryComposerDraft(record));
    const currentRecoveryId = activeRecoveryIdRef.current;
    if (currentRecoveryId === null) {
      recoveryOwnsLocalDraftRef.current = true;
      setActiveRecoveryId(record.clientMutationId);
    }
    updateText(merged.text);
    attachments.replaceWithSettled(merged.attachments);
    cursorToRestoreRef.current = merged.text.length;
    textareaRef.current?.focus();

    const ownerId = currentRecoveryId ?? record.clientMutationId;
    const persistence = queueRecoveryPersistence(ownerId, merged.text, settledInputAttachments(merged.attachments));
    if (currentRecoveryId !== null && currentRecoveryId !== record.clientMutationId) {
      void persistence
        .then(async () => {
          await discardRecoveryPendingTurn(record.clientMutationId, ref);
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
  // lastDrainSnapshotRef (text + the currently-staged marker set) so
  // handleDrainSuccess can later tell whether the composer changed between
  // THIS read and the drain actually resolving - QueueStrip only ever calls
  // this once per handleDrain invocation, immediately before starting the
  // request, so the snapshot always reflects exactly what that drain sent.
  function getComposerText() {
    const markers = new Set(attachments.items.map((item) => item.marker));
    lastDrainSnapshotRef.current = { text: textRef.current, markers };
    return {
      text: textRef.current,
      attachments: attachments.toInputAttachments(),
      hasPending: attachments.hasPending,
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
    const submittedMarkers = new Set(attachments.items.map((item) => item.marker));
    const payload = attachments.toInputAttachments();
    const submittedRecoveryId = activeRecoveryIdRef.current;
    let wonRecoveryResend = true;
    setBusyAction(kind === "send" || kind === "queue" ? "submit" : "steer");
    try {
      await submitWithPendingTracking(
        {
          ref,
          method: kind,
          text: submittedText,
          attachments: payload,
          onFailure: (err) => {
            const label = kind === "send" ? "Send" : kind === "queue" ? "Queue" : kind === "steer" ? "Steer" : "Drain";
            toasts.push("error", sessionActionError(`${label} failed`, err));
          },
        },
        async () => {
          if (submittedRecoveryId !== null) {
            await queueRecoveryPersistence(submittedRecoveryId, submittedText, payload);
            wonRecoveryResend = await resendRecoveryPendingTurn(submittedRecoveryId, ref, kind, submittedText, payload);
            return;
          }
          if (kind === "send") return threadsStore.getState().send(ref, submittedText, payload);
          if (kind === "queue") return threadsStore.getState().queue(ref, submittedText, payload);
          if (kind === "steer") return threadsStore.getState().steer(ref, submittedText, payload);
          return threadsStore.getState().drainAsSteer(ref, submittedText, payload);
        },
      );
      if (submittedRecoveryId !== null && activeRecoveryIdRef.current === submittedRecoveryId) {
        recoveryOwnsLocalDraftRef.current = false;
        setActiveRecoveryId(null);
        if (!wonRecoveryResend) toasts.push("info", "This message was already sent in another tab.");
        if (textRef.current !== submittedText) writeDraft(ref, textRef.current);
      }
      clearIfUnchanged(submittedText);
      attachments.clearSubmitted(submittedMarkers);
    } catch {
      // The local durable write failed. The submitted composer payload stays
      // untouched and no network request was eligible to start.
    } finally {
      setBusyAction(null);
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
  async function handleBuiltinSubmit(match: BuiltinMatch): Promise<void> {
    const submittedText = textRef.current;
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
    if (outcome.ok) clearIfUnchanged(submittedText);
    // On failure: the draft is left exactly as typed (clearIfUnchanged is
    // simply never called) - runBuiltinCommand has already toasted why.
  }

  function handleFormSubmit(event: FormEvent): void {
    event.preventDefault();
    if (busyAction !== null) return;
    if (!hasContent) return; // empty composer: no-op, no request, no message
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    // The Slack-model interception: a draft with no attachments that parses
    // as a known BUILT-IN session command (sessionBuiltins, above) runs that
    // command instead of sending the text as a message. A message carrying
    // an attachment is never read as a command, regardless of its text.
    // Everything that does NOT match - an unknown "/foo", or a plugin
    // catalog command (matchBuiltinInvocation only ever matches
    // sessionBuiltins, never the catalog - see that function's own doc
    // comment) - falls straight through to the ordinary routing below,
    // unchanged: that's the escape hatch.
    if (!hasAttachments) {
      const match = matchBuiltinInvocation(text, sessionBuiltins);
      if (match) {
        void handleBuiltinSubmit(match);
        return;
      }
    }
    // `availability` already carries the resumable-session substitution (see
    // where it is computed). Re-deriving it here is what let the tooltip and
    // the router disagree: the tooltip read the table while this read an
    // override, so the button could promise to queue and then fire a send.
    const route = decideSubmitRoute({ hasContent, availability });
    if (route === "none") {
      toasts.push("error", "Send is not available for this session");
      return;
    }
    void submitAction(route);
  }

  function handleSteerClick(): void {
    if (busyAction !== null) return;
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    const route = decideSteerRoute({ hasText, hasAttachments, queueDepth });
    if (route === "none") {
      textareaRef.current?.focus();
      return;
    }
    // Both routes steer the ACTIVE turn; without one the daemon rejects them
    // ("no active turn to steer"). Drain needs this guard as much as steer:
    // unguarded, a Steer-click routing to drain (non-empty queue or staged
    // attachments) minted a durable intent the hub rejects forever (kata wr3s).
    if ((route === "steer" || route === "drain") && !activeTurnId) {
      toasts.push("error", `${route === "drain" ? "Drain" : "Steer"} failed: no active turn`);
      return;
    }
    void submitAction(route);
  }

  async function handleInterruptClick(): Promise<void> {
    if (busyAction !== null) return;
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
  function handleKeyDown(event: ReactKeyboardEvent<HTMLTextAreaElement>): void {
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
  function handleTextareaBlur(): void {
    if (ended) setFollowUpFocused(false);
    setSlashToken(null);
  }

  return (
    <div className={CLASS.composer}>
      {/* T4: ask dock - renders above the queue strip.
          useAskDockPending(ref) (askPending, above) hides/inerts the input
          row below while a question is pending. */}
      <AskDock ref={ref} />
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
        busy={busyAction !== null}
        onDrainBusyChange={(draining) => setBusyAction(draining ? "drain" : null)}
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
      {(!ended || showFollowUpCard) && (
        <div className={CLASS.formAnchor}>
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
                field={
                  <Textarea
                    ref={textareaRef}
                    value={text}
                    onChange={handleTextChange}
                    onKeyDown={handleKeyDown}
                    onPaste={handlePaste}
                    autoGrow
                    // The PromptCard around it draws the one border this field
                    // needs, and owns the focus ring via :focus-within.
                    seamless
                    // A finished session's card is one line of invitation at
                    // rest, opening to a real writing surface once it has focus.
                    // Driven from React state rather than a :focus-within CSS
                    // rule because the floor has to reach the field's own `rows`
                    // to take effect at all (see widgets/textarea's rows
                    // comment), and only the prop can do that.
                    minLines={ended ? (followUpEngaged ? 3 : 1) : undefined}
                    onFocus={ended ? () => setFollowUpFocused(true) : undefined}
                    onBlur={handleTextareaBlur}
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
                      <SessionChrome ref={ref} placement="composer" />
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
                            disabled={busyAction !== null}
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
                          disabled={busyAction !== null || !hasContent || !(ended ? canSendWhenEnded : canCompose)}
                        >
                          <span className={CLASS.submitLabel}>Send</span>
                        </Button>
                      </Tooltip>
                      {showSteer && (
                        <Tooltip
                          label={
                            enterToSend
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
                            // already gate this control's existence.
                            disabled={busyAction !== null}
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
    </div>
  );
}
