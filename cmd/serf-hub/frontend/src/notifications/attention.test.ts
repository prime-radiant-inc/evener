import { describe, expect, test } from "vitest";
import type { TreeNode, TreeResponse } from "../stores/tree";
import { type AttentionEntry, detectFires, levelFromState, snapshotFromTree } from "./attention";

// Wire-true node: every field GET /api/tree's apiTreeNode/apiTreeNodeTier
// stamps on a NeedsYou-tier row (cmd/serf-hub/web_api_tree.go:648-708). The
// engine only reads ref/state/title/ask_pending, but the fixture carries the
// full shape so a future field rename in the Go handler surfaces here rather
// than hiding behind a synthetic stub (the W5 "trace the daemon shape first"
// discipline). `state` is already the normalized UI state
// (tree.go:236-259): "awaiting"|"warning" ⇒ needs_you, "errored" ⇒ error.
function node(overrides: Partial<TreeNode> & Pick<TreeNode, "ref" | "state">): TreeNode {
  return {
    row_id: `needsyou:${overrides.ref}`,
    host_id: "local",
    session_id: overrides.ref.replace(/^local:/, ""),
    title: overrides.ref,
    project: "proj",
    kind: "session",
    tier: "needsyou",
    live: true,
    children: [],
    ...overrides,
  };
}

function tree(needsYou: TreeNode[]): TreeResponse {
  return {
    generated_at: "2026-01-01T00:00:00Z",
    sources: [],
    live: [],
    needs_you: needsYou,
    favorites: [],
    projects: [],
    archived_projects: [],
    test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
  };
}

describe("levelFromState", () => {
  // Mirrors the daemon's attentionLevel(NormalizeState(status)) exactly
  // (attention.go:53-64) so a per-thread level derived on the client agrees
  // with the server's own badge bucketing.
  test("active is working", () => expect(levelFromState("active")).toBe("working"));
  test("awaiting is needs_you", () => expect(levelFromState("awaiting")).toBe("needs_you"));
  test("warning is needs_you", () => expect(levelFromState("warning")).toBe("needs_you"));
  test("errored is error", () => expect(levelFromState("errored")).toBe("error"));
  test("idle is idle", () => expect(levelFromState("idle")).toBe("idle"));
  test("ended is idle", () => expect(levelFromState("ended")).toBe("idle"));
  test("unknown is idle", () => expect(levelFromState("notLoaded")).toBe("idle"));
});

describe("snapshotFromTree", () => {
  test("null tree is an empty snapshot", () => {
    expect(snapshotFromTree(null).size).toBe(0);
  });

  test("keys by ref, carrying level + askPending + title", () => {
    const snap = snapshotFromTree(
      tree([
        node({ ref: "local:a", state: "awaiting", title: "Ask A", ask_pending: true }),
        node({ ref: "local:b", state: "errored", title: "Err B" }),
      ]),
    );
    expect(snap.get("local:a")).toEqual<AttentionEntry>({
      ref: "local:a",
      title: "Ask A",
      level: "needs_you",
      askPending: true,
    });
    expect(snap.get("local:b")).toEqual<AttentionEntry>({
      ref: "local:b",
      title: "Err B",
      level: "error",
      askPending: false,
    });
  });

  test("warning maps into the needs_you level", () => {
    const snap = snapshotFromTree(tree([node({ ref: "local:w", state: "warning" })]));
    expect(snap.get("local:w")?.level).toBe("needs_you");
  });
});

describe("detectFires", () => {
  const asks = "asks" as const;
  const all = "all" as const;

  function snap(...nodes: TreeNode[]): Map<string, AttentionEntry> {
    return snapshotFromTree(tree(nodes));
  }

  test("a ref newly in the tier is a transition into the alarming set", () => {
    const prev = snap();
    const next = snap(node({ ref: "local:a", state: "awaiting", ask_pending: true }));
    expect(detectFires(prev, next, asks).map((e) => e.ref)).toEqual(["local:a"]);
  });

  test("a ref already in the tier does not re-fire", () => {
    const prev = snap(node({ ref: "local:a", state: "awaiting", ask_pending: true }));
    const next = snap(node({ ref: "local:a", state: "awaiting", ask_pending: true }));
    expect(detectFires(prev, next, all)).toEqual([]);
  });

  test("error->needs_you within the tier does not fire (stayed alarming)", () => {
    const prev = snap(node({ ref: "local:a", state: "errored" }));
    const next = snap(node({ ref: "local:a", state: "awaiting" }));
    expect(detectFires(prev, next, all)).toEqual([]);
  });

  test("dropping out of the tier does not fire", () => {
    const prev = snap(node({ ref: "local:a", state: "awaiting" }));
    const next = snap();
    expect(detectFires(prev, next, all)).toEqual([]);
  });

  describe("loudScope narrowing", () => {
    test("asks: a plain your-move needs_you (no ask, not error) is silent", () => {
      const prev = snap();
      const next = snap(node({ ref: "local:a", state: "awaiting", ask_pending: false }));
      expect(detectFires(prev, next, asks)).toEqual([]);
    });

    test("asks: an ask_pending transition fires", () => {
      const prev = snap();
      const next = snap(node({ ref: "local:a", state: "awaiting", ask_pending: true }));
      expect(detectFires(prev, next, asks).map((e) => e.ref)).toEqual(["local:a"]);
    });

    test("asks: an error transition fires even without ask_pending", () => {
      const prev = snap();
      const next = snap(node({ ref: "local:e", state: "errored", ask_pending: false }));
      expect(detectFires(prev, next, asks).map((e) => e.ref)).toEqual(["local:e"]);
    });

    test("all: a plain your-move needs_you fires", () => {
      const prev = snap();
      const next = snap(node({ ref: "local:a", state: "awaiting", ask_pending: false }));
      expect(detectFires(prev, next, all).map((e) => e.ref)).toEqual(["local:a"]);
    });
  });

  test("multiple simultaneous transitions each fire under all", () => {
    const prev = snap(node({ ref: "local:a", state: "awaiting" }));
    const next = snap(
      node({ ref: "local:a", state: "awaiting" }),
      node({ ref: "local:b", state: "awaiting", ask_pending: false }),
      node({ ref: "local:c", state: "errored" }),
    );
    expect(
      detectFires(prev, next, all)
        .map((e) => e.ref)
        .sort(),
    ).toEqual(["local:b", "local:c"]);
  });
});
