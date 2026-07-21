// Pure send/steer/queue/drain routing decisions (parity-m5-composer.md §A,
// kata 0bq1) - framework-agnostic on purpose so Composer.tsx's own
// click/keydown handlers stay thin dispatchers over these, and every branch
// is unit-testable without mounting anything.
import type { SendQueueAvailability } from "../../../protocol/sendQueueAvailability";

export type SubmitRoute = "send" | "queue" | "none";

// decideSubmitRoute: submit is a no-op when the composer is empty of both
// text and attachments (parity-m5-composer.md §A); otherwise queue-mode
// takes priority when available (the daemon is mid-turn and Send is
// deliberately routed to the queue), else plain send, else neither
// capability is available and there is nothing to do. deriveSendQueueAvailability's
// three fixed outcomes never actually produce canSend===canQueue===true
// together, but this checks both explicitly (queue first) rather than
// assuming that exclusivity blindly.
export function decideSubmitRoute(opts: { hasContent: boolean; availability: SendQueueAvailability }): SubmitRoute {
  if (!opts.hasContent) return "none";
  if (opts.availability.canQueue) return "queue";
  if (opts.availability.canSend) return "send";
  return "none";
}

export type SteerRoute = "steer" | "drain" | "none";

// decideSteerRoute forks the Steer button/Shift+Enter action on composer +
// queue state (kata 0bq1):
//   - a non-empty queue, OR any staged attachments, ALWAYS routes to
//     turn/drainAsSteer - regardless of the textarea's own text content (an
//     empty textarea with a non-empty queue still drains).
//   - otherwise (empty queue, no attachments): text routes to classic
//     turn/steer; no text is a no-op (focus the textarea, no request).
export function decideSteerRoute(opts: { hasText: boolean; hasAttachments: boolean; queueDepth: number }): SteerRoute {
  const queueEmpty = opts.queueDepth <= 0;
  if (!queueEmpty || opts.hasAttachments) return "drain";
  if (opts.hasText) return "steer";
  return "none";
}

// isTurnActive is the interrupt/steer/model-switch "busy" predicate
// (thread-state.js's legacy SerfThreadState.isBusy), deliberately NOT the
// same gate as deriveSendQueueAvailability (send/queue key off statusType
// alone - see that module's own comment on why activeTurnId must stay out
// of it). Interrupt and steer both need the stronger check: a turn is only
// truly "in flight" once both the status flip AND the turn/started
// notification (which populates activeTurnId) have landed.
export function isTurnActive(statusType: string, activeTurnId: string | undefined): boolean {
  return statusType === "active" && !!activeTurnId;
}
