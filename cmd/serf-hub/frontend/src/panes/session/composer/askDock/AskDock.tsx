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
// Mount expectations for whoever wires this into Composer.tsx's tree (T2,
// at merge - see that file's own header, "T3/T4 render inside Composer's
// own tree"):
//   - <AskDock ref={ref} /> - `ref` matches Composer/SessionChrome's own
//     established prop-name convention (a plain prop, not React's ref -
//     fine under this project's React 19).
//   - Answer text follows the same durable send path as the main composer.
//     Network outcomes are owned by the outbox/recovery surfaces and never
//     restore text into the main composer.
//   - This component does NOT hide/inert the plain composer surface or
//     own its mode-switch status announcement ("Message composer ready.")
//     - that is the composer's own surface to show/hide, and T2 owns it.
//     Call the exported useAskDockPending(ref) hook to decide whether to
//     hide/inert the plain composer for a given ref; this component's own
//     internal status region only announces ENTERING ask-response mode
//     (there is content to hide FOR, once this returns non-null).
//   - Failure feedback for a local durable-enqueue error is a toast (the
//     wave's decided convention, T1's loadOlder reference implementation);
//     network outcomes are rendered by recovery state, not an inline banner.
import { useEffect, useId, useRef } from "react";
import { Button, useToasts } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { AskQuestionCard } from "./AskQuestionCard";
import type { AskResolution } from "./askCompose";
import { type AskAnswerState, askDockStore, useAskDockStore } from "./askDockStore";
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

// useAskDockPending is the seam T2 (or any other composer-surface owner)
// reads to decide whether to hide/inert the plain composer for `ref` -
// see this file's own header.
export function useAskDockPending(ref: string): boolean {
  return useAskDockStore((s) => (s.byRef.get(ref)?.batches.length ?? 0) > 0);
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

  const tabId = (index: number) => `${baseId}-tab-${index}`;
  const panelId = `${baseId}-panel`;

  function selectTab(index: number, focus: boolean) {
    const question = batch.questions[index];
    if (!question) return;
    askDockStore.getState().setActive(sessionRef, batch.id, question.key);
    if (focus) tabRefs.current[index]?.focus();
  }

  // ARIA tabs with automatic activation (arrow keys both move and select -
  // the panel is right there on the same surface, so there is no expensive
  // switch to defer behind a second Enter press). Home/End jump the ends.
  function handleTabsKeyDown(event: React.KeyboardEvent) {
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
    <div className={CLASS.batch}>
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
        <span className={CLASS.count} aria-live="polite" aria-atomic="true">
          {answeredCount} of {total} {total === 1 ? "question" : "questions"} answered
        </span>
        {/* Blue primary, same pattern as sandboxEscalation's Allow button
            (topic 16: amber owns the container above, blue owns the one
            action that resolves it) - Button's own primary variant is the
            token-contract-ungated --accent, so this needs no allowlisting. */}
        <Button variant="primary" size="sm" disabled={batch.sending} onClick={() => onSend(batch.id)}>
          Send answers
        </Button>
      </div>
    </div>
  );
}

export function AskDock({ ref: sessionRef }: AskDockProps) {
  const batches = useAskDockStore((s) => s.byRef.get(sessionRef)?.batches ?? NO_BATCHES);
  const answers = useAskDockStore((s) => s.byRef.get(sessionRef)?.answers ?? NO_ANSWERS);
  const toasts = useToasts();
  const dockRef = useRef<HTMLDivElement>(null);
  const wasEmptyRef = useRef(true);

  // Auto-focuses the first answer control the moment the dock activates
  // (empty -> non-empty) - edge-triggered on batches.length so a LATER
  // ask_user call that only grows an already-open batch, or that mints a
  // sibling batch while another is sending, never steals focus from an
  // answer already in progress (test-ask-card.js: "a later ask_user call
  // that adds more questions does not steal focus from an answer input
  // currently being edited"). No ref threads down into AskQuestionCard for
  // this - querying the dock's own root for the first focusable control is
  // simpler and this is a one-time, edge-triggered action, not an ongoing
  // focus-management relationship. Scoped to [data-ask-question] (the
  // question card) so a multi-question batch's TAB BUTTONS - which sit
  // before the card in DOM order (kata 99yf) - never win this query over
  // the first actual answer control.
  useEffect(() => {
    const isEmpty = batches.length === 0;
    const wasEmpty = wasEmptyRef.current;
    wasEmptyRef.current = isEmpty;
    if (!wasEmpty || isEmpty) return;
    const first = dockRef.current?.querySelector<HTMLElement>(
      '[data-ask-question] input[type="radio"], [data-ask-question] input[type="checkbox"], [data-ask-question] input[type="text"], [data-ask-question] button',
    );
    first?.focus();
  }, [batches.length]);

  if (batches.length === 0) return null;

  async function handleSend(batchId: string) {
    const outcome = await askDockStore.getState().sendBatch(sessionRef, batchId);
    if (outcome.outcome === "error") {
      // Already the finished sentence, labelled by the store - the one place
      // that can still tell a failed send from the failed session resume
      // behind it (askDockStore's SendBatchOutcome).
      toasts.push("error", outcome.message);
    }
    // "sent"/"stale": nothing further to do here - a successful send's own
    // batch removal, and a stale click's own no-op, are both already
    // reflected by askDockStore's reactive state.
  }

  return (
    <div className={CLASS.dock} ref={dockRef} data-ask-response-dock>
      <div className={CLASS.anchor} role="status" aria-live="polite">
        Answer the agent’s questions.
      </div>
      {batches.map((batch) => (
        <AskBatchCard key={batch.id} sessionRef={sessionRef} batch={batch} answers={answers} onSend={handleSend} />
      ))}
    </div>
  );
}
