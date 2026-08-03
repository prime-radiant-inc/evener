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
// and is rendered below. job_stop and delegate_send return concise actionable
// text/footers in output; their direct raw results duplicate that status or
// would repeat details already present in the existing correlation bodies, so
// those bodies stay output-driven. job_send_message is a retired/banned tool
// name kept only as a defensive alias reading its legacy target arg.
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

function delegateSendResponse(item: ItemModel): string | undefined {
  const raw = asJsonObject(item.raw);
  if (raw && typeof raw.output === "string") return raw.output === "" ? undefined : raw.output;

  const output = item.output ?? "";
  if (output === "") return undefined;
  const footer = trailingBracketFooter(output);
  if (!footer?.startsWith("delegate_id ")) return output;

  const trimmed = output.trimEnd();
  const footerStart = trimmed.lastIndexOf("[");
  const response = trimmed.slice(0, footerStart).replace(/\n$/, "");
  return response.trim() === "" ? undefined : response;
}

function DelegateSendBody(props: ToolRenderProps) {
  const { item } = props;
  const message = str(parseArgs(item.argumentsJSON), "message");
  const response = delegateSendResponse(item);

  useCorrelateSubagentRow(props, {
    resolveKey: (item) =>
      resolveRowKey(delegateSendTarget(parseArgs(item.argumentsJSON)), undefined, item.callId ?? item.id),
    resolveKind: (item) => {
      const footer = trailingBracketFooter(item.output ?? "");
      return footer ? classifyJobStatus(statusWordFromText(footer)) : undefined;
    },
    resolvePreview: (item) => trailingBracketFooter(item.output ?? "") ?? "",
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
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const target = clip(delegateSendTarget(args), ID_CLIP);
    const footer = trailingBracketFooter(item.output ?? "");
    return footer ? `Messaged ${target} · ${footer}` : `Messaged ${target}`;
  },
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
