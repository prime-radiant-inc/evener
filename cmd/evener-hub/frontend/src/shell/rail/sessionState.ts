// The humanized wire state a row's second line leads with (§2.3) - the same
// wire state vocabulary cadenceStateFor reads, worded for a person rather
// than mapped to a Cadence family.
//
// "awaiting" itself splits on askPending: hubapi.StateWord (hubapi/
// attention.go, Track A §2 ask-tiering) already draws this same line for the
// TUI and the older web surface - "Question waiting" when the agent is
// genuinely blocked on an answer, "Your move" when a turn simply ended with
// nothing further queued - because those are different urgencies wearing the
// identical amber dot. This rail's own row never read askPending before,
// so every "awaiting" row rendered as the same generic "waiting on you" -
// a person scanning the list for the one session that's actually blocked on
// them had to open every amber row to find out which. Lowercased to match
// this line's existing casing ("working"/"failed"/"idle"), not the Go
// vocabulary's sentence case verbatim.
//
// "warning" gets its own word for the same reason (kata 59mx): StateWord
// already gives it a dedicated "Warning", distinct from either awaiting
// band, so a warning row reading as generic "waiting on you" was this
// gloss never having read that vocabulary for this state either - the same
// gap ask_pending closed for "awaiting" above. Sharing Cadence's "needs-you"
// dot family (cadenceStateFor) is still correct: that comment's own text
// says only the dot family is shared by design, never the word.
export function humanizeState(wireState: string, askPending: boolean): string {
  switch (wireState) {
    case "active":
      return "working";
    case "awaiting":
      return askPending ? "question waiting" : "your move";
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
