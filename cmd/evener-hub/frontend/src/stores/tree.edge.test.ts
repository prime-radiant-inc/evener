// Edge cases for tree.ts mergeProjectPage that close the remaining uncovered lines.

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
  };
}

describe("mergeProjectPage", () => {
  test("replaces an existing row at the same tier", () => {
    const existing = makeNode("row1", "current");
    const updated = makeNode("row1", "current");
    updated.label = "Updated label";
    const project = makeProject([existing]);
    const page = makePage("current", [updated], 0);
    const result = mergeProjectPage(project, page);
    expect(result.sessions[0]?.label).toBe("Updated label");
  });

  test("inserts a new row at the offset position", () => {
    const row1 = makeNode("row1", "current");
    const row3 = makeNode("row3", "current");
    const project = makeProject([row1, row3]);
    const newRow = makeNode("row2", "current");
    const page = makePage("current", [newRow], 1);
    const result = mergeProjectPage(project, page);
    expect(result.sessions.map((s) => s.row_id)).toEqual(["row1", "row2", "row3"]);
  });

  test("inserts at end when offset exceeds array length", () => {
    const row1 = makeNode("row1", "current");
    const project = makeProject([row1]);
    const newRow = makeNode("row2", "current");
    const page = makePage("current", [newRow], 10);
    const result = mergeProjectPage(project, page);
    expect(result.sessions.map((s) => s.row_id)).toEqual(["row1", "row2"]);
  });

  test("preserves rows from other tiers", () => {
    const currentRow = makeNode("row1", "current");
    const recentRow = makeNode("row2", "recent");
    const project = makeProject([currentRow, recentRow]);
    const newRow = makeNode("row3", "current");
    const page = makePage("current", [newRow], 1);
    const result = mergeProjectPage(project, page);
    expect(result.sessions.map((s) => s.row_id)).toEqual(["row1", "row3", "row2"]);
  });

  test("handles an empty page with no new rows", () => {
    const row1 = makeNode("row1", "current");
    const project = makeProject([row1]);
    const page = makePage("current", [], 0);
    const result = mergeProjectPage(project, page);
    expect(result.sessions.map((s) => s.row_id)).toEqual(["row1"]);
  });

  test("handles a project with no existing rows", () => {
    const project = makeProject([]);
    const newRow = makeNode("row1", "current");
    const page = makePage("current", [newRow], 0);
    const result = mergeProjectPage(project, page);
    expect(result.sessions.map((s) => s.row_id)).toEqual(["row1"]);
  });

  test("updates the more flag for the page tier", () => {
    const project = makeProject([makeNode("row1", "current")]);
    const page = makePage("current", [], 0);
    page.remaining = true;
    const result = mergeProjectPage(project, page);
    expect(result.more_current).toBe(true);
  });
});
