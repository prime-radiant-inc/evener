// Resume and Sign in take priority over continuation on the latest failure.
import type { ThreadModel } from "@evener/appwire-client";
import { failureRowIdentity } from "../projectedRows";

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
	if (!(canSend && ownFailure && row.turnId === latest)) return null;
	return "retry";
}
