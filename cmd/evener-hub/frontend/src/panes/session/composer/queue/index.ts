// Public surface of the queue module: the optimistic-pending machinery the
// composer's submissions and the transcript's pending rows share. The queue
// strip component itself is retired - every undelivered user message renders
// as a transcript row in the live-edge trailing region
// (transcript/messages/PendingRowStack.tsx).

export type { PendingMethod, PendingTurnEntry, SubmitWithPendingTrackingOptions } from "./pendingTurnsStore";
export { pendingTurnEntries, submitWithPendingTracking, usePendingTurnEntries } from "./pendingTurnsStore";
export { queueEntryPreviewText } from "./queueDisplay";
