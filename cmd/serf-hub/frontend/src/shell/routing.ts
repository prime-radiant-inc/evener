// URL <-> pane glue - the one place that knows how a pane type + its params
// map to a browser URL and back (see the wave-3 plan's architecture note).
// paneToURL/urlToPane are pure string transforms with no React/browser
// dependency, so any host (this hand-rolled AppShell today, dockview's
// panel titles/links later) can reuse them without pulling in a router.
// navigate() is the one browser-integration helper alongside them: pushing
// history and notifying same-tab listeners is still "URL glue", just not a
// pure function.
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
    default:
      return assertNever(type);
  }
}

// navigate pushes a new pathname onto browser history and notifies same-tab
// listeners without a full reload. pushState alone does not fire popstate
// (only real back/forward navigation does), so this dispatches one itself -
// the single event AppShell listens for to cover both programmatic
// navigation (this function) and the browser's own back/forward buttons.
export function navigate(pathname: string): void {
  if (window.location.pathname === pathname) return;
  window.history.pushState({}, "", pathname);
  window.dispatchEvent(new PopStateEvent("popstate"));
}
