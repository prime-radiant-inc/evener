// @vitest-environment jsdom

import type { NavigationSessionSummary } from "@evener/appwire-client";
import { afterEach, expect, test } from "vitest";
import "../../panes/session";
import { resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { needsYouRefs, nextNeedsYouRef, openNeedsYouSession } from "./needsYouCycle";

afterEach(() => {
  window.history.pushState({}, "", "/");
  window.history.replaceState({}, "", "/");
  resetWorkspaceStoreForTests();
});

function row(ref: string): NavigationSessionSummary {
  return {
    ref,
    host_id: "local",
    session_id: ref,
    title: ref,
    project: "Proj",
    state: "awaiting",
    kind: "session",
    live: true,
    children: [],
  };
}

test("needsYouRefs preserves bounded server order", () => {
  expect(needsYouRefs([row("local:a"), row("local:b"), row("local:c")])).toEqual(["local:a", "local:b", "local:c"]);
});
test("needsYouRefs is empty before resources load", () => expect(needsYouRefs(null)).toEqual([]));
test("nextNeedsYouRef advances and wraps", () => {
  expect(nextNeedsYouRef(["a", "b", "c"], "a")).toBe("b");
  expect(nextNeedsYouRef(["a", "b", "c"], "c")).toBe("a");
});
test("nextNeedsYouRef starts at first without a matching focus", () => {
  expect(nextNeedsYouRef(["a", "b", "c"], null)).toBe("a");
  expect(nextNeedsYouRef(["a", "b", "c"], "missing")).toBe("a");
});
test("nextNeedsYouRef returns null for an empty list", () => expect(nextNeedsYouRef([], "a")).toBeNull());
test("nextNeedsYouRef excludes its only current session and duplicate rows", () => {
  expect(nextNeedsYouRef(["a"], "a")).toBeNull();
  expect(nextNeedsYouRef(["a", "b", "b", "c"], "b")).toBe("c");
});
test("openNeedsYouSession navigates to encoded session URL", () => {
  openNeedsYouSession("local:ny1");
  expect(window.location.pathname).toBe("/s/local%3Any1");
});
test("openNeedsYouSession records an attention step even for the current URL", () => {
  window.history.pushState({}, "", "/s/local%3Any1");
  workspaceStore.getState().openPane("session", { ref: "local:ny1" });
  const initialLength = window.history.length;
  let popped = false;
  window.addEventListener("popstate", () => (popped = true), { once: true });
  openNeedsYouSession("local:ny1");
  expect(window.history.length).toBe(initialLength + 1);
  expect((window.history.state as Record<string, unknown>).__evenerAttention).toMatchObject({
    generation: expect.any(String),
    id: expect.any(String),
  });
  expect(popped).toBe(true);
});
