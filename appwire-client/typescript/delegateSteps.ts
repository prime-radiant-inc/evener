// What a delegate_send step says, in both clients: the delegate it messaged
// and, once the call settles, the status its footer reports. The web's
// delegate_send row and the phone's step line read these; the web's body
// reads the footer and the raw state through the same helpers.
//
// Ground truth: agent/session_tools_jobs.go's formatDelegateSend prints any
// earlier results a wait carried (each numbered and printed as a reply is),
// then any reply, then a bracketed footer "[delegate_id <id> · <action> ·
// <status> · running in background · wait ignored: <why>]" (every field after
// the action optional), then structured_result and watch lines;
// marshalDelegateSendResult returns the same fields as the step's raw state,
// the earlier results under earlier_results.

import type { ItemModel } from "./model";
import { composeStepWords, type StepWords, summaryOf, withDetail } from "./stepWords";
import { nonblank, parseArgs, str } from "./toolCallText";
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
  earlier_results?: unknown;
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

// The statuses a delegate_send footer carries. A send's result status is a
// job status, never the delegate's lifecycle (idle belongs to the delegate
// call's receipt, stableDelegateResult): running while the delegate works,
// else stableDelegateOutcomeJobStatus's completed, failed, cancelled, stopped
// or exhausted (agent/delegate_runtime.go). delivered and not_delivered are
// the footer's status before 2a94f56d14 (Aug 2026) retired the delegate job
// schema; transcripts from then still carry them.
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

/** A delegate_send footer: its text inside the brackets, the index of its
 * line in the output, its status field when it has one ("running",
 * "delivered", "not_delivered", …), whether it says the delegate runs in the
 * background, and why its wait was ignored when it says so. */
export type DelegateSendFooterInfo = {
  text: string;
  index: number;
  status?: string;
  runningInBackground: boolean;
  waitIgnoredReason?: string;
};

function isFooterLine(line: string): boolean {
  return line.startsWith("[delegate_id ") && line.endsWith("]");
}

/** The footer a delegate_send printed: its last line that opens
 * "[delegate_id " and closes "]", whatever lines (worktree, warning,
 * structured_result, watches) the tool printed after it. Undefined when the
 * output has no such line, or that line isn't the footer's shape. */
export function delegateSendFooter(output: string): DelegateSendFooterInfo | undefined {
  const trimmed = output.trimEnd();
  const lines = trimmed.split("\n");

  let index = lines.length - 1;
  while (index >= 0 && !isFooterLine(lines[index] ?? "")) index -= 1;
  const footerLine = lines[index];
  if (footerLine === undefined) return undefined;

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
  const status = statusField !== undefined && KNOWN_DELEGATE_SEND_STATUSES.has(statusField) ? statusField : undefined;
  if (status !== undefined) fieldIndex += 1;

  const runningInBackground = fields[fieldIndex] === "running in background";
  if (runningInBackground) fieldIndex += 1;

  const watchingField = fields[fieldIndex] ?? "";
  if (watchingField === "watching") {
    fieldIndex += 1;
  }

  const waitIgnoredField = fields[fieldIndex] ?? "";
  let waitIgnoredReason: string | undefined;
  if (waitIgnoredField.startsWith("wait ignored: ")) {
    waitIgnoredReason = waitIgnoredField.slice("wait ignored: ".length).trim();
    if (waitIgnoredReason === "") return undefined;
    fieldIndex += 1;
  }

  if (fieldIndex !== fields.length) return undefined;
  return { text: footer, index, status, runningInBackground, waitIgnoredReason };
}

/** The parts of a delegate_send step its exchange reads: its result's raw
 * state and printed output. */
export type DelegateSendResult = Pick<ItemModel, "raw" | "output">;

/** The delegate's reply to a send that waited for one: the raw state's
 * output, else what the tool printed above its footer (all of it when there
 * is no footer). Undefined when the send got none, as a steer doesn't. */
