// AskDock is the composer's ask_user answering surface (wave-5 plan T4):
// renders every currently-pending batch (askDockStore.ts owns the
// bookkeeping - see that file's header for why a batch is the right unit,
// not one shared list) as its own card group with its own footer and Send
// button, so a late-arriving question during an in-flight submission is
// independently answerable and independently sendable rather than being
// blocked or merged (contracts-composer-queue-pending.md test-ask-
// submit.js). Renders nothing at all when there is nothing pending.
//
// Within a batch, exactly ONE question is on screen at a time (kata 99yf):
// a multi-question batch leads with a tab strip - one tab per question, in
// posting order, with an answered check - and only the active tab's
// AskQuestionCard renders below it, so a 4-question ask no longer fills the
// screen with four stacked cards. The active tab is askDockStore state
// (survives a pane remount), and a one-click resolution auto-advances to
// the next unanswered question (see askDockStore.setAnswer). A
// single-question batch has nothing to switch to, so it gets no strip.
//
// The footer's primary button turns a multi-question batch into a walk
// (ask-dialog UX rework): it reads "Next question" on every question but
// the last - answered or not - wraps from the last question back to the
// first still-unanswered one, and only becomes an enabled "Send answers"
// once every question has an answer, so a batch can never be submitted
// past unseen or unanswered questions. A single-question batch keeps the
// original always-send contract (parity-m5-composer.md §C: Send is always
// enabled, an unanswered question composes as skipped) since there is
// nowhere else to walk.
//
// Mount expectations (Session.tsx wires this in as TranscriptBody's
// trailingRow - the transcript's last virtual row, so the answering surface
// scrolls with the content instead of covering the footer):
//   - <AskDock ref={ref} /> - `ref` matches Composer/SessionChrome's own
//     established prop-name convention (a plain prop, not React's ref -
//     fine under this project's React 19).
//   - All interactive state lives in askDockStore, NOT component state: the
//     virtual list unmounts this row when the reader scrolls far enough
//     away, and remounts it on return - answers, notes, and the active tab
//     must survive that, and they do.
//   - Answer text follows the same durable send path as the main composer.
//     Network outcomes are owned by the outbox/recovery surfaces and never
//     restore text into the main composer.
//   - This component does NOT hide/inert the plain composer surface or
//     own its mode-switch status announcement ("Message composer ready.")
//     - that is the composer's own surface to show/hide, and Composer.tsx
//     owns it. Call the exported useAskDockPending(ref) hook to decide
//     whether to hide/inert the plain composer for a given ref; this
//     component's own internal status region only announces ENTERING
//     ask-response mode (there is content to hide FOR, once this returns
//     non-null).
//   - Failure feedback for a local durable-enqueue error is a toast (the
//     wave's decided convention, T1's loadOlder reference implementation);
//     network outcomes are rendered by recovery state, not an inline banner.
//
// Phase 3 keyboard operation (webui-keybindings-p3): on top of the per-batch
// keys above, Alt+PageDown/Alt+PageUp jump focus between BATCHES directly,
// wrapping at both ends (landing on the target batch's selected tab when it
// has a tab strip, else its first answer control). Alt+Page* rather than
// Alt+Arrow* because the Phase 3 transcript-scroll bindings own
// Alt+ArrowUp/Down with allowInEditable: false - and the dispatcher's
// editable test only covers INPUT/TEXTAREA/SELECT, so from the dock's tab
// and send BUTTONS those chords would still scroll the transcript - and
// rather than bare PageUp/PageDown, which keep their native meaning (the
// dock is its own overflow-y scroller). Escape stays a deliberate NO-OP:
// parity-m5-composer.md:120 documents that the dock is the one canonical
// response surface with no collapse state to escape to, so this component
// installs no Escape handler at all (AskDock.test.tsx pins both halves). A
// send returns focus: to the composer via composerFocus.ts's
// requestComposerFocus seam when the dock just emptied, or to the next
// still-pending batch's entry control when it has not (the composer input
// row is hidden/inert until the last batch resolves - Composer.tsx).
import { useCallback, useEffect, useId, useRef, useState } from "react";
import { Button, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { requestComposerFocus } from "../composerFocus";
import { AskQuestionCard } from "./AskQuestionCard";
import type { AskResolution } from "./askCompose";
import { type AskAnswerState, askDockStore, nextUnansweredKey, useAskDockStore } from "./askDockStore";
import styles from "./askdock.module.css";
import type { AskBatch } from "./reconcileBatches";

const CLASS = {
  dock: requireClass(styles.dock, "askdock.module.css", "dock"),
  anchor: requireClass(styles.anchor, "askdock.module.css", "anchor"),
  batch: requireClass(styles.batch, "askdock.module.css", "batch"),
  tabs: requireClass(styles.tabs, "askdock.module.css", "tabs"),
  tab: requireClass(styles.tab, "askdock.module.css", "tab"),
  tabCheck: requireClass(styles.tabCheck, "askdock.module.css", "tabCheck"),
  footer: requireClass(styles.footer, "askdock.module.css", "footer"),
  count: requireClass(styles.count, "askdock.module.css", "count"),
  visuallyHidden: requireClass(styles.visuallyHidden, "askdock.module.css", "visuallyHidden"),
};

export interface AskDockProps {
  ref: string;
}

const NO_BATCHES: AskBatch[] = [];
const NO_ANSWERS: Record<string, AskAnswerState> = {};
const UNTOUCHED_ANSWER: AskAnswerState = { resolution: null, note: "" };

// FIRST_CONTROL_SELECTOR names a batch's first answer control. Two callers:
// the activation auto-focus effect (scoped to [data-ask-question] so a
// multi-question batch's TAB BUTTONS - which sit before the card in DOM
// order, kata 99yf - never win over the first actual answer control) and
// focusBatchEntry's no-tab-strip fallback below.
const FIRST_CONTROL_SELECTOR =
  '[data-ask-question] input[type="radio"], [data-ask-question] input[type="checkbox"], [data-ask-question] input[type="text"], [data-ask-question] button';

// focusBatchEntry moves focus into a batch at its orientation point: the
// selected tab when the batch has a tab strip (the tab announces where in
// the walk the reader is - "2. Second, tab, selected"), else the first
// answer control, matching the dock-activation auto-focus below.
function focusBatchEntry(batchEl: HTMLElement): void {
  const entry =
    batchEl.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]') ??
    batchEl.querySelector<HTMLElement>(FIRST_CONTROL_SELECTOR);
  entry?.focus();
}

