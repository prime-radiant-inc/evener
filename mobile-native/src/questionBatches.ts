// The native question sheet's batches: the package's ask-dock store over the
// one conversation the session screen shows, in the subscribe/getSnapshot/
// begin/finish shape that screen drives. The screen has no ThreadModel yet -
// it scans its own conversation model for the live questions and owns the send
// - so this adapter feeds the store's reconcile directly and never wires it;
// the store's freeze/settle/exclude rules are the package's, not copied here.
import {
	type AskBatch,
	type AskQuestionRef,
	createAskDockStore,
} from "@evener/appwire-client";
import { questionsIdentity } from "./questionAnswers";

// One sheet, one conversation: the store keys by ref, the sheet does not.
const REF = "conversation";
const NO_BATCHES: AskBatch[] = [];

/** Own submissions by question identity, using the shared reconciliation. */
export class QuestionBatches {
	private store = createAskDockStore();
	getSnapshot = () =>
		this.store.getState().byRef.get(REF)?.batches ?? NO_BATCHES;
	subscribe = (listener: () => void) => this.store.subscribe(listener);
	reconcile(questions: AskQuestionRef[]) {
		this.store.reconcile(REF, questions);
	}
	/** Freeze `expected` for sending. False when the batch is gone, already
	 * sending, or no longer holds the questions the sheet rendered. */
	begin(expected: AskBatch): boolean {
		const current = this.getSnapshot().find(
			(batch) => batch.id === expected.id,
		);
		return (
			current !== undefined &&
			questionsIdentity(current.questions) ===
				questionsIdentity(expected.questions) &&
			this.store.beginSend(REF, expected.id)
		);
	}
	finish(id: string, accepted: boolean) {
		this.store.finishSend(REF, id, accepted);
	}
}
