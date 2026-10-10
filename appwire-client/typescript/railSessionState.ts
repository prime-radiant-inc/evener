// The humanized wire state used by session summaries, including the web
// rail's title HoverCard: the same wire state vocabulary its cadenceStateFor
// reads, worded for a person rather than mapped to a broad Cadence family.
// Pure functions, no imports.
//
// "awaiting" itself splits on askPending, as hubapi.StateWord (hubapi/
// attention.go) does for the TUI and the older web surface: "Question
// waiting" for a pending question, "Needs you" for a turn that ended on
// needs_response (#4093). A plain reply rests idle and never reads awaiting.
// Return values are lowercase so each presentation surface can apply its own
// casing.
//
// "warning" gets its own word for the same reason (kata 59mx): StateWord
// already gives it a dedicated "Warning", distinct from either awaiting
// band. Sharing Cadence's "needs-you" dot family is still correct: only the
// dot family is shared by design, never the word.
//
// A pending approval (approvalWaiting below) leads over every state but a
// failure: the escalation blocks its turn mid-tool, so the session keeps
// reporting "active", and "working" would hide the one thing it needs from a
// person.
export function humanizeState(wireState: string, askPending: boolean, approvalPending = false): string {
  if (approvalWaiting(wireState, approvalPending)) return "approval waiting";
  switch (wireState) {
    case "active":
      return "working";
    case "awaiting":
      return needsResponseRest(wireState, askPending) ? "needs you" : "question waiting";
    case "restartRequired":
      return "restart required";
    case "warning":
      return "warning";
    case "errored":
      return "failed";
    case "ended":
      return "ended";
    default: // "idle", "notLoaded", "", and any future/unknown value
      return "idle";
  }
}

// needsResponseRest says a session rests awaiting with no pending question:
// its turn ended on needs_response, asking for a reply. The hub's
// hubapi.NeedsResponse draws the same line.
export function needsResponseRest(wireState: string, askPending: boolean): boolean {
  return wireState === "awaiting" && !askPending;
}

// approvalWaiting says a session is waiting on a person to allow or deny a
// sandbox escalation. Its wire state cannot say so: the escalation blocks the
// turn mid-tool, so the state stays "active". A failure outranks the
// approval, the line the hub draws too (hubapi.AttentionState).
export function approvalWaiting(wireState: string, approvalPending: boolean): boolean {
  return approvalPending && wireState !== "errored";
}