// useAskDockPending is the seam T2 (or any other composer-surface owner)
// reads to decide whether to hide/inert the plain composer for `ref` -
// see this file's own header.
export function useAskDockPending(ref: string): boolean {
  return useAskDockStore((s) => (s.byRef.get(ref)?.batches.length ?? 0) > 0);
}

// useAskDockActivationEpoch is the pending set's activation counter
// (askDockStore's activationEpoch) - the signal the transcript's
// new-content pill edges on, since a pending boolean alone cannot express
// "still pending, but atomically replaced by a different question".
export function useAskDockActivationEpoch(ref: string): number {
  return useAskDockStore((s) => s.byRef.get(ref)?.activationEpoch ?? 0);
}

function answerFor(answers: Record<string, AskAnswerState>, key: string): AskAnswerState {
  return answers[key] ?? UNTOUCHED_ANSWER;
}

interface AskBatchCardProps {
  sessionRef: string;
  batch: AskBatch;
  answers: Record<string, AskAnswerState>;
  onSend(batchId: string): void;
}

function AskBatchCard({ sessionRef, batch, answers, onSend }: AskBatchCardProps) {
  const answeredCount = batch.questions.filter((q) => answerFor(answers, q.key).resolution !== null).length;
  const total = batch.questions.length;
  const baseId = useId();
  const tabRefs = useRef<Array<HTMLButtonElement | null>>([]);

  // The one visible question (kata 99yf): the store's recorded tab choice
  // while it still names a question in this batch, else the first question
  // (an absent entry's render-time default - see AskDockRefState.active's
  // own doc comment). The selector returns a primitive, so this useStore
  // call re-renders only when the actual key changes.
  const storedActive = useAskDockStore((s) => s.byRef.get(sessionRef)?.active[batch.id]);
  const activeKey =
    storedActive !== undefined && batch.questions.some((q) => q.key === storedActive)
      ? storedActive
      : batch.questions[0]?.key;
  const activeIndex = batch.questions.findIndex((q) => q.key === activeKey);
  const activeQuestion = activeIndex >= 0 ? batch.questions[activeIndex] : undefined;

  // The footer's primary-button target: a question key to move to, or
  // undefined to send. Ask-dialog UX rework: a multi-question batch is a
  // WALK, not a form the reader can submit past - on every question but the
  // last the button is "Next question" and moves one question forward,
  // answered or not; on the last question it wraps back to the first
  // still-unanswered one, and only when every question has an answer does
  // it become "Send answers". If the last question itself is the only
  // unanswered one there is nowhere to advance to, so Send stays but is
  // DISABLED - skipping through a multi-question batch is impossible. A
  // single-question batch keeps its original always-send contract
  // (parity-m5-composer.md §C: Send is always enabled, an unanswered
  // question composes as skipped) since there is nowhere else to walk.
  const anyUnanswered = batch.questions.some((q) => answerFor(answers, q.key).resolution === null);
  const isLast = activeIndex === total - 1;
  let advanceTarget: string | undefined;
  if (total > 1) {
    if (!isLast) advanceTarget = batch.questions[activeIndex + 1]?.key;
    else if (anyUnanswered) advanceTarget = nextUnansweredKey(batch, answers, activeIndex);
  }
  const sendDisabled = batch.sending || (total > 1 && anyUnanswered && advanceTarget === undefined);

  const tabId = (index: number) => `${baseId}-tab-${index}`;
  const panelId = `${baseId}-panel`;

  function selectTab(index: number, focus: boolean) {
    const question = batch.questions[index];
    if (!question) return;
    askDockStore.getState().setActive(sessionRef, batch.id, question.key);
    if (focus) tabRefs.current[index]?.focus();
  }

  function handlePrimaryAction() {
    // The keyboard path honors the SAME disabled state as the footer button:
    // on the last question with the walk unfinished (or with a send in
    // flight) the button is disabled, and Mod+Enter must not bypass that and
    // submit the batch with the question on screen implicitly skipped.
    if (sendDisabled) return;
    if (advanceTarget !== undefined) {
      askDockStore.getState().setActive(sessionRef, batch.id, advanceTarget);
      return;
    }
    onSend(batch.id);
  }

  // Keyboard submit (UX fix): Mod+Enter anywhere in the batch invokes the
  // SAME primary action the footer button does (send, or advance to the
  // next question in the walk - the advanceTarget computation above), so a
  // keyboard-only reader never has to reach for the mouse. A bare Enter
  // does the same, but ONLY from the question's own free-text answer input
  // (data-ask-free-input, AskQuestionCard.tsx's own doc comment on that
  // attribute) - it is a plain <input>, not a <textarea>, so there is no
  // newline for a bare Enter to otherwise insert, unlike the main
  // composer's own Shift+Enter-newlines contract. Guarded on isComposing
  // both ways: an IME composition's own confirm keystroke also fires as
  // "Enter", and must never be read as a submit (same guard Composer.tsx's
  // own handleKeyDown applies to its Enter-to-send path).
  function handleBatchKeyDown(event: React.KeyboardEvent) {
    if (event.key !== "Enter" || event.nativeEvent.isComposing) return;
    const isPrimaryChord = event.metaKey || event.ctrlKey;
    const isFreeTextInputEnter =
      !isPrimaryChord &&
      !event.altKey &&
      !event.shiftKey &&
      (event.target as HTMLElement).dataset.askFreeInput === "true";
    if (!isPrimaryChord && !isFreeTextInputEnter) return;
    event.preventDefault();
    // The dock CONSUMED this chord: keep it from the window-level dispatcher
    // too. preventDefault alone only reaches bindings that honor
    // defaultPrevented - a binding that deliberately ignores it (rail.toggle)
    // remapped onto one of these chords would otherwise fire alongside the
    // send (roborev PR #884 round 2).
    event.stopPropagation();
    handlePrimaryAction();
  }

  // ARIA tabs with automatic activation (arrow keys both move and select -
  // the panel is right there on the same surface, so there is no expensive
  // switch to defer behind a second Enter press). Home/End jump the ends.
  function handleTabsKeyDown(event: React.KeyboardEvent) {
    // Any modifier means the key belongs elsewhere: Alt+ArrowUp/Down and
    // Alt+Home/End are the global transcript scroll/jump chords, and from a
    // tab BUTTON the dispatcher's editable test does not shield them - the
    // walk must not eat them (roborev PR #884 round 2).
    if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    const current = activeIndex >= 0 ? activeIndex : 0;
    let next = -1;
    if (event.key === "ArrowRight") next = (current + 1) % total;
    else if (event.key === "ArrowLeft") next = (current - 1 + total) % total;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = total - 1;
    if (next === -1) return;
    event.preventDefault();
    selectTab(next, true);
  }

  function renderCard(question: AskBatch["questions"][number], index: number) {
    return (
      <AskQuestionCard
        key={question.key}
        question={question}
        number={index + 1}
        answer={answerFor(answers, question.key)}
        onResolutionChange={(resolution: AskResolution | null) =>
          askDockStore.getState().setAnswer(sessionRef, question.key, resolution)
        }
        onNoteChange={(note) => askDockStore.getState().setNote(sessionRef, question.key, note)}
      />
    );
  }

  return (
    // data-ask-batch lets the dock-level batch jump (Alt+PageUp/Down) and the
    // post-send focus move locate each batch's card without depending on the
    // CSS-module class name.
    // biome-ignore lint/a11y/noStaticElementInteractions: catches Mod+Enter/Enter bubbling up from the batch's own controls (radios, the free-text input) - the div is a layout container, not itself interactive, same precedent as Settings.tsx's own Escape-catching wrapper
    <div className={CLASS.batch} onKeyDown={handleBatchKeyDown} data-ask-batch={batch.id}>
      {total > 1 && (
        // key="tabs"/key="panel" on both siblings: when a late ask_user call
        // grows a single-question batch past one question, the tab strip
        // INSERTS before the card wrapper - keyed siblings let React reuse
        // the wrapper instead of remounting it, which is what keeps an
        // in-progress answer's focus alive across exactly that transition
        // (test-ask-card.js's "does not steal focus" contract).
        <div key="tabs" className={CLASS.tabs} role="tablist" aria-label="Questions" onKeyDown={handleTabsKeyDown}>
          {batch.questions.map((question, index) => {
            const answered = answerFor(answers, question.key).resolution !== null;
            const selected = question.key === activeKey;
            return (
              <button
                key={question.key}
                ref={(el) => {
                  tabRefs.current[index] = el;
                }}
                type="button"
                role="tab"
                id={tabId(index)}
                aria-selected={selected}
                aria-controls={panelId}
                // Roving tabindex: only the selected tab is in the tab order -
                // the strip is one stop, not N, and arrows move within it.
                tabIndex={selected ? 0 : -1}
                className={CLASS.tab}
                onClick={() => selectTab(index, false)}
                // aria-label, not a visually-hidden suffix inside the
                // content: the answered state must read identically
                // everywhere ("1. First (answered)"), and content-derived
                // names get implementation-defined whitespace at element
                // boundaries. The ✓ glyph stays aria-hidden decoration for
                // sighted readers.
                aria-label={answered ? `${index + 1}. ${question.header} (answered)` : undefined}
              >
                {index + 1}. {question.header}
                {answered && (
                  <span className={CLASS.tabCheck} aria-hidden="true">
                    ✓
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}
      {/* The card wrapper is ALWAYS a key="panel" div (never the cards as
          direct batch children - see the key="tabs" comment above), so the
          1 -> 2 question transition swaps only its props (plain wrapper ->
          tabpanel), never remounts it. Two static branches, not conditional
          role/aria attributes: the a11y linter verifies aria-labelledby is
          valid for tabpanel only when the role is a static string. Inside
          it, only the active question's card mounts once tabs exist - the
          whole point of kata 99yf is that the other N-1 questions are not
          on the screen at all. */}
      {total > 1 ? (
        <div key="panel" role="tabpanel" id={panelId} aria-labelledby={tabId(activeIndex)}>
          {activeQuestion !== undefined && renderCard(activeQuestion, activeIndex)}
        </div>
      ) : (
        <div key="panel">{batch.questions.map((question, index) => renderCard(question, index))}</div>
      )}
      <div className={CLASS.footer}>
        {/* Visual count only - NOT a live region (virtualized remounts would
            re-announce it; AskDockAnnouncements owns count announcements). */}
        <span className={CLASS.count}>
          {answeredCount} of {total} {total === 1 ? "question" : "questions"} answered
        </span>
        {/* Blue primary, same pattern as sandboxEscalation's Allow button
            (topic 16: amber owns the container above, blue owns the one
            action that resolves it) - Button's own primary variant is the
            token-contract-ungated --accent, so this needs no allowlisting.
            Label follows advanceTarget so the button never claims
            "Send answers" while actually just moving the reader to another
            question, and Send stays disabled while a multi-question batch
            still has an unanswered question nobody is being walked to. */}
        <Button variant="primary" size="sm" disabled={sendDisabled} onClick={handlePrimaryAction}>
          {advanceTarget !== undefined ? "Next question" : "Send answers"}
        </Button>
      </div>
    </div>
  );
}

// AskDockAnnouncements is this surface's ONE aria-live region, mounted
// OUTSIDE the virtual list (Session.tsx, beside the transcript view
// announcements). The dock row is virtualized, so a live region inside it
// re-inserts on every scroll-away/scroll-back remount and re-announces
// unchanged text (roborev PR #854) - this component never unmounts with the
// row and announces only real transitions: a pending set arriving ("Answer
// the agent's questions.", the old in-row anchor's text), the answered
// count moving (the old in-row count span's text), and resolution clearing
// the region. The composer owns the exit half ("Message composer ready.")
// for the same reason it always has.
export function AskDockAnnouncements({ ref: sessionRef }: AskDockProps) {
  const batches = useAskDockStore((s) => s.byRef.get(sessionRef)?.batches ?? NO_BATCHES);
  const answers = useAskDockStore((s) => s.byRef.get(sessionRef)?.answers ?? NO_ANSWERS);
  const epoch = useAskDockStore((s) => s.byRef.get(sessionRef)?.activationEpoch ?? 0);
  const [announcement, setAnnouncement] = useState({ text: "", key: 0 });
  const prevRef = useRef<{ ref: string; pending: boolean; count: string; epoch: number }>({
    ref: sessionRef,
    pending: false,
    count: "",
    epoch: 0,
  });

  const pending = batches.length > 0;
  const total = batches.reduce((n, batch) => n + batch.questions.length, 0);
  const answered = batches.reduce(
    (n, batch) => n + batch.questions.filter((q) => answerFor(answers, q.key).resolution !== null).length,
    0,
  );
  const count = pending ? `${answered} of ${total} ${total === 1 ? "question" : "questions"} answered` : "";

  const announce = useCallback((text: string) => setAnnouncement((a) => ({ text, key: a.key + 1 })), []);

  useEffect(() => {
    const prev = prevRef.current;
    prevRef.current = { ref: sessionRef, pending, count, epoch };
    // A pane can be reused across refs (a sidebar click swaps the session
    // on a persistent pane): treat that as a fresh activation. The keyed
    // content remount below is what makes an identical-text re-announcement
    // audible at all - a live region announces content mutations, and
    // unchanged text is no mutation.
    if (prev.ref !== sessionRef) {
      announce(pending ? "Answer the agent’s questions." : "");
      return;
    }
    // Every activation announces the prompt - including an atomic
    // pending-set REPLACEMENT, which the epoch alone can see: pending stays
    // true throughout and the new set may carry an identical answered
    // count, so neither other signal moves.
    if (epoch !== prev.epoch && epoch > 0) {
      announce("Answer the agent’s questions.");
      return;
    }
    if (pending && count !== prev.count) {
      announce(count);
    } else if (!pending && prev.pending) {
      announce("");
    }
  }, [sessionRef, pending, count, epoch, announce]);

  return (
    <div className={CLASS.visuallyHidden} role="status" aria-live="polite" data-testid="ask-dock-announcements">
      <span key={announcement.key}>{announcement.text}</span>
    </div>
  );
}

export function AskDock({ ref: sessionRef }: AskDockProps) {
  const batches = useAskDockStore((s) => s.byRef.get(sessionRef)?.batches ?? NO_BATCHES);
  const answers = useAskDockStore((s) => s.byRef.get(sessionRef)?.answers ?? NO_ANSWERS);
  const pendingGreeted = useAskDockStore((s) => s.byRef.get(sessionRef)?.pendingGreeted ?? false);
  const toasts = useToasts();
  const dockRef = useRef<HTMLDivElement>(null);

  // Auto-focuses the first answer control the moment a pending set
  // activates (no batches -> some batches) AND the dock is actually visible.
  // Two separate protections, both because the dock is the transcript's
  // trailing virtual row:
  //  - The EDGE lives in askDockStore (pendingGreeted), not a component ref:
  //    scrolling far away unmounts the row and scrolling back remounts it,
  //    and a component-level edge would treat every remount as a fresh
  //    activation. A fresh question after a fully resolved set re-activates
  //    because the store resets the flag when the pending set empties; a
  //    later ask_user call that grows an already-open batch never
  //    re-triggers (test-ask-card.js's no-steal contract).
  //  - The VISIBILITY gate (IntersectionObserver): overscan mounts the row
  //    while the reader is scrolled away, and focusing then would move the
  //    reader's context to an off-screen control. Focus waits for the dock
  //    to actually intersect the viewport, which composes with the
  //    new-content pill: its jump brings the dock into view, and the
  //    intersection is what lands focus. Once the dock IS visible, plain
  //    focus() is intentional - the browser reveals the focused control,
  //    which a tall dock needs: the pill's jump aligns the dock's END, so
  //    the first control of a tall batch can still sit above the fold, and
  //    focusing it there without the reveal would strand it invisibly.
  //    jsdom has no IntersectionObserver - the fallback keeps tests without
  //    a stub on the immediate-focus path.
  // No ref threads down into AskQuestionCard for this - querying the dock's
  // own root for the first focusable control is simpler and this is a
  // one-time, edge-triggered action. Scoped to [data-ask-question] (the
  // question card) so a multi-question batch's TAB BUTTONS - which sit
  // before the card in DOM order (kata 99yf) - never win this query over
  // the first actual answer control.
  useEffect(() => {
    if (batches.length === 0 || pendingGreeted) return;
    const dock = dockRef.current;
    if (!dock) return;
    const focusFirst = () => {
      askDockStore.getState().markPendingGreeted(sessionRef);
      dock.querySelector<HTMLElement>(FIRST_CONTROL_SELECTOR)?.focus();
    };
    if (typeof IntersectionObserver !== "function") {
      focusFirst();
      return;
    }
    const observer = new IntersectionObserver((entries) => {
      if (!entries.some((entry) => entry.isIntersecting)) return;
      observer.disconnect();
      focusFirst();
    });
    observer.observe(dock);
    return () => observer.disconnect();
  }, [batches.length, pendingGreeted, sessionRef]);

  if (batches.length === 0) return null;

  async function handleSend(batchId: string) {
    const outcome = await askDockStore.getState().sendBatch(sessionRef, batchId);
    if (outcome.outcome === "error") {
      // Already the finished sentence, labelled by the store - the one place
      // that can still tell a failed send from the failed session resume
      // behind it (askDockStore's SendBatchOutcome).
      toasts.push("error", outcome.message);
      return;
    }
    // "stale": nothing further to do here - the dock re-checked and found
    // nothing left to send, and that no-op is already reflected by
    // askDockStore's reactive state.
    if (outcome.outcome !== "sent") return;
    // Focus return (Phase 3): the sent batch's card - including whichever
    // control had focus - unmounts with it, and without an explicit move
    // keyboard focus drops to <body>. removeBatch has already run inside
    // sendBatch, so the store (not the not-yet-flushed DOM) says what
    // remains. Dock empty: ask the composer to take focus back through the
    // composerFocus.ts seam (the request survives until the input row's
    // hidden/inert lifts - exactly the transition this send just caused).
    // Batches remain: the composer input row is still hidden/inert, so move
    // focus to the next pending batch's entry control instead. That
    // batch's DOM element still exists pre-flush and survives it (keyed),
    // so the query is safe at either flush ordering.
    const remaining = askDockStore.getState().byRef.get(sessionRef)?.batches ?? [];
    const nextBatch = remaining[0];
    if (nextBatch === undefined) {
      requestComposerFocus(sessionRef);
      return;
    }
    const nextEl = dockRef.current?.querySelector<HTMLElement>(`[data-ask-batch="${nextBatch.id}"]`);
    if (nextEl) focusBatchEntry(nextEl);
  }

  // Batch jump (Phase 3): Alt+PageDown/Alt+PageUp move focus between batch
  // cards directly, wrapping at both ends - the tab strip's ArrowLeft/Right
  // walk only moves within ONE batch. Strict Alt-only chord (an extra
  // modifier or an IME composition lets the key keep whatever other meaning
  // it has). Fewer than two batches: nothing to jump to, and the event is
  // left alone rather than claimed.
  function handleDockKeyDown(event: React.KeyboardEvent) {
    if (event.nativeEvent.isComposing) return;
    if (!event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return;
    const delta = event.key === "PageDown" ? 1 : event.key === "PageUp" ? -1 : 0;
    if (delta === 0) return;
    const root = dockRef.current;
    if (!root) return;
    const batchEls = Array.from(root.querySelectorAll<HTMLElement>("[data-ask-batch]"));
    if (batchEls.length < 2) return;
    event.preventDefault();
    // Consumed: stop propagation as well so a remapped window-level binding
    // that ignores defaultPrevented cannot fire alongside the jump (same
    // contract as the batch Enter handler above).
    event.stopPropagation();
    const current = batchEls.findIndex((el) => event.target instanceof Node && el.contains(event.target));
    const next =
      current === -1 ? (delta === 1 ? 0 : batchEls.length - 1) : (current + delta + batchEls.length) % batchEls.length;
    const target = batchEls[next];
    if (target) focusBatchEntry(target);
  }

  return (
    // biome-ignore lint/a11y/noStaticElementInteractions: catches Alt+PageUp/Down bubbling up from any control inside any batch - the div is a layout container, not itself interactive, same precedent as the batch-level Enter handler above
    <div className={CLASS.dock} ref={dockRef} data-ask-response-dock onKeyDown={handleDockKeyDown}>
      {/* Visual caption only - NOT a live region. The row is virtualized, so
          an aria-live region here would re-insert and re-announce on every
          scroll-away/scroll-back remount; the one live region for this
          surface is AskDockAnnouncements, mounted outside the virtual list. */}
      <div className={CLASS.anchor}>Answer the agent’s questions.</div>
      {batches.map((batch) => (
        <AskBatchCard key={batch.id} sessionRef={sessionRef} batch={batch} answers={answers} onSend={handleSend} />
      ))}
    </div>
  );
}
