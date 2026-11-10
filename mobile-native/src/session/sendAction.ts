// What the composer's one Send does right now (spec 8.5). It is the native
// adapter over the shared send/queue gate (appwire-client/typescript/
// sendQueueGate.ts), which owns the ordering, shut-down conversion,
// capability application and action answer both clients used to duplicate:
// - the package's send/queue table, with this client's own unreflected send
//   as its tier 6;
// - a recovery-fenced session sends, and so resumes, exactly as a shut-down
//   one does: Send is the recovery action, never a separate Resume press;
// - a shut-down session sends, and so resumes, when the hub says it can.
//
// This adapter supplies only the native client facts: the resume fence
// (resumeRequired) and the live connection. The gate never re-derives either
// from a status or a pending list.
import {
	NATIVE_SEND_QUEUE_POLICY,
	sendQueueGate,
	type SendQueueRecovery,
	type ThreadModel,
} from "@evener/appwire-client";
import { ownPendingSend, type PendingTurnEntry } from "@evener/appwire-client/state/mutation";

export type SendAction = "send" | "queue" | "resume" | "none";

export type SendSource = Pick<ThreadModel, "status" | "capabilities" | "resumeRequired">;

/** The native recovery fact: a fenced conversation asks for the explicit
 *  resume before it sends. Everything else leaves the gate's table.
 *
 *  The fence is the gate here, never the send capability:
 *  applyThreadResumeRequirement CLEARS send as it sets resumeRequired, so a
 *  capability check would refuse every fence the hub actually sends. Only a
 *  restartRequired daemon keeps no action, and the gate checks that first. */
export function nativeSendQueueRecovery(conversation: SendSource): SendQueueRecovery {
	return conversation.resumeRequired ? { kind: "explicit-resume" } : { kind: "none" };
}

export function sendAction(
	conversation: SendSource,
	pendingMutations: readonly PendingTurnEntry[] | null | undefined,
	connected: boolean,
): SendAction {
	return sendQueueGate({
		statusType: conversation.status.type,
		capabilities: conversation.capabilities,
		hasPendingSend: ownPendingSend(pendingMutations),
		connected,
		recovery: nativeSendQueueRecovery(conversation),
		policy: NATIVE_SEND_QUEUE_POLICY,
	}).action;
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
