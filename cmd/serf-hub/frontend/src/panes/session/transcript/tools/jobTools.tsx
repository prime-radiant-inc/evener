// Descriptors for the job_*/delegate_send family (parity checklist §2's
// job_read_output/job_list/job_stop/job_send_message renderers), minus
// `delegate` itself (the spawn call - see subagentModule.tsx, which owns
// the aggregated view these calls' targets correlate into).
//
// Ground truth (agent/session_tools_jobs.go, verified directly): the
// registered "read one job" tool is named job_status, NOT job_read_output,
// which has no definition and no registration, and reaches this renderer
// only from stored transcripts. ExecuteCall marshals State directly into
// item.raw with no wrapper key. job_status returns whole-object JSON in
// item.output, so its existing output parser is the useful representation;
// raw duplicates that state. job_list returns human-formatted text in output
// but a stable direct jobListResult in raw, which supplies valuable row fields
// and is rendered below. job_stop remains formatted-output-driven; delegate_send
// prefers validated raw state and otherwise falls back to its historical
// formatted output. job_send_message is a retired/banned tool name kept only
// as a defensive alias reading its legacy target arg.
import { useLayoutEffect } from "react";
import type { ItemModel } from "../../../../protocol/model";
import { CodeBlock } from "../../../../widgets";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { HeadClippedOutputBody } from "./bodies";
import { clip, clipJobID, parseArgs, parseJSONObject, str, trailingBracketFooter } from "./helpers";
import { classifyJobStatus, resolveRowKey, statusWordFromText } from "./subagentModule";
import { turnScopeKey, updateSubagentRowIfExists } from "./subagentModuleStore";

const ID_CLIP = 26;

// correlateOnly is the shared shape job_status/job_stop/delegate_send's
// bodies use to ALSO update an existing subagent-module row for the child
// they're checking on/messaging, in addition to their own normal output
// display - never spawning a fresh row (subagentModule.tsx's own
// updateSubagentRowIfExists already enforces that; this is just the
// per-tool row-key/kind/preview derivation).
type CorrelationResolvers = {
  resolveKey: (item: ItemModel) => string;
  resolveKind: (item: ItemModel) => ReturnType<typeof classifyJobStatus> | undefined;
  resolvePreview: (item: ItemModel) => string;
};

function useCorrelateSubagentRow(
  { item, sessionRef }: Pick<ToolRenderProps, "item" | "sessionRef">,
  { resolveKey, resolveKind, resolvePreview }: CorrelationResolvers,
): void {
  useLayoutEffect(() => {
    const kind = resolveKind(item);
    if (kind === undefined) return; // nothing settled to report yet
    updateSubagentRowIfExists(turnScopeKey(sessionRef, item.turnId), resolveKey(item), {
      kind,
      resultPreview: resolvePreview(item),
      completedAt: item.completedAt,
    });
  });
}

function CorrelatingBody({
  item,
  sessionRef,
  resolveKey,
  resolveKind,
  resolvePreview,
}: ToolRenderProps & {
  resolveKey: (item: ItemModel) => string;
  resolveKind: (item: ItemModel) => ReturnType<typeof classifyJobStatus> | undefined;
  resolvePreview: (item: ItemModel) => string;
}) {
  useCorrelateSubagentRow({ item, sessionRef }, { resolveKey, resolveKind, resolvePreview });
  return <HeadClippedOutputBody item={item} live={false} />;
}

type JsonObject = Record<string, unknown>;

function asJsonObject(value: unknown): JsonObject | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as JsonObject) : undefined;
}

interface JobListState {
  jobs: JsonObject[];
  total: number;
}

