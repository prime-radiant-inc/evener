import type { ThreadModel } from "./model";

// Reading saved notes does not require a live session. Editing is gated
// separately; an unknown or unsupported capability never opens a blank reader.
export function canReadSharedNotes(model: ThreadModel | undefined): boolean {
  return model?.capabilities.sharedNotes === true;
}

// Shared notes stay readable while a session is under the hub's recovery fence
// (resumeRequired), so the SharedNotes capability and status alone admit edits
// the hub refuses until an explicit resume. The fence closes the write gate
// here; an edit made before the fence arrived stays in the draft store and an
// already-queued mutation stays in the durable outbox, both for retry after
// resume. Read rendering (canReadSharedNotes) is deliberately unaffected.
export function canWriteSharedNotes(
  model: Pick<ThreadModel, "status" | "resumeRequired" | "capabilities"> | undefined,
): boolean {
  return (
    model?.capabilities.sharedNotes === true &&
    model.resumeRequired !== true &&
    !["ended", "closed", "notLoaded", "restartRequired"].includes(model.status.type)
  );
}
