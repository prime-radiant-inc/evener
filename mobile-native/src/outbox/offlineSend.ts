// The durable request for a message sent while offline (spec 8.5, ruling
// 12). It is fenced to the session instance the phone last saw: the fence the
// conversation service computed on its last read of the session (the read's
// instance, or the thread id when it names none; mobile/src/services/
// conversation.ts, where it computes readInstanceId), the same value an online
// send carries, so a session that restarted meanwhile refuses it and its
// ghost says so. This module adds no fallback of its own.
import type { InputItem } from "@evener/appwire-client";
import type { ConversationMutationRequest } from "../../../mobile/src/state/conversationMutation";

export interface OfflineTarget {
	hubId: string;
	ref: string;
	threadId: string;
	/** The conversation's instanceId, which every read sets. */
	instanceId: string;
}

export function offlineRequest(
	target: OfflineTarget,
	action: "send" | "queue" | "resume",
	input: InputItem[],
): ConversationMutationRequest {
	return {
		kind: action === "queue" ? "queue" : "send",
		hubId: target.hubId,
		targetRef: target.ref,
		threadId: target.threadId,
		instanceId: target.instanceId,
		input,
	};
}