// jobListState validates the direct StateResult.State shape produced by
// jobListTool: {jobs:[{job_id,...}], count, total, ...}. There is no `state` or
// `job_list` wrapper key because ExecuteCall marshals State itself. A malformed
// or older raw value falls back to the producer's formatted output below.
function jobListState(raw: unknown): JobListState | undefined {
  const state = asJsonObject(raw);
  if (!state || !Array.isArray(state.jobs)) return undefined;

  const jobs: JsonObject[] = [];
  for (const value of state.jobs) {
    const job = asJsonObject(value);
    if (!job || typeof job.job_id !== "string") return undefined;
    jobs.push(job);
  }

  const total =
    typeof state.total === "number" ? state.total : typeof state.count === "number" ? state.count : jobs.length;
  return { jobs, total };
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
      {state.jobs.length === 0 ? (
        <div>No jobs.</div>
      ) : (
        state.jobs.map((job) => {
          const identity = textField(job, "job_id");
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
        {state.jobs.length} of {state.total} jobs
      </div>
    </div>
  );
}

registerToolRenderer({
  match: (name) => name === "job_status" || name === "job_read_output",
  icon: "job",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const parsedOutput = parseJSONObject(item.output);
    const jobId = (parsedOutput && str(parsedOutput, "job_id")) ?? str(args, "job_id") ?? "";
    const status = parsedOutput ? str(parsedOutput, "status") : undefined;
    return status ? `Checked ${clipJobID(jobId)} · ${status}` : `Checked ${clipJobID(jobId)}`;
  },
  body(props: ToolRenderProps) {
    return (
      <CorrelatingBody
        {...props}
        resolveKey={(item) => {
          const args = parseArgs(item.argumentsJSON);
          const parsedOutput = parseJSONObject(item.output);
          const jobId = (parsedOutput && str(parsedOutput, "job_id")) ?? str(args, "job_id") ?? "";
          return resolveRowKey(undefined, jobId, item.callId ?? item.id);
        }}
        resolveKind={(item) => {
          const parsed = parseJSONObject(item.output);
          return parsed ? classifyJobStatus(str(parsed, "status")) : undefined;
        }}
        resolvePreview={(item) => {
          const parsed = parseJSONObject(item.output);
          return (parsed && str(parsed, "reason")) ?? "";
        }}
      />
    );
  },
});

registerToolRenderer({
  match: "job_list",
  icon: "job",
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
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const jobId = str(args, "job_id") ?? "";
    const footer = trailingBracketFooter(item.output ?? "");
    return footer ? `Stopped ${clipJobID(jobId)} · ${footer}` : `Stopped ${clipJobID(jobId)}`;
  },
  body(props: ToolRenderProps) {
    return (
      <CorrelatingBody
        {...props}
        resolveKey={(item) =>
          resolveRowKey(undefined, str(parseArgs(item.argumentsJSON), "job_id") ?? "", item.callId ?? item.id)
        }
        resolveKind={(item) => {
          const footer = trailingBracketFooter(item.output ?? "");
          return footer ? classifyJobStatus(statusWordFromText(footer)) : undefined;
        }}
        resolvePreview={(item) => trailingBracketFooter(item.output ?? "") ?? ""}
      />
    );
  },
});

function delegateSendTarget(args: Record<string, unknown>): string {
  // `to` is the live delegate_send arg; `target` is the retired
  // job_send_message alias's own arg name (agent/transcript_render.go's
  // historical rendering path still reads it this way).
  return str(args, "to") ?? str(args, "target") ?? "";
}

type DelegateSendRawState = {
  action: string;
  running_in_background: boolean;
  output?: string;
  transcript_ref?: string;
};

function delegateSendResult(raw: unknown): raw is DelegateSendRawState {
  const state = asJsonObject(raw);
  return (
    state !== undefined &&
    typeof state.action === "string" &&
    state.action.trim() !== "" &&
    typeof state.running_in_background === "boolean" &&
    (state.output === undefined || typeof state.output === "string") &&
    (state.transcript_ref === undefined || typeof state.transcript_ref === "string")
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

// The target's transcript ref rides the tool call's raw state
// (agent/session_tools_jobs.go's delegateSendResult.TranscriptRef), so the
// collapsed row can offer the same open-in-pane link the subagent module rows
// have. Runtime-message results and pre-field transcripts carry no ref - no
// button, never a dead link.
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
  const args = parseArgs(item.argumentsJSON);
  const target = clip(delegateSendTarget(args), ID_CLIP);
  const base = target === "" ? "Sent a message to a delegate" : `Sent a message to delegate ${target}`;
  const footer = delegateSendFooter(item.output ?? "");
  const status = footer ? statusWordFromText(footer.text) : undefined;
  return status ? `${base} · ${status}` : base;
}

function DelegateSendBody(props: ToolRenderProps) {
  const { item } = props;
  const message = str(parseArgs(item.argumentsJSON), "message");
  const response = delegateSendResponse(item);

  useCorrelateSubagentRow(props, {
    resolveKey: (item) =>
      resolveRowKey(delegateSendTarget(parseArgs(item.argumentsJSON)), undefined, item.callId ?? item.id),
    resolveKind: (item) => {
      const footer = delegateSendFooter(item.output ?? "");
      return footer ? classifyJobStatus(statusWordFromText(footer.text)) : undefined;
    },
    resolvePreview: (item) => delegateSendFooter(item.output ?? "")?.text ?? "",
  });

  if (!message && !response) return null;
  return (
    <div data-testid="delegate-send-body">
      {message ? (
        <section>
          <strong>Message</strong>
          <div data-testid="delegate-send-message">
            <CodeBlock text={message} copyLabel="Copy message" />
          </div>
        </section>
      ) : null}
      {response ? (
        <section>
          <strong>Response</strong>
          <div data-testid="delegate-send-response">
            <CodeBlock text={response} copyLabel="Copy response" />
          </div>
        </section>
      ) : null}
    </div>
  );
}

registerToolRenderer({
  match: (name) => name === "delegate_send" || name === "job_send_message",
  icon: "send",
  summary: delegateSendSummary,
  openTranscriptRef: delegateSendTranscriptRef,
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
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const operation = str(args, "operation");
    return operation ? `${item.toolName}: ${operation}` : (item.toolName ?? "");
  },
  body: HeadClippedOutputBody,
});
