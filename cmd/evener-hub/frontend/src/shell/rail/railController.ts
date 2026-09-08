// railController is the rail's imperative reveal seam: the palette's /project
// command (T3) calls revealSessionInRail(ref) to expand the session's project
// section and scroll it into view, without the palette reaching into the rail's
// internal React state.
//
// The mounted RailHost registers a handler here (setRailRevealHandler) and
// clears it on unmount. RailHost owns the reveal-first step (opening the ☰
// overlay drawer when the rail is collapsed); the mounted <Rail/> owns the
// expand + scroll.
//
// The rail host now arrives behind a lazy chunk (shell/rail/index.tsx), so a
// reveal can fire while no handler is registered yet. The latest such reveal
// waits here and is delivered to the next handler that registers - a
// fire-and-forget palette command must not silently drop because the chunk
// was still in flight. Only the latest waits: each reveal supersedes the
// last, matching the handler-registration rule that the most recent mount
// wins. Clearing the handler (RailHost's unmount cleanup) drops a waiting
// reveal with it: the host that would have consumed it is gone, and firing it
// on a later, unrelated mount would expand a section the user no longer asked
// about. When no rail is mounted at all and none is on its way, a reveal is
// still a safe no-op. Singleton module state, same shape as the palette
// controller (T1).
export type RailRevealHandler = (ref: string) => void;

let handler: RailRevealHandler | null = null;
let pendingReveal: string | null = null;

export function setRailRevealHandler(next: RailRevealHandler | null): void {
  handler = next;
  if (next === null) {
    pendingReveal = null;
    return;
  }
  if (pendingReveal !== null) {
    const queued = pendingReveal;
    pendingReveal = null;
    next(queued);
  }
}

export function revealSessionInRail(ref: string): void {
  if (handler !== null) {
    handler(ref);
    return;
  }
  pendingReveal = ref;
}
