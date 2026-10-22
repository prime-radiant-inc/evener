// Sign in takes priority over Retry on the latest failure. A recovery-fenced
// session offers no Resume row: Send carries that resume.
//
// The one action an error row in the transcript offers (spec 8.2, "Error";
// rulings 24 and 26): Sign in when a sign-in failed, and otherwise Retry on
// the latest turn's own failure while Send can act AND the input that opened
// the turn can be re-issued. Retry re-sends that input — the person's own
// words and images (turnRetryInput.ts), never a sentence put in their mouth;
// with no input to re-send, the row offers no Retry at all.
import type { ThreadModel } from "@evener/appwire-client";
import { failureRowIdentity } from "../projectedRows";

export type ErrorAction = "signIn" | "retry";

// Credentials that expired or are invalid, in either word order.
const SIGN_IN_FAILURE =
	/sign[- ]?in|log[- ]?in|\b401\b|unauthori[sz]ed|credentials? (?:expired|invalid)|(?:expired|invalid) credentials?/i;

export function errorAction(
	row: { id: string; title: string; detail: string; turnId?: string },
	session: Pick<ThreadModel, "turns">,
	canSend: boolean,
): ErrorAction | null {
	if (SIGN_IN_FAILURE.test(row.title) || SIGN_IN_FAILURE.test(row.detail)) return "signIn";
	// Only a turn's own failure row, and only the latest turn's: the sentence
	// speaks about where the session is now.
	const ownFailure = row.turnId !== undefined && row.id === failureRowIdentity(row.turnId);
	const latest = session.turns[session.turns.length - 1]?.id;
	if (!(canSend && ownFailure && row.turnId === latest)) return null;
	return "retry";
}
