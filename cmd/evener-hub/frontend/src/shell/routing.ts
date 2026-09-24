// URL <-> pane glue - the one place that knows how a pane type + its params
// map to a browser URL and back (see the wave-3 plan's architecture note).
// paneToURL/urlToPane are pure string transforms with no React/browser
// dependency, so any host (this hand-rolled AppShell today, dockview's
// panel titles/links later) can reuse them without pulling in a router.
// navigate() is the one browser-integration helper alongside them: pushing
// history and notifying same-tab listeners is still "URL glue", just not a
// pure function.

import { isLocalHost, normalizeHost } from "../stores/hostRouting";
import type { PaneTypeId } from "./paneRegistry";

function refParam(params: unknown): string | null {
  if (typeof params !== "object" || params === null) return null;
  const ref = (params as { ref?: unknown }).ref;
  return typeof ref === "string" && ref.length > 0 ? ref : null;
}

// A hub session ref is "<hostID>:<sessionID>", both halves non-empty - the
// same shape hubapi.ParseRef (hubapi/refs.go) has always required. A BARE
// session id is not a ref and does not route here.
//
// The frontend used to accept one anyway, and that was a real bug rather than
// mere laxness: the rail opens a session as "local:<id>" while a bare-id deep
// link opens it as "<id>", sameParams reads the two as different panes, and
// the same session ends up open twice, side by side. Observed in a browser.
// Old bare-id links (the htmx UI's canonical route form) are deliberately not
// carried forward - there is one ref form now.
function isRef(raw: string): boolean {
  const at = raw.indexOf(":");
  return at > 0 && at < raw.length - 1;
}

function sectionParam(params: unknown): string | null {
  if (typeof params !== "object" || params === null) return null;
  const section = (params as { section?: unknown }).section;
  return typeof section === "string" && section.length > 0 ? section : null;
}

// matchGroup runs `re` against `pathname` and returns its first capture
// group, decoded - or null if the regex didn't match at all. Folding "no
// match" and "matched but the group was somehow empty" into one null result
// keeps every call site below a single `if (x !== null)` check, and sidesteps
// noUncheckedIndexedAccess turning a guaranteed capture group into
// `string | undefined` at every call site instead of just this one.
function matchGroup(re: RegExp, pathname: string): string | null {
  const value = re.exec(pathname)?.[1];
  return value !== undefined ? decodeURIComponent(value) : null;
}

function assertNever(x: never): never {
  throw new Error(`routing: unhandled pane type "${String(x)}"`);
}

export function urlToPane(pathname: string): { type: PaneTypeId; params: unknown } | null {
  if (pathname === "/") return { type: "welcome", params: {} };
  if (pathname === "/new") return { type: "spawn", params: {} };
  if (pathname === "/settings") return { type: "settings", params: {} };
  // Dedicated top-level alias (Global Constraints lists /credentials as its
  // own deep link, distinct from the generic /settings/{section} pattern
  // below) for old bookmarks/links. paneToURL never emits this form - see
  // its "settings" case.
  if (pathname === "/credentials") return { type: "settings", params: { section: "credentials" } };
  // Old bookmarks for the pre-rewrite /settings/providers page land on the
  // credentials section (triage #12). Intercepted before the generic
  // /settings/{section} match below so it never resolves to a "providers"
  // section that has no pane. Inbound-only, like /credentials: paneToURL
  // emits the canonical /settings/credentials form (its "settings" case).
  if (pathname === "/settings/providers") return { type: "settings", params: { section: "credentials" } };

  const sessionRef = matchGroup(/^\/s\/([^/]+)$/, pathname);
  if (sessionRef !== null && isRef(sessionRef)) return { type: "session", params: { ref: sessionRef } };

  // /thread/{ref} is the share-link target: it renders the SESSION pane
  // (composer live, per the tested legacy thread-document mode) inside the
  // chrome-stripped single-pane shell layout the shell applies off
  // isSinglePaneRoute (singlePane.ts) - NOT the read-only "transcript" pane,
  // which is a distinct open-beside surface with no URL (see paneToURL).
  // Inbound-only, like /credentials: a session pane serializes back to /s/{ref}.
  const threadRef = matchGroup(/^\/thread\/([^/]+)$/, pathname);
  if (threadRef !== null && isRef(threadRef)) return { type: "session", params: { ref: threadRef } };

  const section = matchGroup(/^\/settings\/([^/]+)$/, pathname);
  if (section !== null) return { type: "settings", params: { section } };

  return null;
}

export function paneToURL(type: PaneTypeId, params: unknown): string | null {
  switch (type) {
    case "welcome":
      return "/";
    case "spawn":
      return "/new";
    case "session": {
      const ref = refParam(params);
      return ref !== null ? `/s/${encodeURIComponent(ref)}` : null;
    }
    case "transcript":
      // No deep link: the read-only transcript pane is an open-beside surface
      // (opened contextually via openBeside, not a URL), the same as "doc"
      // below. /thread/{ref} now belongs to the session pane's single-pane
      // mode (see urlToPane), so it can't also address a transcript pane.
      return null;
    case "settings": {
      const section = sectionParam(params);
      return section !== null ? `/settings/${encodeURIComponent(section)}` : "/settings";
    }
    case "doc":
      // No deep link yet - doc panes open contextually from a session, not
      // via a standalone URL. Revisit if/when a wave needs one.
      return null;
    case "sessionTasks":
    case "sessionActivity":
    case "sessionDetails":
      // Session panel panes are contextual surfaces opened beside a session;
      // they intentionally have no standalone URL.
      return null;
    default:
      return assertNever(type);
  }
}

