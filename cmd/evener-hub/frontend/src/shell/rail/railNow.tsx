// railNow.tsx owns the rail's ticking clock. A row's relative "last update"
// stamp is a function of wall-clock time AND an anchor timestamp on the
// summary, never of the summary alone: an idle session stops producing
// navigation data, so a label derived only when a new summary arrives freezes
// at whatever it read then (the reported "stays 'now' until a page refresh").
// This module supplies the missing half - the clock.
//
// The provider is a CHILDREN-PASSING component so its per-tick state lives
// here: React bails out of re-rendering the (referentially unchanged) rail
// subtree, and only the leaves that read RailNowContext re-render. That mirrors
// panes/session/chrome/ActivityTree.tsx's TreeTickProvider, whose comment
// states the same contract for the activity tree.
import { createContext, type ReactNode, useContext, useEffect, useState } from "react";

// The rail's labels are minute-granular ("now"/"2m"/"3h"/"5d"), so the tick
// only has to be fine enough that "now" turns into "1m" promptly. 3s matches
// the session pane's liveness cadence (panes/session/liveness.ts NOW_TICK_MS).
export const RAIL_NOW_TICK_MS = 3_000;

// null means "no live clock": a row rendered without the provider (a direct
// unit test, or any future standalone use) reads the current instant once per
// render instead of subscribing. That keeps a bare render timer-free and
// deterministic, the same contract SessionNowContext documents.
export const RailNowContext = createContext<number | null>(null);

// useRailNow reads the rail's ticking clock. A component that calls it
// re-renders on every tick, so callers must be the smallest leaf that actually
// renders a clock-derived value - never a whole row.
export function useRailNow(): number {
  return useContext(RailNowContext) ?? Date.now();
}

export function RailTickProvider({ children }: { children: ReactNode }): ReactNode {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), RAIL_NOW_TICK_MS);
    return () => clearInterval(id);
  }, []);
  return <RailNowContext.Provider value={now}>{children}</RailNowContext.Provider>;
}
