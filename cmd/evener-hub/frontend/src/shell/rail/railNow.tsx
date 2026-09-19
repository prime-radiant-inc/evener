// railNow.tsx owns the rail's ticking clock. A row's relative "last update"
// stamp is a function of wall-clock time AND an anchor timestamp on the
// summary, never of the summary alone: an idle session stops producing
// navigation data, so a label derived only when a new summary arrives freezes
// at whatever it read then (the reported "stays 'now' until a page refresh").
// This module supplies the missing half - the clock.
//
// The provider is a CHILDREN-PASSING component so its per-tick state lives
// here: React bails out of re-rendering the (referentially unchanged) rail
// subtree, and only the leaves that read RailNowContext re-render. The timer
// itself is panes/session/liveness.ts's shared useNowTick/NOW_TICK_MS, not a
// second copy of it. The CONTEXT stays rail-local, the way
// chrome/treeNow.ts keeps its own: the rail and the activity tree tick
// concurrently over different subtrees, so sharing one context would couple
// two independent clocks.
import { createContext, type ReactNode, useContext } from "react";
import { NOW_TICK_MS, useNowTick } from "../../panes/session/liveness";

// The rail's stamps are minute-granular and share the session pane's liveness
// cadence, so one interval value governs both surfaces.
export const RAIL_NOW_TICK_MS = NOW_TICK_MS;

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
  const now = useNowTick(RAIL_NOW_TICK_MS);
  return <RailNowContext.Provider value={now}>{children}</RailNowContext.Provider>;
}
