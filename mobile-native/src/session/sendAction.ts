// What the composer's one Send does right now (spec 8.5). It mirrors the web
// composer's availability (cmd/evener-hub/frontend/src/panes/session/composer/
// Composer.tsx, availabilityFor):
// - the package's send/queue table, with this client's own unreflected send
//   as its tier 6;
// - a paused session sends nothing until it is resumed;
// - a finished session sends, and so resumes, when the hub says it can.
import { deriveSendQueueAvailability, type ThreadModel } from "@evener/appwire-client";
import { ownPendingSend, type PendingTurnEntry } from "@evener/appwire-client/state/mutation";

export type SendAction = "send" | "queue" | "resume" | "none";

export type SendSource = Pick<ThreadModel, "status" | "capabilities" | "resumeRequired">;

// The statuses of a session with no runtime: a first message resumes it.
const ENDED = new Set(["ended", "closed", "notLoaded"]);

export function sendAction(
	conversation: SendSource,
	pendingMutations: readonly PendingTurnEntry[] | null | undefined,
	connected: boolean,
): SendAction {
	if (!connected || conversation.resumeRequired) return "none";
	const status = conversation.status.type;
	const availability = deriveSendQueueAvailability({
		statusType: status,
		capabilities: conversation.capabilities,
		hasPendingSend: ownPendingSend(pendingMutations),
	});
	if (availability.canQueue) return "queue";
	const ended = ENDED.has(status);
	// Send offers only what the store's send() will take: it requires the
	// hub's send capability whatever the status.
	if (conversation.capabilities.send && (availability.canSend || ended)) return ended ? "resume" : "send";
	return "none";
}

export function composerPlaceholder(action: SendAction, questionPending: boolean): string {
	if (questionPending) return "Answer or ask…";
	if (action === "queue") return "Tell the agent something…";
	if (action === "resume") return "Message to resume";
	return "Message";
}

/** Send is a paper airplane, so its accessibility label says what pressing it
 * does. */
export function sendLabel(action: SendAction, questionPending: boolean): string {
	if (questionPending) return "Send answer";
	if (action === "queue") return "Queue message";
	if (action === "resume") return "Send and resume";
	return "Send";
}
