// Edge cases for tree.ts that close remaining uncovered lines:
// - tierField "recent" and "archived" cases (lines 234, 236)

import { describe, expect, test } from "vitest";
import { mergeProjectPage, type TreeNode, type TreeProject, type TreeProjectPage } from "./tree";

function makeNode(rowId: string, tier: string): TreeNode {
  return {
    row_id: rowId,
    session_id: `sess_${rowId}`,
    tier: tier as TreeNode["tier"],
    label: `Session ${rowId}`,
    ref: `ref_${rowId}`,
    kind: "session",
    attention: false,
    archived: false,
    running: false,
    status_type: "idle",
  };
}

function makeProject(sessions: TreeNode[]): TreeProject {
  return {
    dir: "/tmp/project",
    label: "Project",
    sessions,
    more_current: false,
    more_recent: false,
    more_archived: false,
  };
}

function makePage(tier: string, sessions: TreeNode[], offset: number): TreeProjectPage {
  return {
    dir: "/tmp/project",
    tier: tier as TreeNode["tier"],
    sessions,
    offset,
    more: false,
    remaining: false,
  };
}

describe("mergeProjectPage tier coverage", () => {
  test("recent tier page updates more_recent flag", () => {
    const project = makeProject([makeNode("r1", "current")]);
    const page = makePage("recent", [makeNode("r2", "recent")], 0);
    page.remaining = true;
    const result = mergeProjectPage(project, page);
    expect(result.more_recent).toBe(true);
    expect(result.more_current).toBe(false);
    expect(result.more_archived).toBe(false);
  });

  test("archived tier page updates more_archived flag", () => {
    const project = makeProject([makeNode("r1", "current")]);
    const page = makePage("archived", [makeNode("r2", "archived")], 0);
    page.remaining = true;
    const result = mergeProjectPage(project, page);
    expect(result.more_archived).toBe(true);
    expect(result.more_current).toBe(false);
    expect(result.more_recent).toBe(false);
  });

  test("current tier page updates more_current flag", () => {
    const project = makeProject([makeNode("r1", "current")]);
    const page = makePage("current", [makeNode("r2", "current")], 0);
    page.remaining = true;
    const result = mergeProjectPage(project, page);
    expect(result.more_current).toBe(true);
    expect(result.more_recent).toBe(false);
    expect(result.more_archived).toBe(false);
  });

  test("all three tiers can coexist in one project", () => {
    const project = makeProject([makeNode("c1", "current"), makeNode("r1", "recent"), makeNode("a1", "archived")]);
    const page = makePage("recent", [makeNode("r2", "recent")], 1);
    page.remaining = true;
    const result = mergeProjectPage(project, page);
    expect(result.more_recent).toBe(true);
    expect(result.sessions).toHaveLength(4);
    // current comes first, then recent, then archived
    expect(result.sessions[0]?.tier).toBe("current");
    expect(result.sessions[1]?.tier).toBe("recent");
    expect(result.sessions[2]?.tier).toBe("recent");
    expect(result.sessions[3]?.tier).toBe("archived");
  });

  test("existing row in page replaces the matching row by row_id", () => {
    const project = makeProject([makeNode("r1", "recent")]);
    // Page contains an updated version of r1 (same row_id, different label)
    const updatedNode = makeNode("r1", "recent");
    updatedNode.label = "Updated Session r1";
    const page = makePage("recent", [updatedNode], 0);
    const result = mergeProjectPage(project, page);
    expect(result.sessions).toHaveLength(1);
    expect(result.sessions[0]?.label).toBe("Updated Session r1");
  });
});
