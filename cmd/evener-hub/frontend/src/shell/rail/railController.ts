// railController is the rail's imperative reveal seam: the palette's /project
// command (T3) calls revealSessionInRail(ref) to expand the session's project
// section and scroll it into view, without the palette reaching into the rail's
// internal React state.
//
// The mounted RailHost registers a handler here (setRailRevealHandler) and
// clears it on unmount. RailHost owns the reveal-first step (opening the ☰
// overlay drawer when the rail is collapsed); the mounted <Rail/> owns the
// expand + scroll. When no rail is mounted at all, revealSessionInRail is a
// safe no-op. Singleton module state, same shape as the palette controller (T1).
export type RailRevealHandler = (ref: string) => void;

let handler: RailRevealHandler | null = null;

export function setRailRevealHandler(next: RailRevealHandler | null): void {
  handler = next;
}

export function revealSessionInRail(ref: string): void {
  handler?.(ref);
}
