// A human's Allow or Deny on a sandbox escalation, as transcript history
// (S16). The daemon records it as a systemMessage whose eventKind is
// "approval_decision" and whose raw.approvalDecision carries the decision
// (apptranscript's ApprovalDecisionAnnouncement); both clients draw the row
// from that structure, never from the daemon's sentence.

import type { ItemModel } from "./model";
import { isPlainObject } from "./plainObject";
import { words } from "./toolSummaries";

export const APPROVAL_DECISION_EVENT_KIND = "approval_decision";

export interface ApprovalDecision {
  readonly escalationId: string;
  readonly approved: boolean;
  /** The tool the sandbox denied, e.g. "write_file". */
  readonly tool: string;
  /** The approval card's kind, e.g. "file_tool". */
  readonly kind: string;
  readonly deniedPath: string;
}

/** The decision an approval_decision item carries, or null for any other item
 * and for one whose raw lacks the decision's fields. */
export function approvalDecisionOf(item: Pick<ItemModel, "eventKind" | "raw">): ApprovalDecision | null {
  if (item.eventKind !== APPROVAL_DECISION_EVENT_KIND || !isPlainObject(item.raw)) return null;
  const decision = item.raw.approvalDecision;
  if (!isPlainObject(decision)) return null;
  const { escalationId, approved, tool, kind, deniedPath } = decision;
  if (typeof approved !== "boolean" || typeof tool !== "string" || typeof deniedPath !== "string") return null;
  return {
    escalationId: typeof escalationId === "string" ? escalationId : "",
    approved,
    tool,
    kind: typeof kind === "string" ? kind : "",
    deniedPath,
  };
}

// The short action for each tool the daemon escalates (agent's
// escalatableTools). Any other tool reads as its name in words.
const TOOL_ACTIONS: Readonly<Record<string, string>> = {
  read_file: "read",
  write_file: "write",
  edit_file: "edit",
};

/** "Allowed: write /path" or "Denied: read /path". */
export function approvalDecisionText(decision: ApprovalDecision): string {
  const action = TOOL_ACTIONS[decision.tool] ?? words(decision.tool);
  const what = [action, decision.deniedPath].filter((part) => part !== "").join(" ");
  return `${decision.approved ? "Allowed" : "Denied"}: ${what}`;
}
