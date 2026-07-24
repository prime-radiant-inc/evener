import { describe, expect, test } from "vitest";
import type { TreeNode as ApiTreeNode, TreeProject as ApiTreeProject } from "../../stores/tree";
import {
  archivedProjectNodes,
  needsYouDescendantCount,
  overrideLookup,
  projectNodeIdForSessionRef,
  projectNodes,
  sessionNodes,
} from "./railNodes";

function node(overrides: Partial<ApiTreeNode> = {}): ApiTreeNode {
  return {
    row_id: "project:p1:local:a",
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
  return {
    key: "p1",
    name: "Proj",
    sessions: [],
    ...overrides,
  };
}

// Always-false lookup: every node renders collapsed unless a test overrides
// the callback directly - keeps each test's intent about expand state
// explicit rather than riding a shared default.
const NEVER_EXPANDED = () => false;

describe("sessionNodes", () => {
  test("maps each session's row_id to the rail node id and carries the source node", () => {
    const [rail] = sessionNodes([node({ row_id: "r1", title: "A" })], NEVER_EXPANDED);
    expect(rail).toMatchObject({ id: "r1", kind: "session" });
    expect(rail && "session" in rail && rail.session.title).toBe("A");
  });

  test("a session with no children renders as a leaf (empty children array)", () => {
    const [rail] = sessionNodes([node({ children: [] })], NEVER_EXPANDED);
    expect(rail?.children).toEqual([]);
  });

  test("a session's nested children (subagent clusters) recurse into session rail nodes", () => {
    const [rail] = sessionNodes(
      [node({ row_id: "parent", children: [node({ row_id: "child", title: "Subagent" })] })],
      NEVER_EXPANDED,
    );
    expect(rail?.children).toHaveLength(1);
    expect(rail?.children?.[0]).toMatchObject({ id: "child", kind: "session" });
  });

  test("expanded state for a branch session comes from the isExpanded callback, keyed by row_id, defaulting to false", () => {
    const isExpanded = overrideLookup(new Map([["parent", true]]));
    const [rail] = sessionNodes([node({ row_id: "parent", children: [node({ row_id: "child" })] })], isExpanded);
    expect(rail?.expanded).toBe(true);
  });

  test("multiple top-level sessions preserve input order", () => {
    const rails = sessionNodes([node({ row_id: "r1" }), node({ row_id: "r2" })], NEVER_EXPANDED);
    expect(rails.map((r) => r.id)).toEqual(["r1", "r2"]);
  });
});

describe("needsYouDescendantCount", () => {
  test("counts needs-you nodes in the subtree, not the node itself", () => {
    const root = node({
      row_id: "s1",
      ref: "r1",
      state: "active",
      children: [
        node({ row_id: "s1a", ref: "r1a", state: "awaiting" }),
        node({
          row_id: "s1b",
          ref: "r1b",
          state: "warning",
          children: [node({ row_id: "s1b1", ref: "r1b1", state: "active" })],
        }),
      ],
    });
    expect(needsYouDescendantCount(root)).toBe(2); // s1a + s1b, not the active root or grandchild
  });

  test("a leaf node (no children) counts zero", () => {
    expect(needsYouDescendantCount(node({ row_id: "leaf", state: "awaiting" }))).toBe(0);
  });
});

describe("projectNodes", () => {
  test("ids are namespaced separately from any session row_id so they never collide within a section", () => {
    const [rail] = projectNodes([project({ key: "p1" })], NEVER_EXPANDED);
    expect(rail?.id).not.toBe("p1");
    expect(rail?.kind).toBe("project");
  });

  test("children come directly from project.sessions, mapped to session rail nodes", () => {
    const [rail] = projectNodes(
      [project({ sessions: [node({ row_id: "r1" }), node({ row_id: "r2" })] })],
      NEVER_EXPANDED,
    );
    expect(rail?.children?.map((c) => c.id)).toEqual(["r1", "r2"]);
  });

  test("a project with no sessions renders as a leaf", () => {
    const [rail] = projectNodes([project({ sessions: [] })], NEVER_EXPANDED);
    expect(rail?.children).toEqual([]);
  });

  test("sorts a project's sessions needs-you-first, preserving relative order otherwise (vbh8)", () => {
    const [rail] = projectNodes(
      [
        project({
          sessions: [
            node({ row_id: "idle1", ref: "r1", state: "idle" }),
            node({ row_id: "needsYou1", ref: "r2", state: "awaiting" }),
            node({ row_id: "idle2", ref: "r3", state: "idle" }),
            // Needs-you via a descendant, not its own state - still sorts first.
            node({
              row_id: "hasNeedsYouChild",
              ref: "r4",
              state: "active",
              children: [node({ ref: "r4a", state: "warning" })],
            }),
          ],
        }),
      ],
      NEVER_EXPANDED,
    );
    expect(rail?.children?.map((c) => c.id)).toEqual(["needsYou1", "hasNeedsYouChild", "idle1", "idle2"]);
  });

  test("expanded defaults to the project's own default_expanded wire field", () => {
    // An empty override map falls back to each call's own default (see
    // overrideLookup) - NEVER_EXPANDED would ignore the default entirely,
    // which is the wrong double to prove a default is actually being read.
    const noOverrides = overrideLookup(new Map());
    const [expandedByDefault] = projectNodes([project({ key: "p1", default_expanded: true })], noOverrides);
    expect(expandedByDefault?.expanded).toBe(true);

    const [collapsedByDefault] = projectNodes([project({ key: "p2", default_expanded: false })], noOverrides);
    expect(collapsedByDefault?.expanded).toBe(false);
  });

  test("an explicit isExpanded override wins over default_expanded", () => {
    const rails = projectNodes([project({ key: "p1", default_expanded: true })], () => false);
    expect(rails[0]?.expanded).toBe(false);
  });
});

describe("archivedProjectNodes", () => {
  test("a project not yet in projectDetails, with sessions to load, gets one loading placeholder child", () => {
    const [rail] = archivedProjectNodes([project({ key: "p1", session_count: 3 })], new Map(), NEVER_EXPANDED);
    expect(rail?.children).toHaveLength(1);
    expect(rail?.children?.[0]?.kind).toBe("loading");
  });

  test("a project not yet in projectDetails with zero sessions gets no children at all", () => {
    const [rail] = archivedProjectNodes([project({ key: "p1", session_count: 0 })], new Map(), NEVER_EXPANDED);
    expect(rail?.children).toEqual([]);
  });

  test("a project present in projectDetails renders its real, hydrated sessions instead of a placeholder", () => {
    const detail = project({ key: "p1", session_count: 2, sessions: [node({ row_id: "r1" }), node({ row_id: "r2" })] });
    const [rail] = archivedProjectNodes(
      [project({ key: "p1", session_count: 2 })],
      new Map([["p1", detail]]),
      NEVER_EXPANDED,
    );
    expect(rail?.children?.map((c) => c.id)).toEqual(["r1", "r2"]);
  });

  test("an archived project ignores default_expanded and starts collapsed regardless", () => {
    const [rail] = archivedProjectNodes([project({ key: "p1", default_expanded: true })], new Map(), NEVER_EXPANDED);
    expect(rail?.expanded).toBe(false);
  });

  test("an explicit isExpanded override still applies to an archived project", () => {
    // The rail node id is namespaced (see the projectNodes id test above)
    // and private to this module - discover it via an unrelated call first
    // rather than hardcoding the private id format in the test.
    const [unexpanded] = archivedProjectNodes([project({ key: "p1" })], new Map(), NEVER_EXPANDED);
    const isExpanded = overrideLookup(new Map([[unexpanded!.id, true]]));
    const [rail] = archivedProjectNodes([project({ key: "p1" })], new Map(), isExpanded);
    expect(unexpanded?.id).toBe(rail?.id);
    expect(rail?.expanded).toBe(true);
  });
});

describe("overrideLookup", () => {
  test("returns the override when present, otherwise the given default", () => {
    const isExpanded = overrideLookup(
      new Map([
        ["a", true],
        ["b", false],
      ]),
    );
    expect(isExpanded("a", false)).toBe(true);
    expect(isExpanded("b", true)).toBe(false);
    expect(isExpanded("c", true)).toBe(true);
    expect(isExpanded("c", false)).toBe(false);
  });
});

describe("projectNodeIdForSessionRef", () => {
  test("returns the projectnode: id matching what projectNodes assigns for a session under a project", () => {
    const projects = [project({ key: "p1", sessions: [node({ ref: "local:a" })] })];
    // Same id projectNodes would put on the branch, so Rail's reveal expands
    // exactly that node's override entry.
    expect(projectNodeIdForSessionRef(projects, "local:a")).toBe("projectnode:p1");
    const [rail] = projectNodes(projects, () => false);
    expect(projectNodeIdForSessionRef(projects, "local:a")).toBe(rail?.id);
  });

  test("finds a session nested in a subagent cluster under the project", () => {
    const projects = [
      project({ key: "p2", sessions: [node({ ref: "local:parent", children: [node({ ref: "local:child" })] })] }),
    ];
    expect(projectNodeIdForSessionRef(projects, "local:child")).toBe("projectnode:p2");
  });

  test("returns null for a ref that belongs to no project (a top-level tier entry)", () => {
    const projects = [project({ key: "p1", sessions: [node({ ref: "local:a" })] })];
    expect(projectNodeIdForSessionRef(projects, "local:elsewhere")).toBeNull();
  });

  test("returns the FIRST matching project's id and does not match a different project", () => {
    const projects = [
      project({ key: "p1", sessions: [node({ ref: "local:a" })] }),
      project({ key: "p2", sessions: [node({ ref: "local:b" })] }),
    ];
    expect(projectNodeIdForSessionRef(projects, "local:b")).toBe("projectnode:p2");
  });
});