// splitURL separates a path-only URL (optionally carrying a query and a hash)
// into its three parts, so withSettingsHost can rewrite one without disturbing
// the others.
function splitURL(target: string): { path: string; search: string; hash: string } {
  const hashAt = target.indexOf("#");
  const hash = hashAt === -1 ? "" : target.slice(hashAt);
  const body = hashAt === -1 ? target : target.slice(0, hashAt);
  const searchAt = body.indexOf("?");
  return searchAt === -1
    ? { path: body, search: "", hash }
    : { path: body.slice(0, searchAt), search: body.slice(searchAt), hash };
}

/** HOST_QUERY_PARAM is the settings URL's host query parameter. It lives here,
 * with the rest of the settings route's URL contract, so the store that holds the
 * selection can ask this module whether a route is a settings route without the
 * two modules importing each other. */
export const HOST_QUERY_PARAM = "host";

/** isSettingsPath answers whether a pathname names a settings route - the routes
 * urlToPane resolves as "settings", aliases included. The selected host belongs
 * to those routes and to no others: they are the only ones that may carry a host,
 * adopt one, or seed one. */
export function isSettingsPath(pathname: string): boolean {
  return urlToPane(pathname)?.type === "settings";
}

/** settingsHostRouteSync is the settings host store's mirror of an APP-BUILT
 * navigation: navigate() calls it with the path and search it just applied, and
 * the store sets its selection to the host that route names (the local hub off a
 * settings route), so the two never disagree. routing.ts cannot import the store
 * itself - the store imports isSettingsPath from this module, so the dependency
 * runs one way only - so the store registers the one function here instead (see
 * stores/settingsHost.ts's syncSettingsHostToRoute). A module that imports
 * routing without pulling the store in leaves this null and navigate() simply
 * does not mirror; every production entry point reaches the store through the
 * settings pane registration (AppShell imports panes/settings). */
type SettingsHostRouteSync = (pathname: string, search: string) => void;
let settingsHostRouteSync: SettingsHostRouteSync | null = null;

export function setSettingsHostRouteSync(sync: SettingsHostRouteSync | null): void {
  settingsHostRouteSync = sync;
}

// withSettingsHost carries the host the address bar already names onto an
// APP-BUILT navigation to a settings route (component 07b): the selected host
// is part of that route, and the builders that cannot know it - the palette's
// `navigate("/settings")` (palette/commands.ts), AppShell's settings.open
// chord, the mobile shell's own URL sync (mobile/StackHost.tsx), a hand-off
// from another pane - must not silently drop it. A URL the app did not build (a
// hand-typed link, the browser's own Back/Forward) never reaches navigate, so
// it still means exactly what it says; navigate()'s own `carrySettingsHost:
// false` opt-out exists for the one navigation whose point IS to drop it (the
// picker's return to the local hub).
function withSettingsHost(target: string): string {
  const { path, search, hash } = splitURL(target);
  if (urlToPane(path)?.type !== "settings") return target;
  // The host belongs to the SETTINGS route, so only a settings route may carry
  // one out of itself: a non-settings URL that merely carries `?host=` (a
  // hand-typed link, another surface's own parameter) must not leak it onto a
  // settings target.
  if (!isSettingsPath(window.location.pathname)) return target;
  const params = new URLSearchParams(search);
  if (params.has(HOST_QUERY_PARAM)) return target;
  // The current host goes through the ONE spelling rule (hostRouting.ts's
  // normalizeHost, the same one the store's own URL reading uses): an absent,
  // empty, or `local` current host normalizes to the local hub, which is nothing
  // to carry - so the target keeps the canonical hostless local URL instead of
  // gaining a `?host=local` or a bare `?host=`.
  const current = normalizeHost(new URLSearchParams(window.location.search).get(HOST_QUERY_PARAM));
  if (isLocalHost(current)) return target;
  params.set(HOST_QUERY_PARAM, current);
  return `${path}?${params.toString()}${hash}`;
}

// navigate pushes or replaces a pathname in browser history and notifies same-tab
// listeners without a full reload. pushState alone does not fire popstate
// (only real back/forward navigation does), so this dispatches one itself -
// the single event AppShell listens for to cover both programmatic
// navigation (this function) and the browser's own back/forward buttons.
//
// Identity is the COMPLETE target URL (path + query + hash), not just its
// path: a target with a query differs from the bare path of the current URL
// (so navigating from /new?dir=A to /new must clear the query and notify), and
// an identical path+query must not push a duplicate entry or re-notify
// listeners. The argument is always a path (optionally with search/hash) - the
// app never constructs an absolute URL here.
//
// A settings target also carries the host the address bar already names (see
// withSettingsHost) unless `carrySettingsHost: false` says the caller is
// deliberately choosing a URL without it.
export function navigate(pathname: string, options: { replace?: boolean; carrySettingsHost?: boolean } = {}): void {
  const target = options.carrySettingsHost === false ? pathname : withSettingsHost(pathname);
  const current = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  if (current === target) return;
  if (options.replace) window.history.replaceState({}, "", target);
  else window.history.pushState({}, "", target);
  // Mirror the settings selection onto the route the app just chose, before any
  // listener runs: an app-built navigation drops a remote selection it leaves
  // behind and adopts the one its target names, so no host-scoped pane ever
  // renders a host the current route does not name. This is the seam that sees a
  // navigation to a settings URL BEFORE the settings pane mounts - the mount sync
  // only runs after the first render, which would paint the previous selection
  // for a frame (see settingsHostRouteSync above).
  if (settingsHostRouteSync !== null) {
    const { path, search } = splitURL(target);
    settingsHostRouteSync(path, search);
  }
  window.dispatchEvent(new PopStateEvent("popstate"));
}
