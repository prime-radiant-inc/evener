// The one JS-side read of the reduced-motion preference. Every other consumer
// in the app is a CSS module's @media (prefers-reduced-motion: reduce) rule
// (widgets/popover, widgets/sheet, ...), which jsdom cannot evaluate - the
// hold-hints overlay mirrors the same preference into a data attribute so its
// instant (no-fade) path is observable and pinned in tests
// (HoldHints.test.tsx). The CSS @media rule stays the first paint's source of
// truth; this hook keeps the attribute in sync, including later changes.

import { useEffect, useState } from "react";

const REDUCED_MOTION_QUERY = "(prefers-reduced-motion: reduce)";

function readPreference(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return false;
  return window.matchMedia(REDUCED_MOTION_QUERY).matches;
}

export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(readPreference);
  useEffect(() => {
    // Guarded like stores/prefs.ts's own matchMedia reads: this project's
    // jsdom has no matchMedia at all, and the no-op path degrades to the
    // initial (false) value.
    if (typeof window.matchMedia !== "function") return undefined;
    const list = window.matchMedia(REDUCED_MOTION_QUERY);
    const onChange = (event: MediaQueryListEvent) => setReduced(event.matches);
    onChange({ matches: list.matches } as MediaQueryListEvent);
    list.addEventListener("change", onChange);
    return () => list.removeEventListener("change", onChange);
  }, []);
  return reduced;
}
