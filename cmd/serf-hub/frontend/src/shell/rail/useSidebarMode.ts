// useSidebarMode resolves the persisted sidebarMode preference (auto/pane/rail)
// plus the desktop viewport into the one thing RailHost actually needs: is the
// inline rail hidden behind the ☰ overlay drawer right now?
//
// Semantics are fixed by the shipped help copy (theme.tsx's Sidebar mode help):
//   pane  -> always expanded
//   rail  -> always collapsed (labelled "Collapsed"; reopen via the ☰ chip)
//   auto  -> docked across the whole desktop range (>=900px); only `rail`
//            collapses. 900px is the first non-mobile pixel (useIsMobile.ts's
//            own mobile/stack threshold is <=899px), so the moment the layout
//            is a desktop one at all, auto keeps the rail docked alongside the
//            workspace like an app - it never auto-tucks on a narrow desktop
//            (3w2p). Below 900px the whole host swaps out for StackHost, so
//            this query effectively only ever answers true on desktop.
import { useEffect, useState } from "react";
import { type SidebarModePref, usePrefsStore } from "../../stores/prefs";

const WIDE_QUERY = "(min-width: 900px)";

// SSR-safe, and safe under jsdom (which has no matchMedia): default to "wide",
// so auto resolves to expanded - the same not-collapsed default a desktop user
// gets, matching useIsMobile.ts's own no-matchMedia rationale.
function isWideViewport(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return true;
  return window.matchMedia(WIDE_QUERY).matches;
}

export interface ResolvedSidebar {
  mode: SidebarModePref;
  /** True when the inline rail is hidden and reachable only through the ☰
   * overlay drawer: always in `rail` mode. In `auto` the rail docks across the
   * whole desktop range (>=900px) and only collapses below it - a width that,
   * being mobile, has already swapped this host out for StackHost. */
  collapsed: boolean;
}

export function useSidebarMode(): ResolvedSidebar {
  const mode = usePrefsStore((s) => s.sidebarMode);
  const [wide, setWide] = useState(isWideViewport);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return undefined;
    const mql = window.matchMedia(WIDE_QUERY);
    // Resync against the current viewport before wiring the listener, same
    // render-time-vs-effect-time reasoning as useIsMobile.ts.
    setWide(mql.matches);
    const onChange = (event: MediaQueryListEvent) => setWide(event.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  const collapsed = mode === "rail" || (mode === "auto" && !wide);
  return { mode, collapsed };
}
