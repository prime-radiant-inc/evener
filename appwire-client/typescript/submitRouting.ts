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

// Session controls: what may this session be asked to do right now.
//
// One derivation, read by the composer (Stop, Steer, the Shift+Enter routes),
// the queue strip (Steer queue now, Steer now) and the palette (/steer,
// /queue, /drain-as-steer). Each surface reads a field; none composes a
// predicate of its own, so a harness that cannot steer is never sent a
// turn/steer, turn/drainAsSteer or turn/promoteQueuedAsSteer it would answer
// Unavailable, from any of them.
//
// The status is the daemon's answer to "is this session working". It is read
// alone, never with ThreadModel.activeTurnId: that id is the transcript's
// bookkeeping of which turn row is open, and the projector closes one row
// before it opens the next. When the daemon runs consecutive turns inside one
// input (a queued message, a notification turn, a goal continuation, a
// drained steering carrier) openTurn emits turn/completed then turn/started,
// the hub relays each as its own message, and the status never leaves active.
// A predicate that read the id went false between those frames, which took
// Steer off the composer at every inline turn boundary and passed the skill
// guard's turn-end barrier mid-input (#1330). The daemon says idle with a
// thread/status/changed frame that always follows the closing turn/completed,
// so the status alone is never late in that direction.
//
// Capabilities are the harness's. The hub's `steer`, `interrupt` and `queue`
// are harness support alone (server/appwire_runtime.go appCapabilitiesLocked;
// none folds the status in, so an idle client can still tell a harness that
// supports an action from one that cannot, #1363, #1375). This derivation
// applies the status for all of them, so every action below reads one rule and
// no caller has to know which flags arrive pre-gated.
//
//   stop   turn/interrupt: active && interrupt.
//   steer  turn/steer: active && steer. With nothing running Send is the
//          route; the daemon would accept a steer while idle but it lands in
//          the next turn.
//   drain  turn/drainAsSteer and turn/promoteQueuedAsSteer: steer && (active
//          || idle with a non-empty queue). A Stop parks the daemon's queue
//          (agent/session_client_mutation.go QueueHeld); the entries stay and
//          the session reports idle with a queue, which an unparked queue
//          never does (agent/session_state.go WireState upgrades idle to
//          active on pending queued work). A drain or promote is one of the
//          runs that releases it (agent/session_client_mutation_queue.go,
//          no status precondition). Idle only: awaiting with a queue is the
//          ask boundary and that queue runs next on its own.
//   queue  turn/queue: active && queue.
//   send   turn/start: !active && send. The composer routes Send through
//          deriveSendQueueAvailability instead, which folds in its own
//          pending-send tier; this field is the plain wire rule.
//
// reason carries, for each false action, why: the harness's capability
// (STEER_UNAVAILABLE and its siblings) or the status (NO_ACTIVE_TURN).
export const NO_ACTIVE_TURN = "no active turn";
export const TURN_RUNNING = "a turn is running";
export const STEER_UNAVAILABLE = "Steer is not available for this session";
export const STOP_UNAVAILABLE = "Stop is not available for this session";
export const QUEUE_UNAVAILABLE = "Queue is not available for this session";
export const SEND_UNAVAILABLE = "Send is not available for this session";
export const QUEUE_EMPTY = "queue is empty";

export type SessionControlName = "stop" | "steer" | "drain" | "drainQueue" | "queue" | "send";

export interface SessionControls {
  stop: boolean;
  steer: boolean;
  // drain: the composer's drain, which appends what is typed before draining,
  // so it has something to send even with the queue empty. drainQueue: the
  // argless /drain-as-steer commands, which send the queue and nothing else and
  // are refused by the daemon ("queue is empty") when there is none.
  drain: boolean;
  drainQueue: boolean;
  queue: boolean;
  send: boolean;
  reason: Partial<Record<SessionControlName, string>>;
}

// Partial, because a client's capability snapshot may not carry every flag
// (native's completion registry filters on whatever the hub advertised); an
// absent flag reads as false, the same as the hub withholding it.
type ControlCapabilities = Partial<Pick<ThreadCapabilities, "steer" | "interrupt" | "queue" | "send">>;

export function sessionControls(
  statusType: string,
  capabilities: ControlCapabilities,
  queueDepth: number,
): SessionControls {
  const active = isTurnActive(statusType);
  const parked = statusType === "idle" && queueDepth > 0;
  const controls: SessionControls = {
    stop: active && capabilities.interrupt === true,
    steer: canSteer(statusType, capabilities),
    drain: canDrainQueue(statusType, capabilities, queueDepth),
    drainQueue: canDrainQueue(statusType, capabilities, queueDepth) && queueDepth > 0,
    queue: active && capabilities.queue === true,
    send: !active && capabilities.send === true,
    reason: {},
  };
  if (!controls.stop) controls.reason.stop = capabilities.interrupt === true ? NO_ACTIVE_TURN : STOP_UNAVAILABLE;
  if (!controls.steer) controls.reason.steer = capabilities.steer === true ? NO_ACTIVE_TURN : STEER_UNAVAILABLE;
  if (!controls.drain) {
    controls.reason.drain = capabilities.steer === true && !active && !parked ? NO_ACTIVE_TURN : STEER_UNAVAILABLE;
  }
  if (!controls.drainQueue) controls.reason.drainQueue = controls.reason.drain ?? QUEUE_EMPTY;
  if (!controls.queue) controls.reason.queue = capabilities.queue === true ? NO_ACTIVE_TURN : QUEUE_UNAVAILABLE;
  if (!controls.send) controls.reason.send = capabilities.send === true ? TURN_RUNNING : SEND_UNAVAILABLE;
  return controls;
}

// The internals of sessionControls, exported for the package qualification's
// smoke calls and the tests; surfaces read sessionControls.
export function isTurnActive(statusType: string): boolean {
  return statusType === "active";
}

export function canSteer(statusType: string, capabilities: Partial<Pick<ThreadCapabilities, "steer">>): boolean {
  return isTurnActive(statusType) && capabilities.steer === true;
}

export function canDrainQueue(
  statusType: string,
  capabilities: Partial<Pick<ThreadCapabilities, "steer">>,
  queueDepth: number,
): boolean {
  if (capabilities.steer !== true) return false;
  return isTurnActive(statusType) || (statusType === "idle" && queueDepth > 0);
}
