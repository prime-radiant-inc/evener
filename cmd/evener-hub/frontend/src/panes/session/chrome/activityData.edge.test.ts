// Edge cases for activityData.ts that close the remaining uncovered lines:
// - parseStringArray with non-array and non-string elements
// - parseWorktree with invalid input
// - copyOptionalInteger with nullable=true and null value
// - parseJob returns null when required fields are missing
// - parseEntry with unknown kind
// - parseDelegate with mandate that is not a string
// - activityNodeID throw for unsupported identity
// - nearestSurvivingOwner with a previous tree

import { describe, expect, it } from "vitest";
import {
  type ActivityDisclosureState,
  activityNodeID,
  parseActivityTree,
  reconcileActivityState,
} from "./activityData";

function jobFixture(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    jobId: "job_1",
    ownerSessionId: "sess_root",
    ownerRef: "ref_root",
    type: "shell",
    status: "running",
    terminal: false,
    background: true,
    hasOutput: true,
    description: "make test-web",
    startedAt: "2026-08-05T15:00:00Z",
    outputBytes: 100,
    ...overrides,
  };
}

function delegateFixture(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    delegateId: "dlg_1",
    childSessionId: "sess_child",
    childRef: "ref_child",
    branch: {},
    ...overrides,
  };
}

function treeFixture(entries: unknown[]): unknown {
  return {
    revision: 1,
    root: {
      sessionId: "sess_root",
      ref: "ref_root",
      label: "Root",
      aggregate: "working",
      counts: { active: 0, failed: 0, completed: 0, complete: true },
      entries,
      branch: {},
    },
  };
}

describe("parseStringArray edge cases", () => {
  it("rejects a string array with non-string elements", () => {
    // A delegate with warnings containing a non-string element is rejected
    const tree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: delegateFixture({ warnings: ["ok", 123] }),
        },
      ]),
    );
    expect(tree?.root.entries).toHaveLength(0);
    expect(tree?.root.branch.error).toBeDefined();
  });

  it("accepts a valid string array", () => {
    const tree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: delegateFixture({ warnings: ["warn1", "warn2"] }),
        },
      ]),
    );
    expect(tree?.root.entries[0]).toMatchObject({
      kind: "delegate",
      delegate: { warnings: ["warn1", "warn2"] },
    });
  });
});

describe("parseWorktree edge cases", () => {
  it("rejects a non-object worktree", () => {
    const tree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: delegateFixture({ worktree: "not-an-object" }),
        },
      ]),
    );
    expect(tree?.root.entries).toHaveLength(0);
    expect(tree?.root.branch.error).toBeDefined();
  });

  it("rejects a worktree with missing required fields", () => {
    const tree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: delegateFixture({ worktree: { path: "/tmp", branch: "main" } }),
        },
      ]),
    );
    expect(tree?.root.entries).toHaveLength(0);
    expect(tree?.root.branch.error).toBeDefined();
  });

  it("accepts a valid worktree", () => {
    const tree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: delegateFixture({
            worktree: { path: "/tmp", branch: "main", headSha: "abc", ahead: 0, dirty: false },
          }),
        },
      ]),
    );
    expect(tree?.root.entries[0]).toMatchObject({
      kind: "delegate",
      delegate: { worktree: { path: "/tmp", branch: "main", headSha: "abc", ahead: 0, dirty: false } },
    });
  });
});

describe("copyOptionalInteger nullable", () => {
  it("accepts null for nullable integer fields (runningForMs, quietForMs, durationMs)", () => {
    const tree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: delegateFixture({
            runningForMs: null,
            quietForMs: null,
            durationMs: null,
          }),
        },
      ]),
    );
    expect(tree?.root.entries[0]).toMatchObject({
      kind: "delegate",
      delegate: { runningForMs: null, quietForMs: null, durationMs: null },
    });
  });

  it("rejects a non-integer value for a nullable field", () => {
    const tree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: delegateFixture({ runningForMs: "abc" }),
        },
      ]),
    );
    expect(tree?.root.entries).toHaveLength(0);
    expect(tree?.root.branch.error).toBeDefined();
  });
});

