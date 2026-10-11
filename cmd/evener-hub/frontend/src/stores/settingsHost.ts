// settingsHost.ts is the settings route's selected host (component 07b's
// host-context): the one host every host-scoped settings pane reads, made once
// at the route level instead of re-invented per pane.
//
// The selection is a module-scoped store, like every other cross-pane store
// here, and it is part of the SETTINGS ROUTE as well: the URL carries it as
// `?host=<name>` (never for `local`, so today's settings URLs stay
// byte-for-byte unchanged), panes/settings/settingsHost.ts serializes and
// restores it, and Settings.tsx keeps it across a section switch.
//
// This module holds the state and the two things that read or rewrite it from a
// URL: the seed a fresh load starts from, and the adoption a popstate performs.
// Both ask shell/routing.ts whether the route is a settings route, which is why
// the settings URL's parameter name and predicate live THERE (routing.ts imports
// hostRouting.ts's one spelling rule, and this module imports routing.ts, so the
// dependency runs one way only). The React glue - the settings URL builder, the
// shared hook and the mount/popstate sync - lives in panes/settings/settingsHost.ts;
// shell/routing.ts's navigate carries the host across the app's own navigations.
import { createStore, useStore } from "zustand";
import { HOST_QUERY_PARAM, isSettingsPath, setSettingsHostRouteSync } from "../shell/routing";
import { LOCAL_HOST, normalizeHost } from "./hostRouting";

// The one spelling rule (absent / empty / `local` -> LOCAL_HOST) lives in
// hostRouting.ts, where shell/routing.ts can reach it too: it is re-exported here
// so this module keeps the surface its consumers and tests already use.
export { normalizeHost };

/** readHostFromSearch reads the settings route's host out of a URL search string,
 * e.g. `"?host=beta"`, with the local hub as the answer when it names none. */
export function readHostFromSearch(search: string): string {
  return normalizeHost(new URLSearchParams(search).get(HOST_QUERY_PARAM));
}

interface SettingsHostState {
  host: string;
}

/** initialSettingsHost is the selection a fresh page load starts from: the host
 * the URL names, but ONLY while the URL is a settings route. `host` is not a
 * settings-exclusive parameter, and the URL sync adopts only on a settings route,
 * so seeding from anywhere else would hand the pane's first render (which runs
 * before the settings shell's own effect) a host the route never named - a
 * spurious evener/host/request for a host that is not configured, and a flash of
 * its refusal, on an app open that should mean "local". */
export function initialSettingsHost(pathname: string, search: string): string {
  return isSettingsPath(pathname) ? readHostFromSearch(search) : LOCAL_HOST;
}

// Seeded from the URL at first use, so a deep link or a reload lands on the host
// it names before anything renders - on a settings route only (see
// initialSettingsHost). Guarded for the non-DOM test environments this module is
// reachable from through shell/routing.ts (a node test importing the routing
// module must not need a window just to read a parameter name): with no window
// there is no URL to seed from, so the default local hub is the honest answer.
export const settingsHostStore = createStore<SettingsHostState>(() => ({
  host:
    typeof window === "undefined" ? LOCAL_HOST : initialSettingsHost(window.location.pathname, window.location.search),
}));

/** setSettingsHost is the one writer of the selection. Callers that also want
 * the route to carry it use the useSettingsHost hook
 * (panes/settings/settingsHost.ts) rather than this directly. */
export function setSettingsHost(host: string): void {
  settingsHostStore.setState({ host: normalizeHost(host) });
}

/** adoptSettingsHostFromURL applies what the SETTINGS route names, reading the
 * live location, and the route is the whole authority: a settings URL with no
 * host means the local hub.
 *
 * That stays honest because every APP-BUILT settings navigation carries the host
 * (shell/routing.ts's navigate, via its own withSettingsHost), so a hostless
 * settings URL is either one the app deliberately built for local or one the app
 * did not build at all (a hand-typed link, the browser's Back/Forward). The pane
 * and the address bar therefore never disagree, and a reload reproduces what the
 * user was looking at - which the earlier "absence leaves the selection alone"
 * reading could not promise.
 *
 * The route check lives HERE rather than in the caller that runs it on mount and
 * on every popstate: the selection belongs to the settings route, and this is the
 * one function that rewrites it from a URL, so asking the question here is what
 * makes it impossible to call around. */
export function adoptSettingsHostFromURL(): void {
  if (!isSettingsPath(window.location.pathname)) return;
  setSettingsHost(readHostFromSearch(window.location.search));
}

/** syncSettingsHostToRoute mirrors the selection onto a route the app just
 * navigated to: the host a SETTINGS route names, or the local hub off one. It is
 * the both-directions form of the route's authority - adoptSettingsHostFromURL
 * only ever rewrites inside a settings route, while this also DROPS a remote
 * selection the app has navigated away from, so the selection cannot outlive the
 * route.
 *
 * shell/routing.ts's navigate calls it at the moment it applies an app-built
 * navigation (its own withSettingsHost has already carried the host onto a
 * settings target, so the target names exactly what the route should select),
 * and the settings pane's unmount seeds from the live URL so a browser
 * Back/Forward - which never calls navigate - drops it too. */
export function syncSettingsHostToRoute(pathname: string, search: string): void {
  setSettingsHost(initialSettingsHost(pathname, search));
}

// Handed to shell/routing.ts's navigate, which cannot import this store itself
// (the dependency runs one way only - see that module's settingsHostRouteSync).
setSettingsHostRouteSync(syncSettingsHostToRoute);

export function useSettingsHostStore<T>(selector: (state: SettingsHostState) => T): T {
  return useStore(settingsHostStore, selector);
}

export function resetSettingsHostForTests(): void {
  settingsHostStore.setState({ host: LOCAL_HOST });
}
