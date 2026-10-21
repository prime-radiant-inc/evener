// What the composer's one Send does right now (spec 8.5). It mirrors the web
// composer's availability (cmd/evener-hub/frontend/src/panes/session/composer/
// Composer.tsx, availabilityFor):
// - the package's send/queue table, with this client's own unreflected send
//   as its tier 6;
// - a recovery-fenced session sends, and so resumes, exactly as a shut-down
//   one does: Send is the recovery action, never a separate Resume press;
// - a shut-down session sends, and so resumes, when the hub says it can.
import { deriveSendQueueAvailability, SHUT_DOWN_STATUSES, type ThreadModel } from "@evener/appwire-client";
import { ownPendingSend, type PendingTurnEntry } from "@evener/appwire-client/state/mutation";

export type SendAction = "send" | "queue" | "resume" | "none";

export type SendSource = Pick<ThreadModel, "status" | "capabilities" | "resumeRequired">;

export function sendAction(
	conversation: SendSource,
	pendingMutations: readonly PendingTurnEntry[] | null | undefined,
	connected: boolean,
): SendAction {
	// A fenced session takes the message and resumes before it sends. Its only
	// unrunnable shape is a restartRequired daemon, which the table answers
	// "none" for below: its older process must be stopped first.
	//
	// Only the FIRST message resumes. A second one while the first is still
	// unreflected queues behind it: two parked turn/starts would race for the
	// same active turn once recovery lands, and the daemon rejects the loser as
	// already active instead of running it.
	//
	// The queue answer still obeys the same rules any other queue does - a
	// restart-required daemon offers nothing, and a harness that cannot queue
	// must not be offered (or, offline, made to persist) a queue request.
	if (conversation.resumeRequired) {
		if (conversation.status.type === "restartRequired") return "none";
		if (ownPendingSend(pendingMutations)) {
			const canQueue = deriveSendQueueAvailability({
				statusType: conversation.status.type,
				capabilities: conversation.capabilities,
				hasPendingSend: true,
			}).canQueue;
			return canQueue ? "queue" : "none";
		}
		return resumeOrNone(conversation);
	}
	if (!connected) return offlineAction(conversation, ownPendingSend(pendingMutations));
	const status = conversation.status.type;
	const availability = deriveSendQueueAvailability({
		statusType: status,
		capabilities: conversation.capabilities,
		hasPendingSend: ownPendingSend(pendingMutations),
	});
	if (availability.canQueue) return "queue";
	// A shut-down session has no runtime: a first message resumes it.
	const ended = SHUT_DOWN_STATUSES.has(status);
	// Send offers only what the store's send() will take: it requires the
	// hub's send capability whatever the status.
	if (conversation.capabilities.send && (availability.canSend || ended)) return ended ? "resume" : "send";
	return "none";
}

// A fenced session answers "resume" online or offline: the row parks durably
// either way and the resume runs behind it once the connection and the fence
// allow. The gate is the fence alone, never the send capability -
// applyThreadResumeRequirement CLEARS send as it sets resumeRequired, so a
// capability check here would refuse every fence the hub actually sends. The
// one shape that keeps no action is a restartRequired daemon, whose older
// process must be stopped first.
function resumeOrNone(conversation: SendSource): SendAction {
	if (conversation.status.type === "restartRequired") return "none";
	return "resume";
}

/** Offline, a message waits in the phone's outbox (spec 8.5, phase 6
 * ruling 13). By the time it arrives a turn may be running, which refuses a
 * turn/start, so it goes as if a send of this phone's were already pending,
 * the package's tier 6: it queues where the harness can (the daemon runs a
 * queued message at once on an idle session), and waits for the connection
 * where it can't. The first message to a shut-down session resumes it; a
 * message after it queues behind it. */
function offlineAction(conversation: SendSource, pendingSend: boolean): SendAction {
	const status = conversation.status.type;
	if (SHUT_DOWN_STATUSES.has(status) && !pendingSend) return conversation.capabilities.send ? "resume" : "none";
	const availability = deriveSendQueueAvailability({
		statusType: status,
		capabilities: conversation.capabilities,
		hasPendingSend: true,
	});
	return availability.canQueue ? "queue" : "none";
}

export function composerPlaceholder(action: SendAction, questionPending: boolean): string {
	if (questionPending) return "Answer or ask…";
	if (action === "queue") return "Tell the agent something…";
	if (action === "resume") return "Message to resume";
	return "Message";
}

/** Send is a paper airplane, so its accessibility label says what pressing it
 * does: offline, that it waits for the connection (ruling 14). */
export function sendLabel(action: SendAction, questionPending: boolean, connected = true): string {
	if (!connected) return questionPending ? "Send answer when you're back online" : "Send when you're back online";
	if (questionPending) return "Send answer";
	if (action === "queue") return "Queue message";
	if (action === "resume") return "Send and resume";
	return "Send";
}
