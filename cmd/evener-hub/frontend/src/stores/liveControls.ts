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
import { threadsStore } from "./threads";

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
