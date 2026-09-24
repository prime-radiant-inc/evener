import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { navigate, paneToURL, urlToPane } from "./routing";

beforeEach(() => {
  window.history.pushState({}, "", "/");
});

afterEach(() => {
  window.history.pushState({}, "", "/");
});

// --- urlToPane: every deep link in the Global Constraints contract -------

test("urlToPane resolves / to the welcome pane", () => {
  expect(urlToPane("/")).toEqual({ type: "welcome", params: {} });
});

test("urlToPane resolves /new to the spawn pane", () => {
  expect(urlToPane("/new")).toEqual({ type: "spawn", params: {} });
});

test("urlToPane resolves /s/{ref} to a session pane", () => {
  expect(urlToPane("/s/local:abc123")).toEqual({ type: "session", params: { ref: "local:abc123" } });
});

test("urlToPane resolves /settings to the settings pane with no section", () => {
  expect(urlToPane("/settings")).toEqual({ type: "settings", params: {} });
});

test("urlToPane resolves /settings/{section} to the settings pane with that section", () => {
  expect(urlToPane("/settings/appearance")).toEqual({ type: "settings", params: { section: "appearance" } });
});

test("urlToPane resolves /credentials to the settings pane's credentials section", () => {
  expect(urlToPane("/credentials")).toEqual({ type: "settings", params: { section: "credentials" } });
});

test("urlToPane resolves /settings/providers to the credentials section (old-bookmark redirect)", () => {
  expect(urlToPane("/settings/providers")).toEqual({ type: "settings", params: { section: "credentials" } });
});

test("urlToPane resolves /thread/{ref} to a session pane (single-pane share link)", () => {
  // /thread/{ref} is the share-link target: it opens the SESSION pane, which
  // the shell then renders chrome-stripped (isSinglePaneRoute). It no longer
  // maps to the never-URL'd read-only transcript pane.
  expect(urlToPane("/thread/local:abc123")).toEqual({ type: "session", params: { ref: "local:abc123" } });
});

test("urlToPane returns null for an unknown path", () => {
  expect(urlToPane("/not/a/real/route")).toBeNull();
});

test("urlToPane returns null for a session path with an empty ref", () => {
  expect(urlToPane("/s/")).toBeNull();
});

// One canonical ref form. hubapi.ParseRef (hubapi/refs.go) has always
// required "<host>:<session>" - a bare session id is not a ref. The frontend
// used to accept one anyway, which meant the SAME session could occupy two
// panes at once: the rail opens "local:<id>", a bare-id deep link opens
// "<id>", and sameParams sees two different panes. Observed in a browser.
test("a bare session id is not a ref and does not resolve to a session pane", () => {
  expect(urlToPane("/s/033vPyFQqNrAhgoiyUvUEy")).toBeNull();
  expect(urlToPane("/thread/033vPyFQqNrAhgoiyUvUEy")).toBeNull();
});

test("a ref with an empty half is rejected the same way", () => {
  expect(urlToPane("/s/local:")).toBeNull();
  expect(urlToPane("/s/:abc123")).toBeNull();
});

test("a non-local host ref still resolves", () => {
  expect(urlToPane("/s/laptop:abc123")).toEqual({ type: "session", params: { ref: "laptop:abc123" } });
});

test("urlToPane decodes a URI-encoded ref", () => {
  expect(urlToPane("/s/local%3Aref%20with%20space")).toEqual({
    type: "session",
    params: { ref: "local:ref with space" },
  });
});

// --- paneToURL: the reverse direction -------------------------------------

test("paneToURL formats the welcome pane as /", () => {
  expect(paneToURL("welcome", {})).toBe("/");
});

test("paneToURL formats the spawn pane as /new", () => {
  expect(paneToURL("spawn", {})).toBe("/new");
});

test("paneToURL formats a session pane as /s/{ref}", () => {
  expect(paneToURL("session", { ref: "local:abc123" })).toBe("/s/local%3Aabc123");
});

test("paneToURL returns null for a transcript pane (open-beside only, no deep link)", () => {
  // The read-only transcript pane is reached via openBeside, not a URL - the
  // same as the doc pane. /thread/{ref} belongs to the session pane now.
  expect(paneToURL("transcript", { ref: "ref_abc123" })).toBeNull();
});

test("paneToURL formats settings with no section as /settings", () => {
  expect(paneToURL("settings", {})).toBe("/settings");
});

