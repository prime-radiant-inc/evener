// @vitest-environment node

import { describe, expect, test } from "vitest";
import type { TreeNode as ApiTreeNode, TreeProject as ApiTreeProject, TreeResponse } from "../../stores/tree";
import { applyPending, type PendingOp } from "./railPending";

function node(overrides: Partial<ApiTreeNode> = {}): ApiTreeNode {
  return {
    row_id: "r1",
    ref: "local:a",
    host_id: "local",
    session_id: "a",
    title: "Session A",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}

function project(overrides: Partial<ApiTreeProject> = {}): ApiTreeProject {
  return { key: "p1", name: "Proj", sessions: [], ...overrides };
}

function tree(overrides: Partial<TreeResponse> = {}): TreeResponse {
  return {
    generated_at: "2026-01-01T00:00:00Z",
    sources: [],
    live: [],
    needs_you: [],
    favorites: [],
    projects: [],
    archived_projects: [],
    test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
    ...overrides,
  };
}

const refsIn = (t: TreeResponse): string[] => {
  const out: string[] = [];
  const walk = (ns: ApiTreeNode[]) => {
    for (const n of ns) {
      out.push(n.ref);
      walk(n.children);
    }
  };
  walk(t.live);
  walk(t.needs_you);
  walk(t.favorites);
  for (const p of [...t.projects, ...t.archived_projects, ...t.test_runs]) walk(p.sessions);
  return out;
};

describe("applyPending", () => {
  test("no ops leaves the tree exactly as it was", () => {
    const t = tree({ projects: [project({ sessions: [node()] })] });
    expect(applyPending(t, [])).toBe(t);
  });

  // The store's tree must survive untouched: an op is dropped once its refresh
  // settles, and the very next render reads that same object. A surviving
  // node with children is the case that matters - a node the op removes is
  // trivially unharmed, so a fixture without survivors proves nothing.
  test("never mutates the tree it was given", () => {
    const original = tree({
      live: [node({ ref: "local:keep", row_id: "rk" })],
      projects: [
        project({
          sessions: [
            node({ ref: "local:parent", children: [node({ ref: "local:gone", row_id: "r2" })] }),
            node({ ref: "local:other", row_id: "r3", favorite: false, title: "untouched" }),
          ],
        }),
      ],
    });
    const snapshot = JSON.stringify(original);
    applyPending(original, [
      { kind: "hideSession", ref: "local:gone" },
      { kind: "sessionFavorite", ref: "local:other", value: true },
      { kind: "sessionTitle", ref: "local:keep", title: "changed" },
    ]);
    expect(JSON.stringify(original)).toBe(snapshot);
  });

  describe("hideSession", () => {
    test("drops the row from its project", () => {
      const t = tree({
        projects: [project({ sessions: [node({ ref: "local:a" }), node({ ref: "local:b", row_id: "r2" })] })],
      });
      const got = applyPending(t, [{ kind: "hideSession", ref: "local:a" }]);
      expect(refsIn(got)).toEqual(["local:b"]);
    });

    // A session can be listed in several tiers at once (Live and its project,
    // for instance). Archiving it must clear every listing, or it vanishes
    // from one place and lingers in another.
    test("drops the row from every tier it appears in", () => {
      const n = node({ ref: "local:a" });
      const t = tree({ live: [n], favorites: [n], projects: [project({ sessions: [n] })] });
      expect(refsIn(applyPending(t, [{ kind: "hideSession", ref: "local:a" }]))).toEqual([]);
    });

    test("drops a nested child without disturbing its parent", () => {
      const t = tree({
        projects: [
          project({
            sessions: [node({ ref: "local:parent", children: [node({ ref: "local:kid", row_id: "r2" })] })],
          }),
        ],
      });
      expect(refsIn(applyPending(t, [{ kind: "hideSession", ref: "local:kid" }]))).toEqual(["local:parent"]);
    });

    test("a ref that isn't in the tree changes nothing", () => {
      const t = tree({ projects: [project({ sessions: [node({ ref: "local:a" })] })] });
      expect(refsIn(applyPending(t, [{ kind: "hideSession", ref: "local:nope" }]))).toEqual(["local:a"]);
    });
  });

  describe("hideProject", () => {
    test("drops the whole project", () => {
      const t = tree({ projects: [project({ key: "p1" }), project({ key: "p2" })] });
      const got = applyPending(t, [{ kind: "hideProject", key: "p1" }]);
      expect(got.projects.map((p) => p.key)).toEqual(["p2"]);
    });

    test("drops it from test runs too, wherever it was listed", () => {
      const t = tree({ test_runs: [project({ key: "tr1" })] });
      expect(applyPending(t, [{ kind: "hideProject", key: "tr1" }]).test_runs).toEqual([]);
    });
  });

  describe("sessionFavorite", () => {
    test("sets the flag on the row", () => {
      const t = tree({ projects: [project({ sessions: [node({ ref: "local:a", favorite: false })] })] });
      const got = applyPending(t, [{ kind: "sessionFavorite", ref: "local:a", value: true }]);
      expect(got.projects[0]?.sessions[0]?.favorite).toBe(true);
    });

    test("clears the flag too, so unpinning shows immediately", () => {
      const t = tree({ projects: [project({ sessions: [node({ ref: "local:a", favorite: true })] })] });
      const got = applyPending(t, [{ kind: "sessionFavorite", ref: "local:a", value: false }]);
      expect(got.projects[0]?.sessions[0]?.favorite).toBe(false);
    });

    test("applies to every listing of the same ref, including nested ones", () => {
      const t = tree({
        live: [node({ ref: "local:a" })],
        projects: [
          project({ sessions: [node({ ref: "local:p", children: [node({ ref: "local:a", row_id: "r9" })] })] }),
        ],
      });
      const got = applyPending(t, [{ kind: "sessionFavorite", ref: "local:a", value: true }]);
      expect(got.live[0]?.favorite).toBe(true);
      expect(got.projects[0]?.sessions[0]?.children[0]?.favorite).toBe(true);
    });
  });

  describe("sessionTitle", () => {
    test("renames every listing of the ref", () => {
      const t = tree({
        live: [node({ ref: "local:a", title: "old" })],
        projects: [project({ sessions: [node({ ref: "local:a", title: "old" })] })],
      });
      const got = applyPending(t, [{ kind: "sessionTitle", ref: "local:a", title: "new" }]);
      expect(got.live[0]?.title).toBe("new");
      expect(got.projects[0]?.sessions[0]?.title).toBe("new");
    });
  });

  describe("projectFavorite", () => {
    test("sets the flag on the project", () => {
      const t = tree({ projects: [project({ key: "p1", favorite: false })] });
      expect(applyPending(t, [{ kind: "projectFavorite", key: "p1", value: true }]).projects[0]?.favorite).toBe(true);
    });
  });

  test("applies several ops in order", () => {
    const t = tree({
      projects: [project({ sessions: [node({ ref: "local:a" }), node({ ref: "local:b", row_id: "r2" })] })],
    });
    const ops: PendingOp[] = [
      { kind: "hideSession", ref: "local:a" },
      { kind: "sessionTitle", ref: "local:b", title: "renamed" },
    ];
    const got = applyPending(t, ops);
    expect(refsIn(got)).toEqual(["local:b"]);
    expect(got.projects[0]?.sessions[0]?.title).toBe("renamed");
  });
});
