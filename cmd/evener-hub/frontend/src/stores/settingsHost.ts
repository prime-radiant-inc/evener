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
// Only state lives here: this module imports zustand and hostRouting, nothing
// from the shell. The URL glue and the React hook live in
// panes/settings/settingsHost.ts; shell/routing.ts imports HOST_QUERY_PARAM
// from here so an app-built settings navigation can carry the host, which is
// safe because this module never imports routing back.
import { createStore, useStore } from "zustand";
import { isLocalHost, LOCAL_HOST } from "./hostRouting";

/** HOST_QUERY_PARAM is the settings URL's host query parameter. */
export const HOST_QUERY_PARAM = "host";

/** normalizeHost maps an absent, empty, or `local` spelling onto LOCAL_HOST -
 * the default - and leaves every real host name alone. */
export function normalizeHost(raw: string | null): string {
  return raw === null || raw === "" || isLocalHost(raw) ? LOCAL_HOST : raw;
}

/** readHostFromSearch reads the settings route's host out of a URL search
 * string, e.g. `"?host=beta"`. */
export function readHostFromSearch(search: string): string {
  return normalizeHost(new URLSearchParams(search).get(HOST_QUERY_PARAM));
}

interface SettingsHostState {
  host: string;
}

// Seeded from the URL at first use, so a deep link or a reload lands on the
// host it names before anything renders. Guarded for the non-DOM test
// environments this module is reachable from through shell/routing.ts (a node
// test importing the routing module must not need a window just to read a
// parameter name): with no window there is no URL to seed from, so the default
// local hub is the honest answer.
export const settingsHostStore = createStore<SettingsHostState>(() => ({
  host: typeof window === "undefined" ? LOCAL_HOST : readHostFromSearch(window.location.search),
}));

/** setSettingsHost is the one writer of the selection. Callers that also want
 * the route to carry it use the useSettingsHost hook
 * (panes/settings/settingsHost.ts) rather than this directly. */
export function setSettingsHost(host: string): void {
  settingsHostStore.setState({ host: normalizeHost(host) });
}

/** adoptSettingsHostFromURL applies what the settings route names, and the route
 * is the whole authority: a URL with no host means the local hub.
 *
 * That stays honest because every APP-BUILT settings navigation carries the host
 * (shell/routing.ts's navigate, via its own withSettingsHost), so a hostless
 * settings URL is either one the app deliberately built for local or one the app
 * did not build at all (a hand-typed link, the browser's Back/Forward). The pane
 * and the address bar therefore never disagree, and a reload reproduces what the
 * user was looking at - which the earlier "absence leaves the selection alone"
 * reading could not promise. */
export function adoptSettingsHostFromURL(search: string): void {
  setSettingsHost(readHostFromSearch(search));
}

export function useSettingsHostStore<T>(selector: (state: SettingsHostState) => T): T {
  return useStore(settingsHostStore, selector);
}

export function resetSettingsHostForTests(): void {
  settingsHostStore.setState({ host: LOCAL_HOST });
}
