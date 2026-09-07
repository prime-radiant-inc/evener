// Descriptors for job_* and delegate_send follow-up calls.
import type { ItemModel } from "../../../../protocol/model";
import { CopyButton } from "../../../../widgets";
import { UserMessageView } from "../messages/UserMessageItem";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { HeadClippedOutputBody } from "./bodies";
import { DelegateStatusBody } from "./delegateStatus";
import { clip, clipJobID, parseArgs, parseJSONObject, str, trailingBracketFooter } from "./helpers";
import { statusWordFromText } from "./subagentModule";

const ID_CLIP = 26;

type JsonObject = Record<string, unknown>;

function asJsonObject(value: unknown): JsonObject | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as JsonObject) : undefined;
}

interface JobListState {
  items: JsonObject[];
  total: number;
}

// jobListState validates the current direct StateResult.State shape produced
// by jobListTool: {items:[{id,...}], count, total, ...}. Stored transcripts
// using the retired jobs/job_id shape remain readable through the separate
// legacy branch. There is no wrapper key because ExecuteCall marshals State
// itself.
function jobListState(raw: unknown): JobListState | undefined {
  const state = asJsonObject(raw);
  if (!state) return undefined;

  const values = Array.isArray(state.items) ? state.items : Array.isArray(state.jobs) ? state.jobs : undefined;
  if (!values) return undefined;
  const identityField = Array.isArray(state.items) ? "id" : "job_id";

  const items: JsonObject[] = [];
  for (const value of values) {
    const item = asJsonObject(value);
    if (!item || typeof item[identityField] !== "string") return undefined;
    items.push(item);
  }

  const total =
    typeof state.total === "number" ? state.total : typeof state.count === "number" ? state.count : items.length;
  return { items, total };
}

function textField(object: JsonObject, key: string): string | undefined {
  const value = object[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

function JobListBody({ item, live }: ToolRenderProps) {
  const state = jobListState(item.raw);
  if (!state) return <HeadClippedOutputBody item={item} live={live} />;

  return (
    <div data-testid="job-list-structured">
      {state.items.length === 0 ? (
        <div>No jobs.</div>
      ) : (
        state.items.map((job) => {
          const identity = textField(job, "id") ?? textField(job, "job_id");
          if (identity === undefined) return null;
          const fields = [identity, textField(job, "type"), textField(job, "status"), textField(job, "phase")].filter(
            (field): field is string => field !== undefined,
          );
          const description = textField(job, "description");
          return (
            <div key={identity} data-testid="job-list-row">
              {fields.join(" · ")}
              {description ? ` — ${description}` : ""}
            </div>
          );
        })
      )}
      <div data-testid="job-list-total">
        {state.items.length} of {state.total} jobs
      </div>
    </div>
  );
}

function jobControlTarget(item: ItemModel): string {
  const args = parseArgs(item.argumentsJSON);
  const parsedOutput = parseJSONObject(item.output);
  return (
    (parsedOutput && (str(parsedOutput, "id") ?? str(parsedOutput, "job_id"))) ??
    str(args, "target") ??
    str(args, "job_id") ??
    ""
  );
}

registerToolRenderer({
  match: (name) => name === "job_status" || name === "job_read_output",
  icon: "job",
  // Background work is state a reader tracks across a turn, so every job
  // row stays on its own line rather than folding into a run.
  fold: "never",
  summary(item: ItemModel) {
    const parsedOutput = parseJSONObject(item.output);
    const jobId = jobControlTarget(item);
    const status = parsedOutput ? str(parsedOutput, "status") : undefined;
    return status ? `Checked ${clipJobID(jobId)} · ${status}` : `Checked ${clipJobID(jobId)}`;
  },
  body: DelegateStatusBody,
});

registerToolRenderer({
  match: "job_list",
  icon: "job",
  fold: "never",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const status = args.status;
    const filter = Array.isArray(status) ? status.filter((s) => typeof s === "string").join(", ") : "";
    return filter ? `Listed jobs (${filter})` : "Listed jobs";
  },
  body: JobListBody,
});

registerToolRenderer({
  match: "job_stop",
  icon: "job",
  fold: "never",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const jobId = str(args, "target") ?? str(args, "job_id") ?? "";
    const footer = trailingBracketFooter(item.output ?? "");
    return footer ? `Stopped ${clipJobID(jobId)} · ${footer}` : `Stopped ${clipJobID(jobId)}`;
  },
  body: HeadClippedOutputBody,
});

function delegateSendTarget(args: Record<string, unknown>): string {
  // `to` is the live delegate_send arg; `target` is the retired
  // job_send_message alias's own arg name (agent/transcript_render.go's
  // historical rendering path still reads it this way).
  return str(args, "to") ?? str(args, "target") ?? "";
}

type DelegateSendRawState = {
  delegate_id?: string;
  action: string;
  running_in_background: boolean;
  output?: string;
  transcript_ref?: string;
  wait_ignored_reason?: string;
};

