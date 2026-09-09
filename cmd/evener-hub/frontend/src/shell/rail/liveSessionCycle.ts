// Live-session navigation for the session.liveNext/session.livePrevious
// keybinding actions: Alt+Shift+ArrowRight/Left move across the rail's LIVE
// section in server order, wrapping at both ends. The action handlers
// (AppShell) read selectLiveRows and call adjacentLiveSessionRef; the
// selection semantics live here, pure, so they are unit-testable without the
// shell. This is needsYouCycle.ts's nextNeedsYouRef precedent made
// bidirectional, including its "current not in the cycle list" rule: next
// lands on the FIRST live session, previous on the LAST.

export type LiveSessionCycleDirection = "next" | "previous";

export function adjacentLiveSessionRef(
  refs: readonly string[],
  currentRef: string | null,
  direction: LiveSessionCycleDirection,
): string | null {
  if (refs.length === 0) return null;
  const index = currentRef === null ? -1 : refs.indexOf(currentRef);
  if (index < 0) {
    return (direction === "next" ? refs[0] : refs[refs.length - 1]) ?? null;
  }
  const step = direction === "next" ? 1 : -1;
  return refs[(index + step + refs.length) % refs.length] ?? null;
}
