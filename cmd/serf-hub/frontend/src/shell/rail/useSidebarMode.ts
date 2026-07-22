// useSidebarMode resolves the persisted sidebarMode preference (auto/pane/rail)
// plus the desktop viewport into the one thing RailHost actually needs: is the
// inline rail hidden behind the ☰ overlay drawer right now?
//
// Semantics are fixed by the shipped Wave-7 help copy (theme.tsx:103-104):
//   pane  -> always expanded
//   rail  -> always collapsed (labelled "Collapsed"; reopen via the ☰ chip)
//   auto  -> responsive: expanded at/above 1200px, collapsed below it
//
// The 1200px desktop breakpoint is deliberately distinct from useIsMobile's
// 900px mobile/stack breakpoint: auto keeps the rail alongside the workspace on
// a wide desktop and tucks it away on a narrow one, both still desktop layouts
// (mobile swaps the whole host out for StackHost well before 900px).
import { useEffect, useState } from "react";
import { type SidebarModePref, usePrefsStore } from "../../stores/prefs";

const WIDE_QUERY = "(min-width: 1200px)";

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
   * overlay drawer: always in `rail` mode, and in `auto` below 1200px. */
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
