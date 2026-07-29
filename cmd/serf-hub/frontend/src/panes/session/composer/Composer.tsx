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
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { sessionActionError } from "../../../protocol/errors";
import { deriveSendQueueAvailability } from "../../../protocol/sendQueueAvailability";
import { openPalette } from "../../../shell/palette/paletteController";
import { prefsStore, usePrefsStore } from "../../../stores/prefs";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
import {
  Button,
  Chip,
  chordLabel,
  Dropzone,
  IconButton,
  PromptCard,
  Textarea,
  Tooltip,
  useToasts,
} from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ImageGallery } from "../transcript/flow/ImageGallery";
import { AskDock, useAskDockPending } from "./askDock";
import { AttachIcon } from "./attachments/AttachIcon";
import { imageFilesFromClipboard } from "./attachments/clipboard";
import { type TextEditor, useAttachments } from "./attachments/useAttachments";
import styles from "./composer.module.css";
import { clearDraft, readDraft, writeDraft } from "./draft";
import { QueueStrip, submitWithPendingTracking } from "./queue";
import { RecoveryTray } from "./recovery/RecoveryTray";
import { decideSteerRoute, decideSubmitRoute, isTurnActive } from "./submitRouting";

export interface ComposerProps {
  ref: string;
}

const CLASS = {
  composer: requireClass(styles.composer, "composer.module.css", "composer"),
  chips: requireClass(styles.chips, "composer.module.css", "chips"),
  visuallyHidden: requireClass(styles.visuallyHidden, "composer.module.css", "visuallyHidden"),
  imageTile: requireClass(styles.imageTile, "composer.module.css", "imageTile"),
  imageThumbnail: requireClass(styles.imageThumbnail, "composer.module.css", "imageThumbnail"),
  dimensionsOverlay: requireClass(styles.dimensionsOverlay, "composer.module.css", "dimensionsOverlay"),
  removeImageButton: requireClass(styles.removeImageButton, "composer.module.css", "removeImageButton"),
  sendTiming: requireClass(styles.sendTiming, "composer.module.css", "sendTiming"),
};

