import { sessionControls } from "@evener/appwire-client";
import type { QueueState } from "@evener/appwire-client";
import type { MobileConversation } from "./projectedRows";

/** The slice of a conversation every control decision reads. Capabilities may
 * be partial: sessionControls reads only the four it gates on, and the
 * composer's command tests describe a session by just those. */
export type ControlsSource = Pick<MobileConversation, "status"> & {
	capabilities: Partial<MobileConversation["capabilities"]>;
	queue: Pick<QueueState, "depth" | "revision"> | null;
};

/**
 * What this conversation may be asked to do now: the SDK's sessionControls
 * (@evener/appwire-client's submitRouting module) over the conversation's status,
 * capabilities and queue depth. Every native affordance and submission reads
 * this rather than a raw capability flag: the hub advertises steer, interrupt
 * and queue as harness support alone, so the status (and, for a drain, the
 * queue a Stop parked) is the client's to apply.
 */
export function conversationControls(conversation: ControlsSource) {
	return sessionControls(conversation.status.type, conversation.capabilities, conversation.queue?.depth ?? 0);
}

/** Whether anything can be composed at all: some action, or a goal to set. */
export function canComposeFor(conversation: ControlsSource): boolean {
	const controls = conversationControls(conversation);
	return controls.send || controls.steer || controls.queue || conversation.capabilities.goal === true;
}

/** The presses on queued messages that steer the running turn with queued
 * text. Cancel only takes a message out of the queue, so it is never refused
 * here. */
export type QueueAction = "promote" | "drainAll";

/**
 * Why a queued-message press must be refused right now, or null when it may
 * proceed. Read at press time against the live conversation, not the one the
 * ghost rendered with: a status that flipped in between (awaiting, idle with
 * the queue gone) is caught here with the control's own reason. Drain-all sends
 * the queue and nothing else, so its control is drainQueue (the drain rule
 * plus a queue to drain); a promote names one row and keeps the drain's.
 */
export function queueActionRefusal(conversation: ControlsSource, action: QueueAction): string | null {
	const controls = conversationControls(conversation);
	const control = action === "drainAll" ? "drainQueue" : "drain";
	return controls[control] ? null : (controls.reason[control] ?? "Steer is not available for this session");
}