export function delegateSendResponse(step: DelegateSendResult): string | undefined {
  if (isDelegateSendResult(step.raw)) {
    const rawOutput = step.raw.output;
    if (rawOutput !== undefined && rawOutput.trim() !== "") return rawOutput;
    // A reply that carried earlier results printed them above its own: what
    // it printed is never its reply, so one with no text has none (#3906).
    const earlier = step.raw.earlier_results;
    if (Array.isArray(earlier) && earlier.length > 0) return undefined;
  }

  const output = step.output ?? "";
  if (output === "") return undefined;
  const footer = delegateSendFooter(output);
  if (footer === undefined) return output;

  const response = output.trimEnd().split("\n").slice(0, footer.index).join("\n");
  return response.trim() === "" ? undefined : response;
}

/** An earlier result a send's wait carried, in words for a client to show
 * where it shows a reply: its text, else its reason (a failed run may have
 * only a reason), else that it had none; and its status. A mid-work update
 * (action "update") is marked update and has no status: it settled nothing. */
export type DelegateSendEarlierResponse = { text: string; status?: string; update?: true };

/** The earlier results a send's wait carried ahead of its own reply, oldest
 * first: results of the same delegate the caller had not yet received
 * (#3906), from the raw state's earlier_results. They are delivered here and
 * nowhere else, so every entry is kept, with or without text. Empty when there
 * are none. */
export function delegateSendEarlierResponses(step: DelegateSendResult): DelegateSendEarlierResponse[] {
  if (!isDelegateSendResult(step.raw)) return [];
  const earlier = step.raw.earlier_results;
  if (!Array.isArray(earlier)) return [];
  return earlier.flatMap((entry): DelegateSendEarlierResponse[] => {
    const state = asJsonObject(entry);
    if (state === undefined) return [];
    const text = nonblank(str(state, "output")) ?? nonblank(str(state, "reason")) ?? "(no reply)";
    if (str(state, "action") === "update") return [{ text, update: true }];
    return [{ text, status: nonblank(str(state, "status")) }];
  });
}

/** How a client heads an earlier result: "earlier reply 2 of 3", with its
 * status when it didn't complete ("earlier reply 2 of 3 · failed"); an update
 * is "earlier update 2 of 3". */
export function delegateSendEarlierLabel(earlier: DelegateSendEarlierResponse, index: number, count: number): string {
  if (earlier.update) return `earlier update ${index + 1} of ${count}`;
  const which = `earlier reply ${index + 1} of ${count}`;
  return earlier.status && earlier.status !== "completed" ? `${which} · ${earlier.status}` : which;
}

/** Why a send's wait was ignored (it asked to wait on a delegate that was
 * already running): the raw state's reason, else the footer's "wait
 * ignored:" field. Undefined when the wait was honoured or not asked for. */
export function delegateSendWaitIgnoredReason(step: DelegateSendResult): string | undefined {
  if (isDelegateSendResult(step.raw)) {
    const reason = step.raw.wait_ignored_reason?.trim();
    if (reason) return reason;
  }
  return delegateSendFooter(step.output ?? "")?.waitIgnoredReason;
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
 * and, once the call settles, its footer's status field in words ("not
 * delivered"), or "running" when the footer names no status but says the
 * delegate runs in the background. The footer's other fields (the
 * delegate_id echo, the action, started_job_id) are noise on one line and
 * stay out of it. */
export function delegateSendWords(step: DelegateSendStep): StepWords {
  const footer = delegateSendFooter(step.output ?? "");
  const status = footer?.status ?? (footer?.runningInBackground ? "running" : undefined);
  return withDetail(sentTo(step), status?.replaceAll("_", " "));
}

export const delegateSendSummary = summaryOf(delegateSendWords);

/** The line before its status: "Sent a message to delegate dlg_x". The web
 * sets the open-transcript control right after it. */
export function delegateSendBase(step: Pick<DelegateSendStep, "argumentsJSON">): string {
  return composeStepWords(sentTo(step));
}