function delegateSendResult(raw: unknown): raw is DelegateSendRawState {
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

type DelegateSendFooterInfo = { text: string; index: number };

function delegateSendFooter(output: string): DelegateSendFooterInfo | undefined {
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

function delegateSendResponse(item: ItemModel): string | undefined {
  if (delegateSendResult(item.raw)) {
    const rawOutput = item.raw.output;
    if (rawOutput !== undefined && rawOutput.trim() !== "") return rawOutput;
  }

  const output = item.output ?? "";
  if (output === "") return undefined;
  const footer = delegateSendFooter(output);
  if (footer === undefined) return output;

  const response = output.trimEnd().split("\n").slice(0, footer.index).join("\n");
  return response.trim() === "" ? undefined : response;
}

function delegateSendWaitIgnoredReason(item: ItemModel): string | undefined {
  if (delegateSendResult(item.raw)) {
    const reason = item.raw.wait_ignored_reason?.trim();
    if (reason) return reason;
  }
  const footer = delegateSendFooter(item.output ?? "");
  if (!footer) return undefined;
  const field = footer.text.split(" · ").find((part) => part.startsWith("wait ignored: "));
  const reason = field?.slice("wait ignored: ".length).trim();
  return reason || undefined;
}

// The target transcript ref enables the row's open-in-pane action.
function delegateSendTranscriptRef(item: ItemModel): string | undefined {
  if (!delegateSendResult(item.raw)) return undefined;
  const ref = item.raw.transcript_ref;
  return ref !== undefined && ref.trim() !== "" ? ref : undefined;
}

// The collapsed summary names the target and, once the call settles, one
// status word recovered from the footer's own text (statusWordFromText -
// field order/presence in the footer is not fixed). The footer's remaining
// metadata (delegate_id echo, started_job_id, "running in background") is
// noise on a one-line summary and stays out of it.
function delegateSendSummary(item: ItemModel): string {
  return delegateSendBase(item) + delegateSendStatusSuffix(item);
}

function delegateSendBase(item: ItemModel): string {
  const args = parseArgs(item.argumentsJSON);
  const target = clip(delegateSendTarget(args), ID_CLIP);
  return target === "" ? "Sent a message to a delegate" : `Sent a message to delegate ${target}`;
}

function delegateSendStatusSuffix(item: ItemModel): string {
  const footer = delegateSendFooter(item.output ?? "");
  const status = footer ? statusWordFromText(footer.text) : undefined;
  return status ? ` · ${status}` : "";
}

// DelegateSendBody renders the exchange as a two-party conversation through
// the transcript's own slack-lean message view: the sent message as an
// outgoing bubble from the agent to the delegate, and - when the call waited
// for one - the delegate's reply as an incoming bubble below it. The
// section testids (delegate-send-message/-response) are the longstanding
// contract of this body and are unchanged.
function DelegateSendBody(props: ToolRenderProps) {
  const { item } = props;
  const args = parseArgs(item.argumentsJSON);
  const message = str(args, "message");
  const response = delegateSendResponse(item);
  const waitIgnoredReason = delegateSendWaitIgnoredReason(item);
  const target = clip(delegateSendTarget(args), ID_CLIP);

  if (!message && !response) return null;
  return (
    <div data-testid="delegate-send-body">
      {message ? (
        <section data-testid="delegate-send-message">
          <UserMessageView
            item={{ ...item, text: message }}
            speaker="agent"
            name={target === "" ? "Agent → delegate" : `Agent → ${target}`}
            timeIso={item.startedAt}
            opensExchange={false}
            actions={<CopyButton text={message} label="Copy message" />}
          />
        </section>
      ) : null}
      {response ? (
        <section data-testid="delegate-send-response">
          <UserMessageView
            item={{ ...item, text: response }}
            speaker="agent"
            name={target === "" ? "Delegate" : `${target} (delegate)`}
            timeIso={item.completedAt ?? item.startedAt}
            opensExchange={false}
            actions={<CopyButton text={response} label="Copy response" />}
          />
        </section>
      ) : null}
      {waitIgnoredReason ? <div data-testid="delegate-send-wait-ignored">Wait ignored: {waitIgnoredReason}</div> : null}
    </div>
  );
}

registerToolRenderer({
  match: (name) => name === "delegate_send" || name === "job_send_message",
  icon: "send",
  fold: "never",
  summary: delegateSendSummary,
  openTranscriptRef: delegateSendTranscriptRef,
  // The summary quotes the delegate target verbatim before the status meta
  // ("Sent a message to delegate <id> · <status>"), so the "open transcript"
  // control rides INLINE between the delegate it opens and the running-state
  // words that describe it (toolRenderers.ts's openTranscriptInline
  // contract) - the complete base prefix, matching summary()'s own text
  // exactly, so ToolRow can verify it with startsWith rather than search for
  // it. Undefined when there is no transcript to open, so the row never
  // builds a dead anchor-split wrapper for a button it will render nothing
  // for (ToolCallItem's own fileDocParams-gating idiom).
  openTranscriptInline: (item) => (delegateSendTranscriptRef(item) !== undefined ? delegateSendBase(item) : undefined),
  body: DelegateSendBody,
});

// Generic fallback for any other job_*-family tool (e.g. job_watch) not
// explicitly registered above - "match by predicate" per this project's
// own locked ToolRendererDescriptor doc comment. Exact matches above
// always win (toolRenderers.ts's own precedence rule), so this only ever
// resolves for a job_* name none of the specific descriptors claimed.
registerToolRenderer({
  match: (name) => name.startsWith("job_"),
  icon: "job",
  fold: "never",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const operation = str(args, "operation");
    return operation ? `${item.toolName}: ${operation}` : (item.toolName ?? "");
  },
  body: HeadClippedOutputBody,
});
