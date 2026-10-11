// The one action an error row in the transcript offers (spec 8.2, "Error";
// rulings 24 and 26): Resume when the session is paused, Sign in when a
// sign-in failed, and otherwise Retry on the latest turn's own failure while
// Send can act. Retry sends Jesse's sentence as your message.
import type { ThreadModel } from "@evener/appwire-client";
import { failureRowIdentity } from "../projectedRows";

export const RETRY_MESSAGE = "Something went wrong. Please try again.";

export type ErrorAction = "resume" | "signIn" | "retry";

// Credentials that expired or are invalid, in either word order.
const SIGN_IN_FAILURE =
	/sign[- ]?in|log[- ]?in|\b401\b|unauthori[sz]ed|credentials? (?:expired|invalid)|(?:expired|invalid) credentials?/i;

export function errorAction(
	row: { id: string; title: string; detail: string; turnId?: string },
	session: Pick<ThreadModel, "resumeRequired" | "turns">,
	canSend: boolean,
): ErrorAction | null {
	if (session.resumeRequired) return "resume";
	if (SIGN_IN_FAILURE.test(row.title) || SIGN_IN_FAILURE.test(row.detail)) return "signIn";
	// Only a turn's own failure row, and only the latest turn's: the sentence
	// speaks about where the session is now.
	const ownFailure = row.turnId !== undefined && row.id === failureRowIdentity(row.turnId);
	const latest = session.turns[session.turns.length - 1]?.id;
	return canSend && ownFailure && row.turnId === latest ? "retry" : null;
}
