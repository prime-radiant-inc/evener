import { expect, test } from "vitest";
import type { ThreadModel } from "../../protocol/model";
import type { TreeResponse } from "../../stores/tree";
import { resolveThreadName } from "./threadTitle";

function fixtureTree(nodes: { ref: string; title: string }[]): TreeResponse {
  return {
    generated_at: "2026-01-01T00:00:00Z",
    sources: [],
    live: nodes.map((n) => ({
      row_id: `row_${n.ref}`,
      ref: n.ref,
      host_id: "local",
      session_id: n.ref,
      title: n.title,
      project: "test-project",
      state: "idle",
      kind: "session",
      live: true,
      children: [],
    })),
    needs_you: [],
    pin_sections: [],
    projects: [],
    archived_projects: [],
    test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
  };
}

test("prefers the live thread name when hydrated", () => {
  const threads = new Map<string, ThreadModel>([["ref_a", { name: "Live name" } as ThreadModel]]);
  const tree = fixtureTree([{ ref: "ref_a", title: "Tree title" }]);
  expect(resolveThreadName(threads, tree, "ref_a")).toBe("Live name");
});

test("falls back to the tree store's title when no thread name is known yet", () => {
  const threads = new Map<string, ThreadModel>();
  const tree = fixtureTree([{ ref: "ref_a", title: "Tree title" }]);
  expect(resolveThreadName(threads, tree, "ref_a")).toBe("Tree title");
});

test("returns undefined when neither source has a name, leaving the raw ref as the caller's own last resort", () => {
  const threads = new Map<string, ThreadModel>();
  expect(resolveThreadName(threads, null, "ref_a")).toBeUndefined();
});
