import { describe, expect, test } from "vitest";
import type { TreeNode as ApiTreeNode, TreeProject as ApiTreeProject } from "../../stores/tree";
import { archivedProjectNodes, overrideLookup, projectNodes, sessionNodes } from "./railNodes";

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

describe("projectNodes", () => {
  test("ids are namespaced separately from any session row_id so they never collide within a section", () => {
    const [rail] = projectNodes([project({ key: "p1" })], NEVER_EXPANDED);
    expect(rail?.id).not.toBe("p1");
    expect(rail?.kind).toBe("project");
  });

  test("children come directly from project.sessions, mapped to session rail nodes", () => {
    const [rail] = projectNodes([project({ sessions: [node({ row_id: "r1" }), node({ row_id: "r2" })] })], NEVER_EXPANDED);
    expect(rail?.children?.map((c) => c.id)).toEqual(["r1", "r2"]);
  });

  test("a project with no sessions renders as a leaf", () => {
    const [rail] = projectNodes([project({ sessions: [] })], NEVER_EXPANDED);
    expect(rail?.children).toEqual([]);
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
    const [rail] = archivedProjectNodes([project({ key: "p1", session_count: 2 })], new Map([["p1", detail]]), NEVER_EXPANDED);
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
    const isExpanded = overrideLookup(new Map([["a", true], ["b", false]]));
    expect(isExpanded("a", false)).toBe(true);
    expect(isExpanded("b", true)).toBe(false);
    expect(isExpanded("c", true)).toBe(true);
    expect(isExpanded("c", false)).toBe(false);
  });
});
