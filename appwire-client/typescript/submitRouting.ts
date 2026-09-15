// Pure send/steer/queue/drain routing decisions (parity-m5-composer.md §A,
// kata 0bq1) - framework-agnostic on purpose so Composer.tsx's own
// click/keydown handlers stay thin dispatchers over these, and every branch
// is unit-testable without mounting anything.
import type { SendQueueAvailability } from "./sendQueueAvailability";
import type { ThreadCapabilities } from "./types.gen";

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

// canSteer gates turn/steer: the composer's Steer button and its keybinding's
// steer route. The session is working (isTurnActive) and the harness can
// steer. The hub's `steer` capability is harness support alone
// (server/appwire_runtime.go appCapabilitiesLocked: it does not fold the
// status in, so an idle client can still tell a harness that steers from one
// that cannot); the status is the client's to apply, and for a plain steer
// it is active, because with nothing running Send is the route.
export function canSteer(statusType: string, capabilities: Pick<ThreadCapabilities, "steer">): boolean {
  return isTurnActive(statusType) && capabilities.steer === true;
}

// canDrainQueue gates turn/drainAsSteer and turn/promoteQueuedAsSteer: the
// queue strip's "Steer queue now" and per-row "Steer now", the composer
// keybinding's drain route and the palette's /drain-as-steer. The harness can
// steer, and either a turn is running or the queue is parked: a Stop parks
// the daemon's queue (agent/session_client_mutation.go QueueHeld), the
// entries stay and the session reports idle with a non-empty queue (a queue
// that is not parked upgrades idle to active, agent/session_state.go
// WireState), and a drain or promote is one of the runs that releases it
// (agent/session_client_mutation_queue.go; neither has a status
// precondition). Idle only: awaiting + a queue is the ask boundary, and that
// queue runs next on its own. One predicate for every drain/promote
// affordance, so a harness that cannot steer is never sent one.
export function canDrainQueue(
  statusType: string,
  capabilities: Pick<ThreadCapabilities, "steer">,
  queueDepth: number,
): boolean {
  if (capabilities.steer !== true) return false;
  return isTurnActive(statusType) || (statusType === "idle" && queueDepth > 0);
}