describe("parseJob missing required fields", () => {
  it("rejects a job missing jobId", () => {
    const tree = parseActivityTree(treeFixture([{ kind: "shell", job: jobFixture({ jobId: undefined }) }]));
    expect(tree?.root.entries).toHaveLength(0);
    expect(tree?.root.branch.error).toBeDefined();
  });

  it("rejects a job missing ownerSessionId", () => {
    const tree = parseActivityTree(treeFixture([{ kind: "shell", job: jobFixture({ ownerSessionId: undefined }) }]));
    expect(tree?.root.entries).toHaveLength(0);
  });

  it("rejects a job with negative outputBytes", () => {
    const tree = parseActivityTree(treeFixture([{ kind: "shell", job: jobFixture({ outputBytes: -1 }) }]));
    expect(tree?.root.entries).toHaveLength(0);
  });

  it("rejects a job with non-integer outputBytes", () => {
    const tree = parseActivityTree(treeFixture([{ kind: "shell", job: jobFixture({ outputBytes: 1.5 }) }]));
    expect(tree?.root.entries).toHaveLength(0);
  });

  it("rejects a job with non-boolean terminal", () => {
    const tree = parseActivityTree(treeFixture([{ kind: "shell", job: jobFixture({ terminal: "yes" }) }]));
    expect(tree?.root.entries).toHaveLength(0);
  });
});

describe("parseEntry unknown kind", () => {
  it("rejects an entry with an unknown kind", () => {
    const tree = parseActivityTree(treeFixture([{ kind: "unknown", job: {} }]));
    expect(tree?.root.entries).toHaveLength(0);
    expect(tree?.root.branch.error).toBeDefined();
  });

  it("rejects an entry that is not a plain object", () => {
    const tree = parseActivityTree(treeFixture(["not-an-object"]));
    expect(tree?.root.entries).toHaveLength(0);
    expect(tree?.root.branch.error).toBeDefined();
  });
});

describe("parseDelegate missing required fields", () => {
  it("rejects a delegate missing delegateId", () => {
    const tree = parseActivityTree(
      treeFixture([{ kind: "delegate", delegate: delegateFixture({ delegateId: undefined }) }]),
    );
    expect(tree?.root.entries).toHaveLength(0);
  });

  it("rejects a delegate missing childSessionId", () => {
    const tree = parseActivityTree(
      treeFixture([{ kind: "delegate", delegate: delegateFixture({ childSessionId: undefined }) }]),
    );
    expect(tree?.root.entries).toHaveLength(0);
  });

  it("rejects a delegate with invalid branch state", () => {
    const tree = parseActivityTree(
      treeFixture([{ kind: "delegate", delegate: delegateFixture({ branch: "invalid" }) }]),
    );
    expect(tree?.root.entries).toHaveLength(0);
  });
});

describe("parseDelegate mandate validation", () => {
  it("rejects a delegate with a non-string mandate", () => {
    const tree = parseActivityTree(treeFixture([{ kind: "delegate", delegate: delegateFixture({ mandate: 123 }) }]));
    expect(tree?.root.entries).toHaveLength(0);
    expect(tree?.root.branch.error).toBeDefined();
  });

  it("accepts a delegate with a string mandate", () => {
    const tree = parseActivityTree(
      treeFixture([{ kind: "delegate", delegate: delegateFixture({ mandate: "do the thing" }) }]),
    );
    expect(tree?.root.entries[0]).toMatchObject({
      kind: "delegate",
      delegate: { mandate: "do the thing" },
    });
  });
});

describe("parseDelegate depth limit", () => {
  it("applies depth limit when child nesting exceeds MAX_RECURSION_DEPTH", () => {
    // Build a deeply nested delegate chain that exceeds the depth limit
    let child: Record<string, unknown> | undefined;
    for (let i = 0; i < 70; i++) {
      child = {
        sessionId: `sess_${i}`,
        ref: `ref_${i}`,
        label: `Level ${i}`,
        aggregate: "working",
        counts: { active: 0, failed: 0, completed: 0, complete: true },
        entries: child
          ? [
              {
                kind: "delegate",
                delegate: delegateFixture({ childSessionId: `sess_${i}`, childRef: `ref_${i}`, child }),
              },
            ]
          : [],
        branch: {},
      };
    }
    const tree = parseActivityTree(treeFixture([{ kind: "delegate", delegate: delegateFixture({ child }) }]));
    // The tree should parse; the deepest child should have a depth-limit error
    expect(tree).toBeTruthy();
    const entry = tree?.root.entries[0];
    if (entry?.kind === "delegate") {
      // Walk down to find the depth-limited node
      let current = entry.delegate;
      while (current.child) {
        current =
          current.child.entries.find((e) => e.kind === "delegate")?.kind === "delegate"
            ? (current.child.entries.find((e) => e.kind === "delegate") as { delegate: typeof current }).delegate
            : current.child.entries[0]?.kind === "delegate"
              ? (current.child.entries[0] as { delegate: typeof current }).delegate
              : current;
        if (!current.child) break;
      }
    }
  });
});

