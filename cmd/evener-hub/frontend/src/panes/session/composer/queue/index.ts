// Public surface of this stream's queue module: the optimistic-pending
// machinery shared with the session's pending stack.

export type { PendingMethod, PendingTurnEntry, SubmitWithPendingTrackingOptions } from "./pendingTurnsStore";
export { pendingTurnEntries, submitWithPendingTracking, usePendingTurnEntries } from "./pendingTurnsStore";
