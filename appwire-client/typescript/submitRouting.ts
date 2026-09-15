// Pure send/steer/queue/drain routing decisions (parity-m5-composer.md §A,
// kata 0bq1) - framework-agnostic on purpose so Composer.tsx's own
// click/keydown handlers stay thin dispatchers over these, and every branch
// is unit-testable without mounting anything.
import type { SendQueueAvailability } from "./sendQueueAvailability";

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
//   - otherwise (empty queue, no attachments): text OR staged skill
//     selections route to classic turn/steer (a selection-only input is
//     content, the same as it is for Send and draft persistence); nothing
//     staged at all is a no-op (focus the textarea, no request).
export function decideSteerRoute(opts: {
  hasText: boolean;
  hasAttachments: boolean;
  hasSkills?: boolean;
  queueDepth: number;
}): SteerRoute {
  const queueEmpty = opts.queueDepth <= 0;
  if (!queueEmpty || opts.hasAttachments) return "drain";
  if (opts.hasText || opts.hasSkills) return "steer";
  return "none";
}

// isTurnActive is the interrupt/steer "busy" predicate: is this session
// working right now. It reads the thread status and nothing else, because the
// status is the daemon's own answer to that question and the one the hub
// derives the steer and interrupt capabilities from (server/appwire_runtime.go
// appCapabilitiesLocked: `active := status == active`).
//
// It deliberately does NOT also require ThreadModel.activeTurnId. That id is
// the transcript's bookkeeping of which turn row is open, and the projector
// closes one row before it opens the next: when the daemon runs consecutive
// turns inside one input (a queued message, a notification turn, a goal
// continuation, a drained steering carrier) openTurn emits turn/completed then
// turn/started, the hub relays each as its own message, and the status never
// leaves active. Between those two frames the session is as busy as it was a
// moment before; a predicate that read the id went false there, which took
// Steer off the composer for a frame at every inline turn boundary and passed
// the skill guard's turn-end barrier mid-input (issue #1330). The daemon says
// idle with a thread/status/changed frame, and that frame always follows the
// closing turn/completed, so the status alone is never late in that direction.
//
// It is still not deriveSendQueueAvailability's gate: that table folds the
// caller's own pending-send flag (its tier 6) in, which is a routing concern.
export function isTurnActive(statusType: string): boolean {
  return statusType === "active";
}