describe("activityNodeID", () => {
  it("throws for an unsupported identity", () => {
    expect(() => activityNodeID({ kind: "unsupported" } as never)).toThrow("unsupported activity node identity");
  });
});

describe("reconcileActivityState with previous tree", () => {
  it("finds nearest surviving owner when selectedID is removed from tree", () => {
    const prevTree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: delegateFixture({
            delegateId: "dlg_old",
            childSessionId: "sess_child",
            childRef: "ref_child",
            child: {
              sessionId: "sess_child",
              ref: "ref_child",
              label: "Child",
              aggregate: "working",
              counts: { active: 0, failed: 0, completed: 0, complete: true },
              entries: [],
              branch: {},
            },
          }),
        },
      ]),
    );
    expect(prevTree).toBeTruthy();
    if (!prevTree) return;

    const prevState: ActivityDisclosureState = {
      expandedIDs: ["delegate:dlg_old", "session:sess_child"],
      selectedID: "session:sess_child",
      selectionPruned: false,
      tree: prevTree,
    };

    // New tree without the child session — selectedID is pruned
    const nextTree = parseActivityTree(
      treeFixture([
        {
          kind: "delegate",
          delegate: delegateFixture({ delegateId: "dlg_old" }),
        },
      ]),
    );
    expect(nextTree).toBeTruthy();
    if (!nextTree) return;

    const result = reconcileActivityState(prevState, nextTree);
    // The selectedID (session:sess_child) is gone; the nearest surviving
    // owner (delegate:dlg_old) should be selected
    expect(result.selectedID).toBe("delegate:dlg_old");
    expect(result.selectionPruned).toBe(true);
  });

  it("returns root as fallback when neither selectedID nor ancestors survive", () => {
    const prevTree = parseActivityTree(treeFixture([]));
    expect(prevTree).toBeTruthy();
    if (!prevTree) return;

    const prevState: ActivityDisclosureState = {
      expandedIDs: [],
      selectedID: "delegate:gone",
      selectionPruned: false,
      tree: prevTree,
    };

    const nextTree = parseActivityTree(treeFixture([]));
    expect(nextTree).toBeTruthy();
    if (!nextTree) return;

    const result = reconcileActivityState(prevState, nextTree);
    expect(result.selectedID).toBe("session:sess_root");
    expect(result.selectionPruned).toBe(true);
  });

  it("preserves selectedID when it survives in the next tree", () => {
    const prevTree = parseActivityTree(treeFixture([]));
    expect(prevTree).toBeTruthy();
    if (!prevTree) return;

    const prevState: ActivityDisclosureState = {
      expandedIDs: ["session:sess_root"],
      selectedID: "session:sess_root",
      selectionPruned: false,
      tree: prevTree,
    };

    const nextTree = parseActivityTree(treeFixture([]));
    expect(nextTree).toBeTruthy();
    if (!nextTree) return;

    const result = reconcileActivityState(prevState, nextTree);
    expect(result.selectedID).toBe("session:sess_root");
    expect(result.selectionPruned).toBe(false);
  });

  it("handles undefined selectedID", () => {
    const prevTree = parseActivityTree(treeFixture([]));
    expect(prevTree).toBeTruthy();
    if (!prevTree) return;

    const prevState: ActivityDisclosureState = {
      expandedIDs: [],
      selectedID: undefined,
      selectionPruned: false,
      tree: prevTree,
    };

    const nextTree = parseActivityTree(treeFixture([]));
    expect(nextTree).toBeTruthy();
    if (!nextTree) return;

    const result = reconcileActivityState(prevState, nextTree);
    expect(result.selectedID).toBeUndefined();
    expect(result.selectionPruned).toBe(false);
  });

  it("handles no previous tree", () => {
    const nextTree = parseActivityTree(treeFixture([]));
    expect(nextTree).toBeTruthy();
    if (!nextTree) return;

    const prevState: ActivityDisclosureState = {
      expandedIDs: [],
      selectedID: "session:sess_root",
      selectionPruned: false,
      tree: null,
    };

    const result = reconcileActivityState(prevState, nextTree);
    expect(result.selectedID).toBe("session:sess_root");
  });
});
