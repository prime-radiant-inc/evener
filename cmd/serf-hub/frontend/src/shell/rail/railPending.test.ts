// @vitest-environment node

import { describe, expect, test } from "vitest";
import type {
  TreeNode as ApiTreeNode,
  TreeProject as ApiTreeProject,
  PinSectionTree,
  TreeResponse,
} from "../../stores/tree";
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
    pin_sections: [],
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
  for (const section of t.pin_sections) walk(section.sessions);
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
      { kind: "sessionPin", ref: "local:other", section: { id: "other", name: "Other", member_count: 1 } },
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
      const t = tree({
        live: [n],
        pin_sections: [{ id: "old", name: "Old", sessions: [n] }],
        projects: [project({ sessions: [n] })],
      });
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

  describe("named session pins", () => {
    function duplicateFixture(): TreeResponse {
      const liveCopy = node({ row_id: "live:a", ref: "local:a", title: "Authoritative A", pin_section_id: "old" });
      const projectCopy = node({
        row_id: "project:a",
        ref: "local:a",
        title: "Authoritative A",
        pin_section_id: "old",
        children: [node({ row_id: "recursive:a", ref: "local:a", pin_section_id: "old" })],
      });
      const pinCopy = node({ row_id: "pin:old:a", ref: "local:a", title: "Authoritative A", pin_section_id: "old" });
      return tree({
        live: [liveCopy],
        projects: [project({ sessions: [projectCopy], favorite: true })],
        pin_sections: [
          { id: "old", name: "Old", sessions: [node({ row_id: "old:first", ref: "local:first" }), pinCopy] },
          { id: "target", name: "Target", sessions: [node({ row_id: "target:keep", ref: "local:keep" })] },
        ],
      });
    }

    function allCopies(t: TreeResponse, ref: string): ApiTreeNode[] {
      const copies: ApiTreeNode[] = [];
      const walk = (nodes: ApiTreeNode[]) => {
        for (const n of nodes) {
          if (n.ref === ref) copies.push(n);
          walk(n.children);
        }
      };
      walk(t.live);
      walk(t.needs_you);
      for (const p of [...t.projects, ...t.archived_projects, ...t.test_runs]) walk(p.sessions);
      for (const section of t.pin_sections) walk(section.sessions);
      return copies;
    }

    const section = (t: TreeResponse, id: string): PinSectionTree | undefined =>
      t.pin_sections.find((candidate) => candidate.id === id);

    test("sessionPin moves all duplicate and recursive copies, removes the old row, and appends one authoritative real row", () => {
      const got = applyPending(duplicateFixture(), [
        { kind: "sessionPin", ref: "local:a", section: { id: "target", name: "Target", member_count: 2 } },
      ]);

      expect(allCopies(got, "local:a")).not.toHaveLength(0);
      expect(allCopies(got, "local:a").every((copy) => copy.pin_section_id === "target")).toBe(true);
      expect(section(got, "old")?.sessions.map((session) => session.ref)).toEqual(["local:first"]);
      expect(section(got, "target")?.sessions.map((session) => session.ref)).toEqual(["local:keep", "local:a"]);
      expect(section(got, "target")?.sessions[1]).toMatchObject({
        row_id: "live:a",
        title: "Authoritative A",
        pin_section_id: "target",
      });
    });

    test("sessionPin materializes a hidden empty target and keeps request-time ordering stable", () => {
      const source = duplicateFixture();
      source.pin_sections = source.pin_sections.filter((candidate) => candidate.id !== "target");

      const got = applyPending(source, [
        { kind: "sessionPin", ref: "local:a", section: { id: "hidden", name: "Hidden", member_count: 1 } },
      ]);

      expect(got.pin_sections.map((candidate) => candidate.id)).toEqual(["old", "hidden"]);
      expect(section(got, "old")?.sessions.map((session) => session.ref)).toEqual(["local:first"]);
      expect(section(got, "hidden")?.sessions.map((session) => session.ref)).toEqual(["local:a"]);
    });

    test("reapplying a pin to its current target neither duplicates nor reorders its row", () => {
      const source = duplicateFixture();
      source.pin_sections[0] = {
        ...source.pin_sections[0]!,
        sessions: [
          node({ row_id: "old:first", ref: "local:first" }),
          source.pin_sections[0]!.sessions[1]!,
          node({ row_id: "old:last", ref: "local:last" }),
        ],
      };
      const got = applyPending(source, [
        { kind: "sessionPin", ref: "local:a", section: { id: "old", name: "Old", member_count: 3 } },
      ]);
      expect(section(got, "old")?.sessions.map((session) => session.ref)).toEqual([
        "local:first",
        "local:a",
        "local:last",
      ]);
    });

    test("sessionUnpin removes the section row, clears every recursive copy, and retains a non-empty section", () => {
      const got = applyPending(duplicateFixture(), [{ kind: "sessionUnpin", ref: "local:a" }]);

      expect(allCopies(got, "local:a").every((copy) => copy.pin_section_id === undefined)).toBe(true);
      expect(section(got, "old")?.sessions.map((session) => session.ref)).toEqual(["local:first"]);
    });

    test("sessionUnpin hides a section only when its last visible row is removed", () => {
      const source = duplicateFixture();
      source.pin_sections[0] = { ...source.pin_sections[0]!, sessions: [source.pin_sections[0]!.sessions[1]!] };

      const got = applyPending(source, [{ kind: "sessionUnpin", ref: "local:a" }]);
      expect(got.pin_sections.map((candidate) => candidate.id)).toEqual(["target"]);
    });

    test("pinSectionRename preserves opaque identity, sessions, and current array order", () => {
      const source = duplicateFixture();
      const oldSessions = source.pin_sections[0]!.sessions;
      const got = applyPending(source, [{ kind: "pinSectionRename", id: "old", name: "Zebra" }]);

      expect(got.pin_sections.map((candidate) => candidate.id)).toEqual(["old", "target"]);
      expect(section(got, "old")).toMatchObject({ id: "old", name: "Zebra" });
      expect(section(got, "old")?.sessions).toEqual(oldSessions);
    });

    test("pinSectionDelete removes the section and clears every duplicate and recursive member flag", () => {
      const got = applyPending(duplicateFixture(), [{ kind: "pinSectionDelete", id: "old" }]);

      expect(got.pin_sections.map((candidate) => candidate.id)).toEqual(["target"]);
      expect(allCopies(got, "local:a").every((copy) => copy.pin_section_id === undefined)).toBe(true);
    });

    test("pinSectionDelete does not clear a nested copy assigned to another section", () => {
      const source = duplicateFixture();
      source.pin_sections[0]!.sessions[1]!.children = [
        node({ row_id: "other-child", ref: "local:other", pin_section_id: "target" }),
      ];
      source.projects[0]!.sessions.push(
        node({ row_id: "project:other", ref: "local:other", pin_section_id: "target" }),
      );

      const got = applyPending(source, [{ kind: "pinSectionDelete", id: "old" }]);
      expect(allCopies(got, "local:other").every((copy) => copy.pin_section_id === "target")).toBe(true);
    });

    test("named pin overlays leave project favorite state unchanged", () => {
      const source = duplicateFixture();
      const ops: PendingOp[] = [
        { kind: "sessionPin", ref: "local:a", section: { id: "target", name: "Target", member_count: 2 } },
        { kind: "sessionUnpin", ref: "local:a" },
        { kind: "pinSectionRename", id: "target", name: "Renamed" },
      ];
      expect(applyPending(source, ops).projects[0]?.favorite).toBe(true);
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
