import type { TreeNode as ApiTreeNode } from "../../stores/tree";

// Archive is a decision about a TOP-LEVEL row, so only a top-level row
// offers it. hubcore's nodeKind (internal/hubcore/tree.go) names the kinds
// that are never top-level: "subagent" (nested under its parent) and "fork"
// (a snapshotted original nested under the branch that superseded it) - the
// same two nestedSessionIDs computes - plus the synthetic "cluster" fold row,
// which stands for a group of sessions rather than being one. Everything else
// is a real top-level session. Written as an exclusion list rather than
// `=== "session"` so an unrecognized future kind still gets the action rather
// than silently losing it.
const NESTED_KINDS: ReadonlySet<string> = new Set(["subagent", "fork", "cluster"]);

export function isTopLevelSession(session: ApiTreeNode): boolean {
  return !NESTED_KINDS.has(session.kind);
}
