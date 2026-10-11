// settingsHost.ts is the settings route's host-context glue: the pure URL
// helpers and the one hook every host-scoped settings pane (and the shared
// HostPicker) reads.
//
// The selection itself is stores/settingsHost.ts; this module owns how the
// route carries it - serialized into `?host=<name>` (omitted for local, so
// today's URLs are unchanged), restored from the URL, and carried across every
// app-built settings navigation (shell/routing.ts's navigate).
import { useCallback, useEffect } from "react";
import { HOST_QUERY_PARAM, navigate, paneToURL, urlToPane } from "../../shell/routing";
import { isLocalHost } from "../../stores/hostRouting";
import {
  adoptSettingsHostFromURL,
  setSettingsHost,
  syncSettingsHostToRoute,
  useSettingsHostStore,
} from "../../stores/settingsHost";

/** settingsURL is the settings route for `section` carrying `host`. `local`
 * adds nothing, so a user who never picks a host keeps today's URL. */
export function settingsURL(section: string | undefined, host: string): string {
  const path = paneToURL("settings", section !== undefined ? { section } : {}) ?? "/settings";
  return isLocalHost(host) ? path : `${path}?${HOST_QUERY_PARAM}=${encodeURIComponent(host)}`;
}

/** settingsURLWithQuery is settingsURL for a navigation WITHIN the route the
 * user is already on: the route's own query parameters ride along, except the
 * one the selection owns.
 *
 * The project pane is what this exists for: it is reached as
 * /settings/project?cwd=<dir> (its own comment - the section IS the project),
 * and its host picker rewrites only the host. Dropping the query leaves the
 * pane reading no cwd at all, so it renders "No project selected" instead of
 * re-scoping the same project to the host just picked - which is the one thing
 * ProjectHostScope's own doc comment promises. */
export function settingsURLWithQuery(section: string | undefined, host: string, search: string): string {
  const params = new URLSearchParams(search);
  params.delete(HOST_QUERY_PARAM);
  const carried = params.toString();
  const base = settingsURL(section, host);
  return carried === "" ? base : `${base}${base.includes("?") ? "&" : "?"}${carried}`;
}

// currentSettingsSection reads the section the address bar names, so the picker
// rewrites the host on the route it is already on rather than guessing one.
function currentSettingsSection(): string | undefined {
  const route = urlToPane(window.location.pathname);
  if (route === null || route.type !== "settings") return undefined;
  const section = (route.params as { section?: unknown }).section;
  return typeof section === "string" ? section : undefined;
}

export interface SettingsHostSelection {
  host: string;
  /** selectHost makes `host` the route's selection: it updates the shared
   * store immediately (every host-scoped pane re-reads at once) and rewrites
   * the URL, so the choice survives a section switch and a reload.
   *
   * The target is built explicitly (settingsURLWithQuery), carrying the route's
   * other query parameters - the project pane's ?cwd= above all, so a switch
   * re-scopes the SAME project to the new host rather than dropping it - while
   * the host parameter is the selection's own.
   *
   * `carrySettingsHost: false` because this is the one navigation whose point is
   * to build the URL explicitly - including the removal of the parameter when
   * the user returns to the local hub (see routing.ts's withSettingsHost). */
  selectHost: (host: string) => void;
}

export function useSettingsHost(): SettingsHostSelection {
  const host = useSettingsHostStore((state) => state.host);
  const selectHost = useCallback((next: string) => {
    setSettingsHost(next);
    navigate(settingsURLWithQuery(currentSettingsSection(), next, window.location.search), {
      carrySettingsHost: false,
    });
  }, []);
  return { host, selectHost };
}

/** useSettingsHostURLSync makes the settings route the authority on the
 * selection - on mount (a deep link or reload) and on every popstate
 * (Back/Forward, and navigate()'s own dispatch, whose target already carries the
 * host). A settings URL with no host means the local hub; the app's own
 * navigations never produce one by accident because routing.ts carries the host
 * onto them. Settings.tsx calls it once, as the settings route's shell.
 *
 * The adoption itself refuses to run off a settings route (stores/settingsHost.ts
 * asks, so no caller can forget to): navigate() announces its own navigation with
 * a popstate, and without that check, navigating AWAY from settings to a hostless
 * URL - or a non-settings URL that merely carries a `host` parameter - would
 * rewrite the stored selection from a route the selection does not belong to. */
export function useSettingsHostURLSync(): void {
  useEffect(() => {
    adoptSettingsHostFromURL();
    window.addEventListener("popstate", adoptSettingsHostFromURL);
    return () => {
      window.removeEventListener("popstate", adoptSettingsHostFromURL);
      // Leaving the settings route drops the selection with it. This hook is the
      // ROUTE-level one (Settings.tsx calls it once), so a section switch INSIDE
      // settings re-renders the same pane and never runs this - a remote
      // selection survives it, as it must - while any real departure does. An
      // app-built navigation is already mirrored by shell/routing.ts's navigate;
      // this covers the rest (a browser Back/Forward, a direct pushState, a pane
      // close), seeding from the live URL so it is a no-op for a settings route
      // and a reset off one.
      syncSettingsHostToRoute(window.location.pathname, window.location.search);
    };
  }, []);
}
