import { sessionControls } from "@evener/appwire-client";
import type { MobileConversation } from "../../mobile/src/conversation/model";

/** The slice of a conversation every control decision reads. */
export type ControlsSource = Pick<MobileConversation, "status" | "capabilities"> & {
  queue: Pick<MobileConversation["queue"], "depth" | "revision">;
};

/**
 * What this conversation may be asked to do now: the SDK's sessionControls
 * (@evener/appwire-client's submitRouting module) over the conversation's status,
 * capabilities and queue depth. Every native affordance and submission reads
 * this rather than a raw capability flag: the hub advertises steer as harness
 * support alone, so the status (and, for a drain, the queue a Stop parked) is
 * the client's to apply.
 */
export function conversationControls(conversation: ControlsSource) {
  return sessionControls(conversation.status, conversation.capabilities, conversation.queue.depth);
}

/** Whether anything can be composed at all: some action, or a goal to set. */
export function canComposeFor(conversation: ControlsSource): boolean {
  const controls = conversationControls(conversation);
  return controls.send || controls.steer || controls.queue || conversation.capabilities.goal === true;
}

/** The queue sheet's steering affordance: whether promote/drain are offered and how they read. */
export function queueSheetPresentation(conversation: ControlsSource) {
  const controls = conversationControls(conversation);
  return {
    canRun: controls.drain,
    runLabel: "Use as steering",
    runAllLabel: "Use all as steering",
    explanation: controls.drain
      ? "Messages run in order. Use steering to bring one into the current turn."
      : "Messages run in order. Your next message runs first, then the queue.",
  };
}
