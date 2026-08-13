// @vitest-environment jsdom
import { afterEach, expect, test } from "vitest";
import type { TreeNode as ApiTreeNode, TreeResponse } from "../../stores/tree";
import { needsYouRefs, nextNeedsYouRef, openNeedsYouSession } from "./needsYouCycle";

afterEach(() => {
  window.history.pushState({}, "", "/");
});

function node(overrides: Partial<ApiTreeNode> = {}): ApiTreeNode {
  return {
    row_id: "row",
    ref: "local:a",
    host_id: "local",
    session_id: "a",
    title: "A",
    project: "Proj",
    state: "awaiting",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

function tree(overrides: Partial<TreeResponse> = {}): TreeResponse {
  return {
    generated_at: "2026-01-01T00:00:00Z",
    sources: [],
    live: [],
    needs_you: [],
    pin_sections: [],
    projects: [],
    archived_projects: [],
    test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
    ...overrides,
  };
}

// --- needsYouRefs: the tree-order source of truth -------------------------

test("needsYouRefs maps tree.needs_you to its refs, preserving server order", () => {
  const refs = needsYouRefs(
    tree({ needs_you: [node({ ref: "local:a" }), node({ ref: "local:b" }), node({ ref: "local:c" })] }),
  );
  expect(refs).toEqual(["local:a", "local:b", "local:c"]);
});

test("needsYouRefs is empty for a null tree (not yet loaded)", () => {
  expect(needsYouRefs(null)).toEqual([]);
});

// --- nextNeedsYouRef: cycling from the current focus, wrapping ------------

test("nextNeedsYouRef advances to the next ref in the list", () => {
  expect(nextNeedsYouRef(["a", "b", "c"], "a")).toBe("b");
  expect(nextNeedsYouRef(["a", "b", "c"], "b")).toBe("c");
});

test("nextNeedsYouRef wraps from the last ref back to the first", () => {
  expect(nextNeedsYouRef(["a", "b", "c"], "c")).toBe("a");
});

test("nextNeedsYouRef starts at the first ref when nothing is currently focused", () => {
  expect(nextNeedsYouRef(["a", "b", "c"], null)).toBe("a");
});

test("nextNeedsYouRef starts at the first ref when the focused session isn't in the needs-you list", () => {
  expect(nextNeedsYouRef(["a", "b", "c"], "not-in-list")).toBe("a");
});

test("nextNeedsYouRef returns null for an empty list", () => {
  expect(nextNeedsYouRef([], "a")).toBeNull();
  expect(nextNeedsYouRef([], null)).toBeNull();
});

// --- openNeedsYouSession: the same navigate-to-URL seam a rail row uses ---
//
// AppShell's own route-placement effect is what actually resolves
// top-level-vs-nested from the URL (see AppShell.test.tsx's own Mod+J
// coverage for that end-to-end behavior) - this unit only has to prove the
// navigation itself.

test("openNeedsYouSession navigates to the session's /s/{ref} URL (paneToURL's own encodeURIComponent)", () => {
  window.history.pushState({}, "", "/");

  openNeedsYouSession("local:ny1");

  expect(window.location.pathname).toBe("/s/local%3Any1");
});

test("openNeedsYouSession is a no-op when already on that session's URL", () => {
  window.history.pushState({}, "", "/s/local%3Any1");
  let popped = false;
  window.addEventListener("popstate", () => (popped = true), { once: true });

  openNeedsYouSession("local:ny1");

  expect(popped).toBe(false);
});
