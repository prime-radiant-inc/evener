// The dismiss seam for a host that presents navigation chrome as an overlay.
// The phone-width Sessions drawer is that host today: its rail is the picker,
// and selecting a session is the tap's own intent, so dismissal must not be
// inferred from focus alone. Tapping the session already being read changes no
// focus, and a placement waiting on a location read changes none until it
// answers; both left the drawer covering the result.
//
// Desktop mounts its rail without a provider, so the hook returns null and a
// rail activation pays nothing.
import { createContext, useContext } from "react";

const NavigationChromeContext = createContext<(() => void) | null>(null);

export const NavigationChromeProvider = NavigationChromeContext.Provider;

/** The overlay host's dismiss action, or null where navigation is always
 * visible. */
export function useDismissNavigationChrome(): (() => void) | null {
  return useContext(NavigationChromeContext);
}