function RemoveIcon() {
  return (
    <svg viewBox="0 0 12 12" width="10" height="10" aria-hidden="true">
      <path d="M2 2 L10 10 M10 2 L2 10" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
    </svg>
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
  const [busyAction, setBusyAction] = useState<BusyAction>(null);
  // Whether a FINISHED session's collapsed follow-up field currently has focus,
  // which is what expands it from its one-line resting state. Only read on that
  // path (see the ended card's minLines below); harmless everywhere else.
  const [followUpFocused, setFollowUpFocused] = useState(false);

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

  function updateText(nextText: string): void {
    textRef.current = nextText;
    setText(nextText);
  }

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
      writeDraft(ref, nextText);
      cursorToRestoreRef.current = cursor;
    },
  };
  const attachments = useAttachments(textEditor);

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

  if (!model) return null; // Session.tsx only mounts this once its own model is hydrated; defensive only.

  // Captured as plain consts (not read as `model.xyz` again below): a
  // closure that references `model` directly cannot inherit the `if
  // (!model) return null` narrowing above through a nested function
  // declaration (a TypeScript limitation, not a real possible-undefined
  // case - Session.tsx never mounts this component before its own model is
  // hydrated), so every handler below reads these already-narrowed values
  // instead of `model.<field>` directly.
  const activeTurnId = model.activeTurnId;
  const availability = deriveSendQueueAvailability({ statusType: model.status.type, capabilities: model.capabilities });
  const busy = isTurnActive(model.status.type, activeTurnId);
  const queueDepth = model.queue?.depth ?? 0;
  const hasText = text.trim() !== "";
  const hasAttachments = attachments.items.length > 0;
  const hasContent = hasText || hasAttachments;

  // Stop and Steer both act on an IN-FLIGHT turn - there is no turn to
  // interrupt and nothing to steer into otherwise, so they are rendered only
  // while one is running (`busy`) AND the harness advertises the matching
  // capability. `busy` also subsumes the ended/closed statuses isTurnActive
  // can never report as active. Both routes a Steer click can take need the
  // live turn: classic turn/steer carries expectedTurnId, and
  // turn/drainAsSteer is refused with "no active turn to steer" when nothing
  // is processing (server/appwire_runtime.go's handleAppTurnDrainAsSteer), so
  // a non-empty queue does not make Steer meaningful on an idle session.
  const showStop = busy && model.capabilities.interrupt;
  const showSteer = busy && model.capabilities.steer;
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
  // cezn: three independently-briefed personas all missed Send's timing
  // mid-flow because only the tooltip above says it, and a 300ms hover is
  // more than anyone gives a button they're about to click. This caption
  // says the same thing WITHOUT a hover - additive only, next to the
  // control row, never touching Send/Steer's own labels. Idle Send is
  // unambiguous (no Steer beside it, no timing question to answer), so it
  // only earns its place while that ambiguity actually exists; hidden
  // alongside the rest of the input row while an ask_user question is
  // pending, same as the row itself (askPending's own doc comment above).
  const showSendTiming = busy && availability.canQueue && !askPending;
  const ended = ENDED_STATUSES.has(model.status.type);
  // A finished session can still be sent to when the source says so: the hub
  // advertises Send for an exited serf thread and auto-resumes it on the first
  // message. The CAPABILITY is the authority here, not
  // deriveSendQueueAvailability - that table answers "can this turn be sent to
  // or queued behind right now", and it deliberately reports both-false for
  // ended/closed, which is the wrong question for a follow-up that resumes the
  // session first. Gating on it renders no card for exactly the sessions the
  // hub says are resumable. When the wire really advertises no send, no card is
  // rendered at all: an unusable field is worse than no field.
  const canCompose = availability.canSend || availability.canQueue;
  // Read here rather than inside the handlers below, which close over `model`
  // outside the narrowing this component does at its top (see that block's own
  // comment on why every handler reads a pre-narrowed local).
  const canSendWhenEnded = model.capabilities.send;
  const showFollowUpCard = ended && canSendWhenEnded;
  // A finished session's card earns its control row once the user engages with
  // it - focused, or holding text or an attachment. Content matters as well as
  // focus: a restored draft, or a blur with text still in the field, must not
  // strand a typed message with no visible way to send it.
  const followUpEngaged = followUpFocused || hasContent;

  function handleTextChange(event: { target: { value: string } }): void {
    updateText(event.target.value);
    writeDraft(ref, event.target.value);
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
    const existing = textRef.current;
    const merged = existing.trim() === "" ? restoredText : `${existing.replace(/\s+$/, "")}\n\n${restoredText}`;
    textEditor.write(merged, merged.length);
    textareaRef.current?.focus();
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
    const submittedText = text;
    const submittedMarkers = new Set(attachments.items.map((item) => item.marker));
    const payload = attachments.toInputAttachments();
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
        () => {
          if (kind === "send") return threadsStore.getState().send(ref, submittedText, payload);
          if (kind === "queue") return threadsStore.getState().queue(ref, submittedText, payload);
          if (kind === "steer") return threadsStore.getState().steer(ref, submittedText, payload);
          return threadsStore.getState().drainAsSteer(ref, submittedText, payload);
        },
      );
      clearIfUnchanged(submittedText);
      attachments.clearSubmitted(submittedMarkers);
    } catch {
      // The local durable write failed. The submitted composer payload stays
      // untouched and no network request was eligible to start.
    } finally {
      setBusyAction(null);
    }
  }

  function handleFormSubmit(event: FormEvent): void {
    event.preventDefault();
    if (busyAction !== null) return;
    if (!hasContent) return; // empty composer: no-op, no request, no message
    if (attachments.hasPending) {
      toasts.push("error", "Image attachment is still processing");
      return;
    }
    // A finished session routes as a plain send: the availability table reports
    // both-false for ended/closed because no turn is in flight to send to or
    // queue behind, but the hub advertises Send for a resumable thread and
    // resumes it on the first message. Without this, a follow-up to an ended
    // session falls through to "none" and toasts "Send is not available" at a
    // session the hub would have happily woken.
    const route = decideSubmitRoute({
      hasContent,
      availability: ended && canSendWhenEnded ? { canSend: true, canQueue: false } : availability,
    });
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
    if (route === "steer" && !activeTurnId) {
      toasts.push("error", "Steer failed: no active turn");
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
    // "/" as the first character of an EMPTY composer opens the command
    // palette (floor §2.1, legacy renderer.js:6914); any other "/" - mid-text
    // or in a non-empty composer - is a literal slash. textRef.current is the
    // synchronous live value, the same read every liveness-sensitive path in
    // this file uses.
    if (event.key === "/" && !event.metaKey && !event.ctrlKey && !event.altKey && textRef.current === "") {
      event.preventDefault();
      openPalette("/");
      return;
    }
    if (event.key !== "Enter") return;
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
      <RecoveryTray sessionRef={ref} threadId={model?.threadId} />
      {/* T3: queue strip - the queue preview (model.queue) above the input
          row; getComposerText/onRestoreToComposer/onDrainSuccess are this
          integration's own seam implementations, see each one's own doc
          comment above. busy/onDrainBusyChange share this component's own
          busyAction gate both ways (BusyAction's own "drain" doc comment). */}
      <QueueStrip
        ref={ref}
        getComposerText={getComposerText}
        onRestoreToComposer={restoreTextToComposer}
        onDrainSuccess={handleDrainSuccess}
        busy={busyAction !== null}
        onDrainBusyChange={(draining) => setBusyAction(draining ? "drain" : null)}
      />
      {hasAttachments && (
        <div className={CLASS.chips} hidden={askPending} inert={askPending}>
          {attachments.items.map((item) => {
            const isImage =
              item.mediaType.startsWith("image/") &&
              item.data &&
              typeof item.width === "number" &&
              typeof item.height === "number";

            if (isImage) {
              const imageUrl = `data:${item.mediaType};base64,${item.data}`;
              return (
                <div key={item.marker} className={CLASS.imageTile}>
                  <ImageGallery images={[{ src: imageUrl }]} />
                  <img className={CLASS.imageThumbnail} src={imageUrl} alt={item.name} />
                  {item.width !== undefined && item.height !== undefined && (
                    <div className={CLASS.dimensionsOverlay}>
                      {item.width}×{item.height}
                    </div>
                  )}
                  <button
                    type="button"
                    className={CLASS.removeImageButton}
                    aria-label={`Remove ${item.name}`}
                    onClick={() => attachments.removeItem(item.marker)}
                  >
                    <RemoveIcon />
                  </button>
                </div>
              );
            }

            return (
              <Chip key={item.marker} tone="neutral" onRemove={() => attachments.removeItem(item.marker)}>
                {/* A single template-literal string, not several sibling
                    expressions: Chip's own removeLabelFor only folds children
                    into the remove button's accessible name ("Remove <text>")
                    when children is unambiguously a string - multiple child
                    nodes would silently fall back to a bare "Remove". */}
                {`${item.name}${typeof item.width === "number" && typeof item.height === "number" ? ` (${item.width}×${item.height})` : ""}`}
              </Chip>
            );
          })}
        </div>
      )}
      {(!ended || showFollowUpCard) && (
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
                  onBlur={ended ? () => setFollowUpFocused(false) : undefined}
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
                        // canCompose comes from the availability table, which
                        // reports both-false for ended/closed: it answers "can
                        // this turn be sent to right now", and a follow-up to a
                        // finished session resumes it first. The capability is
                        // the authority there, the same way it is for whether
                        // this card renders at all - otherwise a session the hub
                        // will happily resume shows a permanently dead Send.
                        disabled={busyAction !== null || !hasContent || !(ended ? canSendWhenEnded : canCompose)}
                      >
                        Send
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
      )}
      {/* cezn: Send's timing, always visible - see showSendTiming's own doc
          comment above for why this exists and what it deliberately doesn't
          touch. */}
      {showSendTiming && <p className={CLASS.sendTiming}>Send queues until the agent stops</p>}
    </div>
  );
}
