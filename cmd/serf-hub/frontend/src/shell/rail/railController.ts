// railController is the rail's imperative reveal seam: the palette's
// /project command (T3) calls revealSessionInRail(ref) to expand the
// session's project section and scroll it into view, without the palette
// reaching into the rail's internal state.
//
// T1 ships a no-op-safe stub; T5 fills the body (lifting the rail's
// expandedOverrides/scroll to an imperative handle, no-op-safe when the rail
// is collapsed - reveal first).
export function revealSessionInRail(_ref: string): void {
  // T5 fills this.
}