test("paneToURL formats settings with a section as /settings/{section}", () => {
  expect(paneToURL("settings", { section: "appearance" })).toBe("/settings/appearance");
});

test("paneToURL formats the credentials section as /settings/credentials, not the /credentials alias", () => {
  // /credentials is an inbound-only alias urlToPane accepts for old
  // bookmarks/links; paneToURL always emits the canonical nested form so
  // there is exactly one URL the app itself ever constructs.
  expect(paneToURL("settings", { section: "credentials" })).toBe("/settings/credentials");
});

test("paneToURL returns null for a session pane with no ref", () => {
  expect(paneToURL("session", {})).toBeNull();
});

test("paneToURL returns null for a transcript pane regardless of ref", () => {
  expect(paneToURL("transcript", { ref: "" })).toBeNull();
  expect(paneToURL("transcript", { ref: "ref_xyz" })).toBeNull();
});

test("paneToURL returns null for a doc pane (no deep link defined yet)", () => {
  expect(paneToURL("doc", { ref: "ref_abc123", path: "README.md" })).toBeNull();
});

test("paneToURL encodes a ref that needs it", () => {
  expect(paneToURL("session", { ref: "local:ref with space" })).toBe("/s/local%3Aref%20with%20space");
});

// --- round trip: every URL paneToURL produces resolves back via urlToPane --

test("round trip: session URL", () => {
  const url = paneToURL("session", { ref: "local:xyz" });
  expect(url).not.toBeNull();
  expect(urlToPane(url!)).toEqual({ type: "session", params: { ref: "local:xyz" } });
});

test("inbound alias: /thread/{ref} resolves to a session pane, which serializes back to /s/{ref}", () => {
  // /thread/{ref} has no round trip of its own (like /credentials): it is an
  // inbound-only share link. It resolves to a session pane, and a session
  // pane's own canonical URL is /s/{ref}, so the app never emits /thread.
  const inbound = urlToPane("/thread/local:xyz");
  expect(inbound).toEqual({ type: "session", params: { ref: "local:xyz" } });
  expect(paneToURL("session", { ref: "local:xyz" })).toBe("/s/local%3Axyz");
});

test("round trip: settings with section URL", () => {
  const url = paneToURL("settings", { section: "appearance" });
  expect(url).not.toBeNull();
  expect(urlToPane(url!)).toEqual({ type: "settings", params: { section: "appearance" } });
});

test("round trip: welcome, spawn, and bare settings URLs", () => {
  expect(urlToPane(paneToURL("welcome", {})!)).toEqual({ type: "welcome", params: {} });
  expect(urlToPane(paneToURL("spawn", {})!)).toEqual({ type: "spawn", params: {} });
  expect(urlToPane(paneToURL("settings", {})!)).toEqual({ type: "settings", params: {} });
});

// --- navigate: pushState + same-tab notification --------------------------

test("navigate pushes the new pathname onto history", () => {
  navigate("/new");
  expect(window.location.pathname).toBe("/new");
});

test("navigate dispatches a popstate event so same-tab listeners can react", () => {
  const handler = vi.fn();
  window.addEventListener("popstate", handler);
  navigate("/new");
  window.removeEventListener("popstate", handler);
  expect(handler).toHaveBeenCalledTimes(1);
});

test("navigate is a no-op when already at the target pathname", () => {
  window.history.pushState({}, "", "/new");
  const handler = vi.fn();
  window.addEventListener("popstate", handler);
  navigate("/new");
  window.removeEventListener("popstate", handler);
  expect(handler).not.toHaveBeenCalled();
});

// RoboRev PR1131 finding 4: navigate() compared only the pathname, so a target
// with a query is never treated as the same document as the bare path. The
// review described the inverse symptom (a /new -> /new?dir= navigate returning
// early); that does not happen because the argument carries the query and so
// never equals window.location.pathname. The real defects of a pathname-only
// identity are below. The bare-path case is the closest match to the finding's
// "returns without updating the query": when already at /new?dir=A, navigating
// to "/new" must clear the query and notify listeners, not silently return.
test("navigate clears the query when the target is the bare path of the current URL", () => {
  window.history.pushState({}, "", "/new?dir=/tmp/project-a");
  const handler = vi.fn();
  window.addEventListener("popstate", handler);
  navigate("/new");
  window.removeEventListener("popstate", handler);
  expect(window.location.pathname).toBe("/new");
  expect(window.location.search).toBe("");
  expect(handler).toHaveBeenCalledTimes(1);
});

