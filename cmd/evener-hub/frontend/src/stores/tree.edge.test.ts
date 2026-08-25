// Edge cases for tree.ts that close remaining uncovered lines:
// - tierField "recent" and "archived" cases (lines 234, 236)

import { describe, expect, test } from "vitest";
import { mergeProjectPage, type TreeNode, type TreeProject, type TreeProjectPage, type TreeTier } from "./tree";

function makeNode(rowId: string, tier: TreeTier): TreeNode {
  return {
    row_id: rowId,
    session_id: `sess_${rowId}`,
    host_id: "local",
    title: `Session ${rowId}`,
    ref: `ref_${rowId}`,
    project: "/tmp/project",
    state: "idle",
    kind: "session",
    tier,
    live: true,
    children: [],
  };
}

function makeProject(sessions: TreeNode[]): TreeProject {
  return {
    key: "/tmp/project",
    name: "Project",
    sessions,
    more_current: 0,
    more_recent: 0,
    more_archived: 0,
  };
}

function makePage(tier: TreeTier, sessions: TreeNode[], offset: number): TreeProjectPage {
  return {
    key: "/tmp/project",
    tier,
    sessions,
    offset,
    remaining: 0,
  };
}

describe("mergeProjectPage tier coverage", () => {
  test("recent tier page updates more_recent flag", () => {
    const project = makeProject([makeNode("r1", "current")]);
    const page = makePage("recent", [makeNode("r2", "recent")], 0);
    page.remaining = 1;
    const result = mergeProjectPage(project, page);
    expect(result.more_recent).toBe(1);
    expect(result.more_current).toBe(0);
    expect(result.more_archived).toBe(0);
    expect(result.sessions.map((session) => session.row_id)).toEqual(["r1", "r2"]);
  });

  test("archived tier page updates more_archived flag", () => {
    const project = makeProject([makeNode("r1", "current")]);
    const page = makePage("archived", [makeNode("r2", "archived")], 0);
    page.remaining = 1;
    const result = mergeProjectPage(project, page);
    expect(result.more_archived).toBe(1);
    expect(result.more_current).toBe(0);
    expect(result.more_recent).toBe(0);
    expect(result.sessions.map((session) => session.row_id)).toEqual(["r1", "r2"]);
  });

  test("current tier page updates more_current flag", () => {
    const project = makeProject([makeNode("r1", "current")]);
    const page = makePage("current", [makeNode("r2", "current")], 0);
    page.remaining = 1;
    const result = mergeProjectPage(project, page);
    expect(result.more_current).toBe(1);
    expect(result.more_recent).toBe(0);
    expect(result.more_archived).toBe(0);
    expect(result.sessions.map((session) => session.row_id)).toEqual(["r2", "r1"]);
  });

  test("all three tiers can coexist in one project", () => {
    const project = makeProject([makeNode("c1", "current"), makeNode("r1", "recent"), makeNode("a1", "archived")]);
    const page = makePage("recent", [makeNode("r2", "recent")], 1);
    page.remaining = 1;
    const result = mergeProjectPage(project, page);
    expect(result.more_recent).toBe(1);
    expect(result.sessions.map(({ row_id, tier }) => ({ row_id, tier }))).toEqual([
      { row_id: "c1", tier: "current" },
      { row_id: "r1", tier: "recent" },
      { row_id: "r2", tier: "recent" },
      { row_id: "a1", tier: "archived" },
    ]);
  });

  test("existing row in page replaces the matching row by row_id", () => {
    const project = makeProject([makeNode("r1", "recent")]);
    // Page contains an updated version of r1 (same row_id, different title)
    const updatedNode = makeNode("r1", "recent");
    updatedNode.title = "Updated Session r1";
    const page = makePage("recent", [updatedNode], 0);
    const result = mergeProjectPage(project, page);
    expect(result.sessions).toHaveLength(1);
    expect(result.sessions[0]?.title).toBe("Updated Session r1");
  });
});
