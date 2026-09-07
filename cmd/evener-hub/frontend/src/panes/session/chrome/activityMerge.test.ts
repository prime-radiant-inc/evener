import { expect, test } from "vitest";
import {
  type ActivityDelegateEntry,
  type ActivityEntry,
  type ActivitySessionNode,
  type ActivityShellEntry,
  type ActivityTree,
  activityNodeID,
} from "./activityData";
import { graftContinuationTree } from "./activityMerge";

const shell = (jobId: string, outputBytes = 0): ActivityShellEntry => ({
  kind: "shell",
  job: {
    jobId,
    ownerSessionId: "root",
    ownerRef: "local:root",
    type: "shell",
    status: "completed",
    terminal: true,
    background: false,
    hasOutput: true,
    description: jobId,
    startedAt: "2026-09-07T00:00:00Z",
    outputBytes,
  },
});
const session = (sessionId: string, entries: ActivityEntry[], continuation?: string): ActivitySessionNode => ({
  kind: "session",
  sessionId,
  ref: `local:${sessionId}`,
  label: sessionId,
  aggregate: "completed",
  counts: { active: 0, failed: 0, completed: entries.length, complete: !continuation },
  entries,
  branch: continuation ? { truncated: true, continuation } : {},
});
const tree = (entries: ActivityEntry[], continuation?: string): ActivityTree => ({
  revision: 4,
  root: session("root", entries, continuation),
});
const delegate = (child?: ActivitySessionNode, revision = 2): ActivityDelegateEntry => ({
  kind: "delegate",
  delegate: {
    delegateId: "delegate",
    childSessionId: "child",
    childRef: "local:child",
    projectionRevision: revision,
    status: revision === 2 ? "completed" : "running",
    child,
    branch: child ? {} : { truncated: true, continuation: "child-page" },
  },
});
const ids = (node: ActivitySessionNode) => node.entries.map(activityNodeID);

test("root continuation retains its prefix and deduplicates overlapping entries", () => {
  const current = tree([shell("a"), shell("b")], "next");
  const patch = tree([shell("b", 20), shell("c")]);
  const before = structuredClone(current);
  const result = graftContinuationTree(current, patch);
  expect(ids(result.root)).toEqual(["job:a", "job:b", "job:c"]);
  expect(result.root.entries[1]).toEqual(shell("b", 20));
  expect(result.root.branch).toEqual({});
  expect(result.root.counts).toEqual(before.root.counts);
  expect(current).toEqual(before);
  result.root.entries.splice(0, 1);
  expect(ids(current.root)).toEqual(["job:a", "job:b"]);
});

test("nested session continuation keeps earlier jobs and unrelated root siblings", () => {
  const current = tree([shell("sibling"), delegate(session("child", [shell("a")], "next"))]);
  const patch = tree([delegate(session("child", [shell("b")]))]);
  const result = graftContinuationTree(current, patch);
  expect(ids(result.root)).toEqual(["job:sibling", "delegate:delegate"]);
  const entry = result.root.entries[1];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing child");
  expect(ids(entry.delegate.child)).toEqual(["job:a", "job:b"]);
  expect(entry.delegate.child.branch).toEqual({});
});

test("delegate continuation expands child data at an unchanged projection revision", () => {
  const current = tree([delegate()]);
  const patch = tree([delegate(session("child", [shell("a")]))]);
  const result = graftContinuationTree(current, patch);
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing child");
  expect(ids(entry.delegate.child)).toEqual(["job:a"]);
  expect(entry.delegate.branch).toEqual({});
  expect(entry.delegate.projectionRevision).toBe(2);
  expect(entry.delegate.status).toBe("completed");
});

test("continuation data does not regress a newer delegate projection", () => {
  const current = tree([delegate(session("child", [shell("a")], "next"), 2)]);
  const patch = tree([delegate(session("child", [shell("b")]), 1)]);
  const result = graftContinuationTree(current, patch);
  const entry = result.root.entries[0];
  if (entry?.kind !== "delegate" || !entry.delegate.child) throw new Error("missing child");
  expect(ids(entry.delegate.child)).toEqual(["job:a", "job:b"]);
  expect(entry.delegate.projectionRevision).toBe(2);
  expect(entry.delegate.status).toBe("completed");
});
