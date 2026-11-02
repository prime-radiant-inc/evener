// The one place a session's wire state, plus the facts around it, becomes the
// attention class every client shows: the phone's Board marks and state line,
// the web rail's dot family and the notification level. The hub states the
// approval half of the rule in hubapi.AttentionState; testdata/attention
// holds the cases both sides run. Pure functions, no imports.

export type AttentionClass =
  | "failed"
  | "question"
  | "needsYou"
  | "approval"
  | "warning"
  | "restartNeeded"
  | "working"
  | "idle"
  | "shutDown";

export interface AttentionFacts {
  askPending?: boolean;
  approvalPending?: boolean;
  // A live child is running (the phone Board counts running subagents).
  runningChildren?: boolean;
  // The row's source cannot be reached, whatever state it last reported.
  offline?: boolean;
}

// normalizeWireState maps the raw ThreadStatus.type vocabulary onto the
// navigation summary's (hubcore.NormalizeState): closed->ended,
// systemError->errored. A normalized value passes through unchanged.
export function normalizeWireState(wireState: string): string {
  switch (wireState) {
    case "closed":
      return "ended";
    case "systemError":
      return "errored";
    default:
      return wireState;
  }
}

// These states outrank a pending approval: a failure, a session that needs a
// restart, and one that has ended or never loaded cannot be answered by an
// approval. The stale escalation never masks them.
const OUTRANKS_APPROVAL: ReadonlySet<string> = new Set(["errored", "restartRequired", "ended", "notLoaded"]);

// approvalWaiting says a session is waiting on a person to allow or deny a
// sandbox escalation. Its wire state cannot say so: the escalation blocks the
// turn mid-tool, so the state stays "active". It mirrors hubapi.AttentionState.
export function approvalWaiting(wireState: string, approvalPending: boolean): boolean {
  return approvalPending && !OUTRANKS_APPROVAL.has(normalizeWireState(wireState));
}

// attentionState is the precedence order: offline, failed, restart needed,
// shut down, then a blocking warning, a question, an approval, awaiting,
// working, a nonblocking warning, idle. Only a nonblocking warning yields to
// running children; a failed child never decides the parent's class.
export function attentionState(wireState: string, facts: AttentionFacts = {}): AttentionClass {
  if (facts.offline) return "shutDown";
  const state = normalizeWireState(wireState);
  const decisive = decisiveClass(state);
  if (decisive && (decisive !== "warning" || facts.askPending || facts.approvalPending)) return decisive;
  if (state === "awaiting" && facts.askPending) return "question";
  if (facts.approvalPending) return "approval";
  // Awaiting without a question is a turn that ended on needs_response: it
  // waits on a person like a question does, ahead of any running work.
  if (state === "awaiting") return "needsYou";
  if (state === "active") return "working";
  if (facts.runningChildren) return "working";
  return decisive ?? "idle";
}

function decisiveClass(state: string): AttentionClass | null {
  switch (state) {
    case "errored":
      return "failed";
    case "restartRequired":
      return "restartNeeded";
    case "warning":
      return "warning";
    case "ended":
    case "notLoaded":
      return "shutDown";
  }
  return null;
}

// attentionNeedsYou says the class belongs to the needs-you family: someone
// has to look, as opposed to working, idle, shut down or failed.
export function attentionNeedsYou(attention: AttentionClass): boolean {
  switch (attention) {
    case "question":
    case "needsYou":
    case "approval":
    case "warning":
    case "restartNeeded":
      return true;
    default:
      return false;
  }
}
