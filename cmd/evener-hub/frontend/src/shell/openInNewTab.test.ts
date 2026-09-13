import { afterEach, expect, test, vi } from "vitest";
import { openInNewTab } from "./openInNewTab";
import { captureNewTabs, NEW_TAB_POLICY, openedNewTab } from "./openInNewTab.testSupport";

afterEach(() => {
  vi.restoreAllMocks();
});

test("opens the URL in a new tab, through an anchor carrying the no-opener policy", () => {
  const anchors = captureNewTabs();
  openInNewTab("https://auth.example/start");
  expect(openedNewTab(anchors)).toEqual({
    url: "https://auth.example/start",
    target: "_blank",
    rel: NEW_TAB_POLICY,
  });
});

test("opens an http URL", () => {
  const anchors = captureNewTabs();
  openInNewTab("http://127.0.0.1:9180/auth/start");
  expect(openedNewTab(anchors)).toEqual({
    url: "http://127.0.0.1:9180/auth/start",
    target: "_blank",
    rel: NEW_TAB_POLICY,
  });
});

// The command palette passes a root-relative path for a session, and the OAuth
// caller passes an absolute server URL. Resolving against this document keeps
// the relative caller working (its scheme is this origin's) while still giving
// the validation something to inspect for the absolute one.
test("opens a root-relative URL against this origin", () => {
  const anchors = captureNewTabs();
  openInNewTab("/s/abc");
  expect(openedNewTab(anchors).url).toBe("/s/abc");
});

// A scheme other than http(s) must never reach the click. `javascript:` runs
// in this origin the moment the anchor is clicked; `rel="noopener noreferrer"`
// governs the opener relationship, not what the URL does. The helper refuses
// loudly so each caller's existing error handling reports it.
for (const url of [
  "javascript:alert(document.cookie)",
  "data:text/html,<script>alert(1)</script>",
  "file:///etc/passwd",
]) {
  const scheme = url.slice(0, url.indexOf(":") + 1);
  test(`refuses to open a ${scheme} URL and never clicks`, () => {
    const anchors = captureNewTabs();
    expect(() => openInNewTab(url)).toThrow(scheme);
    expect(anchors).toHaveLength(0);
  });
}

// The opener decides whether the new document runs with a copy of this tab's
// sessionStorage, and with it the per-client mutation identity
// (stores/mutationClientIdentity.ts). That copy happens as the window is
// created, so `window.open`'s "noopener" feature string cannot be the only
// guard: a browser that ignores it (Safari) has copied the storage before
// there is any handle to fix up. This pins that the open does not go through
// `window.open` at all.
test("does not open the tab with window.open, whose noopener the browser may ignore", () => {
  captureNewTabs();
  const open = vi.spyOn(window, "open");
  openInNewTab("https://auth.example/start");
  expect(open).not.toHaveBeenCalled();
});

test("leaves no anchor behind in the document", () => {
  const before = document.body.childElementCount;
  captureNewTabs();
  openInNewTab("https://auth.example/start");
  expect(document.body.childElementCount).toBe(before);
});
