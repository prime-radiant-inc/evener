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
