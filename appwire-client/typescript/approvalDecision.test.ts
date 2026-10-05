// @vitest-environment node

import { expect, test } from "vitest";
import { approvalDecisionOf, approvalDecisionText } from "./approvalDecision";
import { systemEventWireItem } from "./testing/systemEventWireFixtures";

// What the daemon sends for a human's Allow and Deny
// (agent/testdata/systemeventwire), as the clients' item model carries it.
const allowed = () => systemEventWireItem("approval-allowed");
const denied = () => systemEventWireItem("approval-denied");

test("reads the decision the daemon put on raw.approvalDecision", () => {
  expect(approvalDecisionOf(allowed())).toEqual({
    escalationId: "esc_1_a",
    approved: true,
    tool: "write_file",
    kind: "file_tool",
    deniedPath: "/Users/j/sites/docs/index.md",
  });
  expect(approvalDecisionOf(denied())).toMatchObject({ approved: false, tool: "read_file", deniedPath: "/etc/hosts" });
});

test("is null for any other event, and for a decision without its fields", () => {
  expect(approvalDecisionOf(systemEventWireItem("tool-repair"))).toBeNull();
  expect(approvalDecisionOf({ eventKind: "approval_decision", raw: undefined })).toBeNull();
  expect(approvalDecisionOf({ eventKind: "approval_decision", raw: { approvalDecision: [] } })).toBeNull();
  expect(
    approvalDecisionOf({ eventKind: "approval_decision", raw: { approvalDecision: { approved: "yes", tool: "x" } } }),
  ).toBeNull();
});

// "Allowed" or "Denied", the tool's short action, and the path.
test("says what was decided in the tool's short action", () => {
  const allowedDecision = approvalDecisionOf(allowed());
  const deniedDecision = approvalDecisionOf(denied());
  if (!allowedDecision || !deniedDecision) throw new Error("fixture decisions did not parse");
  expect(approvalDecisionText(allowedDecision)).toBe("Allowed: write /Users/j/sites/docs/index.md");
  expect(approvalDecisionText(deniedDecision)).toBe("Denied: read /etc/hosts");
  expect(approvalDecisionText({ ...deniedDecision, tool: "edit_file" })).toBe("Denied: edit /etc/hosts");
  // Any other tool reads as its name in words.
  expect(approvalDecisionText({ ...deniedDecision, tool: "apply_patch" })).toBe("Denied: apply patch /etc/hosts");
  // A decision with no path still says what was decided, with no stray space.
  expect(approvalDecisionText({ ...deniedDecision, deniedPath: "" })).toBe("Denied: read");
});
