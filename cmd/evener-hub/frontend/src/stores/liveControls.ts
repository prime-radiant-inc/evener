// The web's one derivation of what a session may be asked to do, and its
// press-time reading of it.
//
// Every control surface (the composer's Stop/Steer/Send, the queue strip's
// drain and promote, the palette's session commands) derives a session's
// controls from the SDK's sessionControls over the wire's status, the harness's
// capabilities and the queue depth: controlsFor. A render offers a control from
// the model it rendered with; the press that follows can land after a status
// frame that render never saw, so a press handler asks pressRefusal, which
// re-derives the controls from the store at the moment the press lands. The
// palette's runWhenAvailable and native's requireControl are the same rule at
// their own boundaries.

import { NO_ACTIVE_TURN, type SessionControls, sessionControls, type ThreadModel } from "@evener/appwire-client";
import { isLocalRecoveryFenced, threadsStore } from "./threads";

export type SessionControl = keyof SessionControls["reason"];

export function controlsFor(model: ThreadModel): SessionControls {
  return sessionControls(model.status.type, model.capabilities, model.queue?.depth ?? 0);
}

// The session's model as the store holds it now, not as a component rendered
// it: for the handlers that act on a press.
export function liveThreadModel(ref: string): ThreadModel | undefined {
  return threadsStore.getState().threads.get(ref);
}

// Why a press on `control` is refused against the session's live controls, or
// undefined when it may run. A session the store no longer holds has no turn
// to act on.
export function pressRefusal(ref: string, control: SessionControl): string | undefined {
  const model = liveThreadModel(ref);
  if (!model) return NO_ACTIVE_TURN;
  return controlsFor(model).reason[control];
}

// The local recovery fence's shared predicate lives in threads.ts - beside
// the obligation state it reads, and where the store's own mutation admission
// uses it (threads.ts cannot import this module without an import cycle).
// Re-exported here so the control surfaces keep one import site for the fence
// and its press-time reading; Composer.tsx's availabilityFor and QueueStrip's
// press handlers derive from this one predicate (the composer module cannot
// lend QueueStrip its copy: Composer imports QueueStrip), and each call site
// adds the shape its own surface needs.
export { isLocalRecoveryFenced };

// The same fence as a press reads it: the obligation as the store holds it
// NOW, not as the subscribing render saw it (this module's own render-vs-press
// rule - a Stop can arm the fence between the render that offered a control
// and the press that follows).
export function pressLocalRecoveryFenced(ref: string): boolean {
  return isLocalRecoveryFenced(ref, threadsStore.getState().restartBlockingObligations.has(ref));
}
