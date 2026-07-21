// The single source of truth for "is this the mobile (<900px) layout" -
// StackHost (shell/mobile/**) vs DockHost. Lives directly in shell/ (not
// shell/mobile/) because AppShell itself needs it to choose which host to
// mount (see shell/mobile/StackHost.tsx's own header comment for that
// mount contract) - a hook the host-selection call site depends on
// shouldn't live inside one of the two things it's choosing between.
import { useEffect, useState } from "react";

const MOBILE_QUERY = "(max-width: 899px)";

// SSR-safe: `window`/`window.matchMedia` may not exist at all outside a
// browser. This app has no SSR today, but the hook is written to survive
// one anyway (an explicit part of this task's scope, not speculative) -
// "not mobile" is the same safe default every consumer already gets from
// AppShell rendering DockHost unconditionally today (see that component's
// own comment on the seam this hook fills).
function isMobileViewport(): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return false;
  return window.matchMedia(MOBILE_QUERY).matches;
}

export function useIsMobile(): boolean {
  const [isMobile, setIsMobile] = useState(isMobileViewport);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return undefined;
    const mql = window.matchMedia(MOBILE_QUERY);
    // Resync against the CURRENT viewport before wiring up the listener:
    // this effect runs after the browser paints (unlike useLayoutEffect,
    // which would run in the same synchronous pass as the useState
    // initializer above), a real point in time where an actual resize or
    // orientation change could already have landed between the two - with
    // no future "change" event left to correct a stale render-time value,
    // since the change already happened.
    setIsMobile(mql.matches);
    const onChange = (event: MediaQueryListEvent) => setIsMobile(event.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);

  return isMobile;
}
