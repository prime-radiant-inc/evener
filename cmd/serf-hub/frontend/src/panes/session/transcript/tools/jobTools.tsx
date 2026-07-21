// Descriptors for the job_*/delegate_send family (parity checklist §2's
// job_read_output/job_list/job_stop/job_send_message renderers), minus
// `delegate` itself (the spawn call - see subagentModule.tsx, which owns
// the aggregated view these calls' targets correlate into).
//
// Ground truth (agent/session_tools_jobs.go, verified directly - see the
// wave-4 task-3 report for the full citation trail): the currently
// registered "read one job" tool is named job_status, NOT job_read_output
// (that Def exists but is never wired into registerJobToolsWithRegistrar);
// job_status is also the ONLY member of this family whose Output is real,
// whole-string JSON (marshalBoundedJSON) - job_list/job_stop/delegate_send
// all return human-FORMATTED TEXT (formatJobList/formatJobStop/
// formatDelegateSend) ending in a "[... · ...]" bracketed footer, with the
// actual structured result only in tool_state/Raw, which protocol/
// reducer.ts drops before it reaches ItemModel (see helpers.ts's own
// header). job_send_message is a retired/banned tool name (see the
// report) kept only as a defensive alias reading its own legacy `target`
// arg, exactly as renderer-tools.js's jobSendMessageRenderer did.
import { useLayoutEffect } from "react";
import type { ItemModel } from "../../../../protocol/model";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { HeadClippedOutputBody } from "./bodies";
import { clip, parseArgs, parseJSONObject, str, trailingBracketFooter } from "./helpers";
import { classifyJobStatus, resolveRowKey, statusWordFromText } from "./subagentModule";
import { updateSubagentRowIfExists } from "./subagentModuleStore";

const ID_CLIP = 26;

// correlateOnly is the shared shape job_status/job_stop/delegate_send's
// bodies use to ALSO update an existing subagent-module row for the child
// they're checking on/messaging, in addition to their own normal output
// display - never spawning a fresh row (subagentModule.tsx's own
// updateSubagentRowIfExists already enforces that; this is just the
// per-tool row-key/kind/preview derivation).
function CorrelatingBody({
  item,
  resolveKey,
  resolveKind,
  resolvePreview,
}: ToolRenderProps & {
  resolveKey: (item: ItemModel) => string;
  resolveKind: (item: ItemModel) => ReturnType<typeof classifyJobStatus> | undefined;
  resolvePreview: (item: ItemModel) => string;
}) {
  useLayoutEffect(() => {
    const kind = resolveKind(item);
    if (kind === undefined) return; // nothing settled to report yet
    updateSubagentRowIfExists(item.turnId, resolveKey(item), {
      kind,
      resultPreview: resolvePreview(item),
      completedAt: item.completedAt,
    });
  });
  return <HeadClippedOutputBody item={item} live={false} />;
}

registerToolRenderer({
  match: (name) => name === "job_status" || name === "job_read_output",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const parsedOutput = parseJSONObject(item.output);
    const jobId = (parsedOutput && str(parsedOutput, "job_id")) ?? str(args, "job_id") ?? "";
    const status = parsedOutput ? str(parsedOutput, "status") : undefined;
    return status ? `Checked ${clip(jobId, ID_CLIP)} · ${status}` : `Checked ${clip(jobId, ID_CLIP)}`;
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
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const status = args["status"];
    const filter = Array.isArray(status) ? status.filter((s) => typeof s === "string").join(", ") : "";
    return filter ? `Listed jobs (${filter})` : "Listed jobs";
  },
  body: HeadClippedOutputBody,
});

registerToolRenderer({
  match: "job_stop",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const jobId = str(args, "job_id") ?? "";
    const footer = trailingBracketFooter(item.output ?? "");
    return footer ? `Stopped ${clip(jobId, ID_CLIP)} · ${footer}` : `Stopped ${clip(jobId, ID_CLIP)}`;
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

registerToolRenderer({
  match: (name) => name === "delegate_send" || name === "job_send_message",
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const target = clip(delegateSendTarget(args), ID_CLIP);
    const footer = trailingBracketFooter(item.output ?? "");
    return footer ? `Messaged ${target} · ${footer}` : `Messaged ${target}`;
  },
  body(props: ToolRenderProps) {
    return (
      <CorrelatingBody
        {...props}
        resolveKey={(item) =>
          resolveRowKey(delegateSendTarget(parseArgs(item.argumentsJSON)), undefined, item.callId ?? item.id)
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

// Generic fallback for any other job_*-family tool (e.g. job_watch) not
// explicitly registered above - "match by predicate" per this project's
// own locked ToolRendererDescriptor doc comment. Exact matches above
// always win (toolRenderers.ts's own precedence rule), so this only ever
// resolves for a job_* name none of the specific descriptors claimed.
registerToolRenderer({
  match: (name) => name.startsWith("job_"),
  summary(item: ItemModel) {
    const args = parseArgs(item.argumentsJSON);
    const operation = str(args, "operation");
    return operation ? `${item.toolName}: ${operation}` : (item.toolName ?? "");
  },
  body: HeadClippedOutputBody,
});
