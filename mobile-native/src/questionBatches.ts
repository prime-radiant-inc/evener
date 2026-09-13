import {
  type AskBatch,
  reconcileBatches,
} from "../../cmd/evener-hub/frontend/src/panes/session/composer/askDock/reconcileBatches";
import type { AskQuestionRef } from "../../cmd/evener-hub/frontend/src/protocol/deriveAskQuestions";

/** Own submissions by question identity, using the current web reconciliation. */
export class QuestionBatches {
  private batches: AskBatch[] = [];
  private excluded = new Set<string>();
  private sequence = 0;
  private listeners = new Set<() => void>();
  getSnapshot = () => this.batches;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private update(batches: AskBatch[]) {
    if (batches === this.batches) return;
    this.batches = batches;
    for (const listener of this.listeners) listener();
  }
  reconcile(questions: AskQuestionRef[]) {
    this.update(
      reconcileBatches(
        this.batches,
        questions.filter((q) => !this.excluded.has(q.key)),
        () => `questions-${++this.sequence}`,
      ),
    );
  }
  begin(expected: AskBatch): boolean {
    const current = this.batches.find((batch) => batch.id === expected.id);
    if (
      !current ||
      current.sending ||
      JSON.stringify(current.questions) !== JSON.stringify(expected.questions)
    )
      return false;
    this.update(
      this.batches.map((batch) =>
        batch === current ? { ...batch, sending: true } : batch,
      ),
    );
    return true;
  }
  finish(id: string, accepted: boolean) {
    const current = this.batches.find((batch) => batch.id === id);
    if (!current?.sending) return;
    if (accepted) {
      for (const question of current.questions) this.excluded.add(question.key);
      this.update(this.batches.filter((batch) => batch.id !== id));
    } else {
      this.update(
        this.batches.map((batch) =>
          batch === current ? { ...batch, sending: false } : batch,
        ),
      );
    }
  }
}
