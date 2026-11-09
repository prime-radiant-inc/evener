// Sign in takes priority over Retry on the latest failure. A recovery-fenced
// session offers no Resume row: Send carries that resume.
//
// The one action an error row in the transcript offers (spec 8.2, "Error";
// rulings 24 and 26): Sign in when a sign-in failed, and otherwise Retry on
// the canonical latest turn's own failure while Send can act. Retry continues
// that turn; it never re-sends the person's words or images.
//
// The action is an internal descriptor because Retry must carry the clicked
// failure's source turn through the row and into activation. Its `disabled`
// state is the row's own: a durable submission of this client's is unresolved,
// or the shared store cannot certify its pending projection. Sign in is
// independent and is never disabled by Retry's pending rule.
import { latestTurnId, retryEligibility, type RetryTurnThread } from "@evener/appwire-client";
import type { PendingTurnEntry } from "@evener/appwire-client/state/mutation";
import { failureRowIdentity, type MobileConversation } from "../projectedRows";

export type ErrorAction = { kind: "signIn" } | { kind: "retry"; failedTurnId: string; disabled: boolean };

/** The shared store facts Retry's eligibility reads. The canonical model is
 * present only while the store holds an accepted current latest-window
 * publication; `displayedLatestTurnId` is the displayed model's last turn,
 * used only to keep the last failure's row present (disabled) while that
 * authority is unavailable. */
export interface ErrorActionState {
	canonical: RetryTurnThread | null;
	displayedLatestTurnId: string | undefined;
	pendingMutations: readonly PendingTurnEntry[] | null | undefined;
	pendingMutationsReady: boolean;
}

// Credentials that expired or are invalid, in either word order.
const SIGN_IN_FAILURE =
	/sign[- ]?in|log[- ]?in|\b401\b|unauthori[sz]ed|credentials? (?:expired|invalid)|(?:expired|invalid) credentials?/i;

export function errorAction(
	row: { id: string; title: string; detail: string; turnId?: string },
	state: ErrorActionState,
	canSend: boolean,
): ErrorAction | null {
	if (SIGN_IN_FAILURE.test(row.title) || SIGN_IN_FAILURE.test(row.detail)) return { kind: "signIn" };
	// Only a turn's own failure row: the row that speaks for that turn, not a
	// warning or another item's error.
	const failedTurnId = row.turnId;
	if (failedTurnId === undefined || row.id !== failureRowIdentity(failedTurnId)) return null;
	// Canonical authority unavailable: the last displayed failure stays on the
	// row, but no press may act until the shared store publishes the model.
	// Send must still be able to act, the same live-service precondition the
	// shared decision applies when the model is present.
	if (state.canonical === null) {
		if (!canSend) return null;
		if (failedTurnId !== state.displayedLatestTurnId) return null;
		return { kind: "retry", failedTurnId, disabled: true };
	}
	// The row check above establishes that this is the clicked turn's own
	// failure. The shared decision owns the latest-turn, pending, readiness and
	// live-service rules and their precedence.
	const decision = retryEligibility({
		clickedTurnId: failedTurnId,
		thread: state.canonical,
		clickedTurnFailed: true,
		pendingMutations: state.pendingMutations,
		pendingMutationsReady: state.pendingMutationsReady,
		serviceReady: canSend,
	});
	switch (decision.reason) {
		// No action: a historical failure, a newer running turn, an incoherent
		// active state, invalidated/failed history, or no live service.
		case "not-latest-turn":
		case "clicked-turn-not-failed":
		case "no-live-service":
			return null;
		// The action stays visible; an unresolved own submission or an unread
		// projection disables it.
		case "pending-input-owned":
		case "pending-not-ready":
			return { kind: "retry", failedTurnId, disabled: true };
		case "allowed":
			return { kind: "retry", failedTurnId, disabled: false };
	}
}

/** The action the clicked turn's own failure row currently offers, resolved
 * from the canonical projection. Activation and the runtime precondition use
 * this rather than a fabricated row or the captured descriptor: a refresh that
 * removes the error, changes it to Sign in, or moves its precedence must
 * submit nothing. No canonical model means no authority, so no action. */
export function canonicalFailureAction(
	canonical: MobileConversation | null,
	failedTurnId: string,
	pendingMutations: readonly PendingTurnEntry[] | null | undefined,
	pendingMutationsReady: boolean,
	canSend: boolean,
): ErrorAction | null {
	if (canonical === null) return null;
	const row = canonical.items.find(
		(item) => item.kind === "failure" && item.id === failureRowIdentity(failedTurnId) && item.turnId === failedTurnId,
	);
	if (row === undefined || row.kind !== "failure") return null;
	return errorAction(
		row,
		{
			canonical,
			displayedLatestTurnId: latestTurnId(canonical),
			pendingMutations,
			pendingMutationsReady,
		},
		canSend,
	);
}