test("navigate is a no-op when already at the identical path and query", () => {
  window.history.pushState({}, "", "/new?dir=/tmp/project-a");
  const handler = vi.fn();
  window.addEventListener("popstate", handler);
  navigate("/new?dir=/tmp/project-a");
  window.removeEventListener("popstate", handler);
  expect(window.location.search).toBe("?dir=/tmp/project-a");
  expect(handler).not.toHaveBeenCalled();
});

// Evidence that the finding's literal symptom does not reproduce: the argument
// carries the query, so it never equals the bare window.location.pathname.
test("navigate to /new?dir=... from /new updates the query and notifies", () => {
  window.history.pushState({}, "", "/new");
  const handler = vi.fn();
  window.addEventListener("popstate", handler);
  navigate("/new?dir=/tmp/project-b");
  window.removeEventListener("popstate", handler);
  expect(window.location.pathname).toBe("/new");
  expect(window.location.search).toBe("?dir=/tmp/project-b");
  expect(handler).toHaveBeenCalledTimes(1);
});

// --- navigate: the settings route carries its host (component 07b) --------
//
// The selected host is part of the settings route, so an app-built navigation
// to a settings URL keeps the host the address bar already names. That is what
// lets every builder which cannot know the selection (the palette's
// navigate("/settings"), AppShell's settings.open chord, the mobile shell's own
// URL sync, a hand-off from another pane) avoid silently dropping it - and what
// makes a hostless settings URL mean the local hub. The opt-out exists for the
// one navigation whose point IS to remove the parameter.

test("navigate carries the current host onto a settings target", () => {
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  navigate("/settings/theme");
  expect(window.location.pathname).toBe("/settings/theme");
  expect(window.location.search).toBe("?host=beta");
});

test("navigate carries the host onto the bare settings route", () => {
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  navigate("/settings");
  expect(window.location.pathname).toBe("/settings");
  expect(window.location.search).toBe("?host=beta");
});

test("navigate leaves a settings target that already names a host alone", () => {
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  navigate("/settings/credentials?host=gamma");
  expect(window.location.search).toBe("?host=gamma");
});

test("carrySettingsHost: false leaves the target without a host", () => {
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  navigate("/settings/credentials", { carrySettingsHost: false });
  expect(window.location.pathname).toBe("/settings/credentials");
  expect(window.location.search).toBe("");
});

test("navigate carries a host alongside a settings target's other query parameters", () => {
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  navigate("/settings/project?cwd=/x");
  const params = new URLSearchParams(window.location.search);
  expect(params.get("cwd")).toBe("/x");
  expect(params.get("host")).toBe("beta");
});

test("navigate does not touch a non-settings target", () => {
  window.history.pushState({}, "", "/settings/credentials?host=beta");
  navigate("/new");
  expect(window.location.pathname).toBe("/new");
  expect(window.location.search).toBe("");
});

test("navigate carries nothing when the current route names no host", () => {
  window.history.pushState({}, "", "/settings/credentials");
  navigate("/settings/theme");
  expect(window.location.pathname).toBe("/settings/theme");
  expect(window.location.search).toBe("");
});

// M-4: the host belongs to the settings route, so only a settings route may
// carry one out of itself. A non-settings URL that merely carries a `host`
// parameter must not leak it onto a settings target.
test("navigate does not carry a host out of a non-settings route", () => {
  window.history.pushState({}, "", "/new?host=ghost");
  navigate("/settings/theme");
  expect(window.location.pathname).toBe("/settings/theme");
  expect(window.location.search).toBe("");
});

test("navigate does not carry a host out of a session route", () => {
  window.history.pushState({}, "", "/s/local:abc?host=ghost");
  navigate("/settings");
  expect(window.location.pathname).toBe("/settings");
  expect(window.location.search).toBe("");
});

// L-2 (round 4): the carry check only looked for an ABSENT parameter, so a
// current `?host=local` - or an empty `?host=` - was propagated onto a settings
// target as a non-canonical URL instead of the canonical hostless local one.
test("navigate does not carry a local host parameter", () => {
  window.history.pushState({}, "", "/settings/credentials?host=local");
  navigate("/settings/theme");
  expect(window.location.pathname).toBe("/settings/theme");
  expect(window.location.search).toBe("");
});

test("navigate does not carry an empty host parameter", () => {
  window.history.pushState({}, "", "/settings/credentials?host=");
  navigate("/settings/theme");
  expect(window.location.pathname).toBe("/settings/theme");
  expect(window.location.search).toBe("");
});
