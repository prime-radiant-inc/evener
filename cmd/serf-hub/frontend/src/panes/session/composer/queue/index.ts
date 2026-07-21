// Public surface of this stream's queue module (Wave 5 T3): the queue strip
// component plus the optimistic-pending machinery it shares with whatever
// wires up plain send/steer submissions (T2's composer core, at the wave
// integration merge - see each export's own doc comment for the exact
// integration-seam contract).

export type { PendingMethod, PendingTurnEntry, SubmitWithPendingTrackingOptions } from "./pendingTurnsStore";
export { PENDING_TIMEOUT_MS, submitWithPendingTracking, usePendingTurnEntries } from "./pendingTurnsStore";
export type { QueueStripProps } from "./QueueStrip";
export { QueueStrip } from "./QueueStrip";
