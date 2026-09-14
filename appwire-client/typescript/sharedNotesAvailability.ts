import type { ThreadModel } from "./model";

// Reading saved notes does not require a live session. Editing is gated
// separately; an unknown or unsupported capability never opens a blank reader.
export function canReadSharedNotes(model: ThreadModel | undefined): boolean {
  return model?.capabilities.sharedNotes === true;
}
