// The native question sheet's batches: the package's ask-dock store over the
// one conversation the session screen shows, in the subscribe/getSnapshot/
// begin/finish shape that screen drives. The screen has no ThreadModel yet -
// it scans its own conversation model for the live questions and owns the send
// - so this adapter feeds the store's reconcile directly and never wires it;
// the store's freeze/settle/exclude rules are the package's, not copied here.
import { type AskBatch, createAskDockStore } from "@evener/appwire-client";
import type { MobileQuestionRef } from "../../mobile/src/conversation/project";

// The batches this adapter hands back, carrying the rows' own question refs.
// The package's reconciliation filters and concatenates the very ref objects it
// was given (reconcileBatches.ts: `questions.filter(...)`, `[...b.questions,
// ...newQuestions]` — it never rebuilds a question), so a ref that went in with
// its bounded display copy comes back out with it. questionBatches.test.ts pins
// that pass-through.
export type MobileAskBatch = Omit<AskBatch, "questions"> & {
	questions: MobileQuestionRef[];
};

// One sheet, one conversation: the store keys by ref, the sheet does not.
const REF = "conversation";
const NO_BATCHES: MobileAskBatch[] = [];

/** Own submissions by question identity, using the shared reconciliation. */
export class QuestionBatches {
	private store = createAskDockStore();
	getSnapshot = (): MobileAskBatch[] =>
		(this.store.getState().byRef.get(REF)?.batches as
			| MobileAskBatch[]
			| undefined) ?? NO_BATCHES;
	subscribe = (listener: () => void) => this.store.subscribe(listener);
	reconcile(questions: MobileQuestionRef[]) {
		this.store.reconcile(REF, questions);
	}
	/** Freeze `expected` for sending. False when the batch is gone, already
	 * sending, or no longer holds the questions the sheet rendered. */
	begin(expected: MobileAskBatch): boolean {
		const current = this.getSnapshot().find(
			(batch) => batch.id === expected.id,
		);
		return (
			current !== undefined &&
			JSON.stringify(current.questions) ===
				JSON.stringify(expected.questions) &&
			this.store.beginSend(REF, expected.id)
		);
	}
	finish(id: string, accepted: boolean) {
		this.store.finishSend(REF, id, accepted);
	}
}
