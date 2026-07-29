// Public surface of this stream's queue module (Wave 5 T3): the queue strip
// component plus the optimistic-pending machinery it shares with whatever
// wires up plain send/steer submissions (T2's composer core, at the wave
// integration merge - see each export's own doc comment for the exact
// integration-seam contract).

export type { PendingMethod, PendingTurnEntry, SubmitWithPendingTrackingOptions } from "./pendingTurnsStore";
export { submitWithPendingTracking, usePendingTurnEntries } from "./pendingTurnsStore";
export type { QueueStripProps } from "./QueueStrip";
export { QueueStrip } from "./QueueStrip";
// queueEntryPreviewText is this stream's own queue-row display helper -
// exported so a future presentational chip for the send/steer/drain pending
// methods (which render in the transcript/conversation pane, outside this
// manifest - see pendingTurnsStore.ts's own doc comment) can reuse the same
// text-or-image-placeholder computation this module already uses for its
// own queue rows, rather than re-deriving it.
export { queueEntryPreviewText } from "./queueDisplay";
