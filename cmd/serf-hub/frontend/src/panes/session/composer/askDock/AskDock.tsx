// AskDock is the composer's ask_user answering surface (wave-5 plan T4):
// renders every currently-pending batch (askDockStore.ts owns the
// bookkeeping - see that file's header for why a batch is the right unit,
// not one shared list) as its own card group with its own footer and Send
// button, so a late-arriving question during an in-flight submission is
// independently answerable and independently sendable rather than being
// blocked or merged (contracts-composer-queue-pending.md test-ask-
// submit.js). Renders nothing at all when there is nothing pending.
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
import { useEffect, useRef } from "react";
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

  return (
    <div className={CLASS.batch}>
      {batch.questions.map((question, index) => (
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
      ))}
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
  // focus-management relationship.
  useEffect(() => {
    const isEmpty = batches.length === 0;
    const wasEmpty = wasEmptyRef.current;
    wasEmptyRef.current = isEmpty;
    if (!wasEmpty || isEmpty) return;
    const first = dockRef.current?.querySelector<HTMLElement>(
      'input[type="radio"], input[type="checkbox"], input[type="text"], button',
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
