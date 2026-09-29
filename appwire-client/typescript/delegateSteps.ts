// What a delegate_send step says, in both clients: the delegate it messaged
// and, once the call settles, the status its footer reports. The web's
// delegate_send row and the phone's step line read these; the web's body
// reads the footer and the raw state through the same helpers.
//
// Ground truth: agent/session_tools_jobs.go's formatDelegateSend prints any
// reply, then a bracketed footer "[delegate_id <id> · <action> · <status> ·
// running in background · wait ignored: <why>]" (every field after the action
// optional), then structured_result and watch lines; marshalDelegateSendResult
// returns the same fields as the step's raw state.

import type { ItemModel } from "./model";
import { composeStepWords, type StepWords, summaryOf, withDetail } from "./stepWords";
import { parseArgs, str } from "./toolCallText";
import { asJsonObject } from "./watchRows";

/** The parts of a delegate_send step its words read. */
export type DelegateSendStep = Pick<ItemModel, "argumentsJSON" | "output">;

/** A delegate_send's raw state, as marshalDelegateSendResult returns it. */
export type DelegateSendRawState = {
  delegate_id?: string;
  action: string;
  running_in_background: boolean;
  output?: string;
  transcript_ref?: string;
  wait_ignored_reason?: string;
};

/** Whether a step's raw state is a delegate_send result. */
export function isDelegateSendResult(raw: unknown): raw is DelegateSendRawState {
  const state = asJsonObject(raw);
  return (
    state !== undefined &&
    typeof state.action === "string" &&
    state.action.trim() !== "" &&
    typeof state.running_in_background === "boolean" &&
    (state.delegate_id === undefined || typeof state.delegate_id === "string") &&
    (state.output === undefined || typeof state.output === "string") &&
    (state.transcript_ref === undefined || typeof state.transcript_ref === "string") &&
    (state.wait_ignored_reason === undefined || typeof state.wait_ignored_reason === "string")
  );
}

const KNOWN_JOB_STATUSES = ["completed", "failed", "cancelled", "stopped", "exhausted", "running"] as const;

/** The job status a footer's text names. Footer fields are optional, so a
 * status can't be read by position. */
export function statusWordFromText(text: string): string | undefined {
  for (const status of KNOWN_JOB_STATUSES) {
    if (new RegExp(`\\b${status}\\b`).test(text)) return status;
  }
  return undefined;
}

const KNOWN_DELEGATE_SEND_STATUSES = new Set([
  "running",
  "completed",
  "failed",
  "exhausted",
  "cancelled",
  "stopped",
  "delivered",
  "not_delivered",
]);

/** A delegate_send footer: its text inside the brackets, and the index of its
 * line in the output. */
export type DelegateSendFooterInfo = { text: string; index: number };

/** The footer a delegate_send printed, when its output ends in one (after any
 * structured_result and watch lines); undefined when the output has none, or
 * a bracketed line that isn't the footer's shape. */
export function delegateSendFooter(output: string): DelegateSendFooterInfo | undefined {
  const trimmed = output.trimEnd();
  const lines = trimmed.split("\n");

  let index = lines.length - 1;
  while (index >= 0) {
    const line = lines[index] ?? "";
    if (line.startsWith("structured_result (valid=") || line === "watches:" || line.startsWith("- ")) {
      index -= 1;
      continue;
    }
    break;
  }

  const footerLine = lines[index];
  if (footerLine === undefined || !footerLine.startsWith("[") || !footerLine.endsWith("]")) return undefined;

  const footer = footerLine.slice(1, -1);
  const fields = footer.split(" · ");
  if (fields.length < 2) return undefined;

  let fieldIndex = 0;
  const delegateField = fields[fieldIndex] ?? "";
  if (!delegateField.startsWith("delegate_id ")) return undefined;
  if (delegateField.slice("delegate_id ".length).trim() === "") return undefined;
  fieldIndex += 1;

  const actionField = fields[fieldIndex] ?? "";
  if (actionField.trim() === "") return undefined;
  fieldIndex += 1;

  const startedJobField = fields[fieldIndex] ?? "";
  if (startedJobField.startsWith("started_job_id ")) {
    if (startedJobField.slice("started_job_id ".length).trim() === "") return undefined;
    fieldIndex += 1;
  }

  const statusField = fields[fieldIndex];
  if (statusField !== undefined && KNOWN_DELEGATE_SEND_STATUSES.has(statusField)) {
    fieldIndex += 1;
  }

  const runningField = fields[fieldIndex] ?? "";
  if (runningField === "running in background") {
    fieldIndex += 1;
  }

  const watchingField = fields[fieldIndex] ?? "";
  if (watchingField === "watching") {
    fieldIndex += 1;
  }

  const waitIgnoredField = fields[fieldIndex] ?? "";
  if (waitIgnoredField.startsWith("wait ignored: ")) {
    if (waitIgnoredField.slice("wait ignored: ".length).trim() === "") return undefined;
    fieldIndex += 1;
  }

  if (fieldIndex !== fields.length) return undefined;
  return { text: footer, index };
}

/** Who a send addressed: `to`, the live argument, or `target`, the retired
 * job_send_message alias's (agent/transcript_render.go's historical rendering
 * path still reads it this way); "" when the call names neither. */
export function delegateSendTarget(step: Pick<DelegateSendStep, "argumentsJSON">): string {
  const args = parseArgs(step.argumentsJSON);
  return str(args, "to") ?? str(args, "target") ?? "";
}

function sentTo(step: Pick<DelegateSendStep, "argumentsJSON">): StepWords {
  const target = delegateSendTarget(step);
  return target === "" ? { verb: "Sent a message to a delegate" } : { verb: "Sent a message to delegate", target };
}

/** "Sent a message to delegate dlg_x" · "running": the delegate it messaged
 * and, once the call settles, one status word from the footer's own text.
 * The footer's other fields (the delegate_id echo, started_job_id, "running
 * in background") are noise on one line and stay out of it. */
export function delegateSendWords(step: DelegateSendStep): StepWords {
  const footer = delegateSendFooter(step.output ?? "");
  return withDetail(sentTo(step), footer ? statusWordFromText(footer.text) : undefined);
}

export const delegateSendSummary = summaryOf(delegateSendWords);

/** The line before its status: "Sent a message to delegate dlg_x". The web
 * sets the open-transcript control right after it. */
export function delegateSendBase(step: Pick<DelegateSendStep, "argumentsJSON">): string {
  return composeStepWords(sentTo(step));
}
