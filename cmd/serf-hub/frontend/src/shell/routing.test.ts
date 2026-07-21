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
  expect(urlToPane("/s/ref_abc123")).toEqual({ type: "session", params: { ref: "ref_abc123" } });
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

test("urlToPane resolves /thread/{ref} to a transcript pane", () => {
  expect(urlToPane("/thread/ref_abc123")).toEqual({ type: "transcript", params: { ref: "ref_abc123" } });
});

test("urlToPane returns null for an unknown path", () => {
  expect(urlToPane("/not/a/real/route")).toBeNull();
});

test("urlToPane returns null for a session path with an empty ref", () => {
  expect(urlToPane("/s/")).toBeNull();
});

test("urlToPane decodes a URI-encoded ref", () => {
  expect(urlToPane("/s/ref%20with%20space")).toEqual({ type: "session", params: { ref: "ref with space" } });
});

// --- paneToURL: the reverse direction -------------------------------------

test("paneToURL formats the welcome pane as /", () => {
  expect(paneToURL("welcome", {})).toBe("/");
});

test("paneToURL formats the spawn pane as /new", () => {
  expect(paneToURL("spawn", {})).toBe("/new");
});

test("paneToURL formats a session pane as /s/{ref}", () => {
  expect(paneToURL("session", { ref: "ref_abc123" })).toBe("/s/ref_abc123");
});

test("paneToURL formats a transcript pane as /thread/{ref}", () => {
  expect(paneToURL("transcript", { ref: "ref_abc123" })).toBe("/thread/ref_abc123");
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

test("paneToURL returns null for a transcript pane with an empty ref", () => {
  expect(paneToURL("transcript", { ref: "" })).toBeNull();
});

test("paneToURL returns null for a doc pane (no deep link defined yet)", () => {
  expect(paneToURL("doc", { ref: "ref_abc123", path: "README.md" })).toBeNull();
});

test("paneToURL encodes a ref that needs it", () => {
  expect(paneToURL("session", { ref: "ref with space" })).toBe("/s/ref%20with%20space");
});

// --- round trip: every URL paneToURL produces resolves back via urlToPane --

test("round trip: session URL", () => {
  const url = paneToURL("session", { ref: "ref_xyz" });
  expect(url).not.toBeNull();
  expect(urlToPane(url!)).toEqual({ type: "session", params: { ref: "ref_xyz" } });
});

test("round trip: transcript URL", () => {
  const url = paneToURL("transcript", { ref: "ref_xyz" });
  expect(url).not.toBeNull();
  expect(urlToPane(url!)).toEqual({ type: "transcript", params: { ref: "ref_xyz" } });
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
