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
import { registerToolRenderer } from "../toolRenderers";
import { clip, parseJSONObject, rememberedArgs, str, trailingBracketFooter } from "./helpers";
import { HeadClippedOutputBody } from "./bodies";
import type { ItemModel } from "../../../../protocol/model";

const ID_CLIP = 26;

registerToolRenderer({
  match: (name) => name === "job_status" || name === "job_read_output",
  summary(item: ItemModel) {
    const args = rememberedArgs(item);
    const parsedOutput = parseJSONObject(item.output);
    const jobId = (parsedOutput && str(parsedOutput, "job_id")) ?? str(args, "job_id") ?? "";
    const status = parsedOutput ? str(parsedOutput, "status") : undefined;
    return status ? `Checked ${clip(jobId, ID_CLIP)} · ${status}` : `Checked ${clip(jobId, ID_CLIP)}`;
  },
  // Output is already JSON text (see this file's own header) - the same
  // head-clipped raw-text body every other cheap tool in this directory
  // uses is a fine, honest way to show it.
  body: HeadClippedOutputBody,
});

registerToolRenderer({
  match: "job_list",
  summary(item: ItemModel) {
    const args = rememberedArgs(item);
    const status = args["status"];
    const filter = Array.isArray(status) ? status.filter((s) => typeof s === "string").join(", ") : "";
    return filter ? `Listed jobs (${filter})` : "Listed jobs";
  },
  body: HeadClippedOutputBody,
});

registerToolRenderer({
  match: "job_stop",
  summary(item: ItemModel) {
    const args = rememberedArgs(item);
    const jobId = str(args, "job_id") ?? "";
    const footer = trailingBracketFooter(item.output ?? "");
    return footer ? `Stopped ${clip(jobId, ID_CLIP)} · ${footer}` : `Stopped ${clip(jobId, ID_CLIP)}`;
  },
  body: HeadClippedOutputBody,
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
    const args = rememberedArgs(item);
    const target = clip(delegateSendTarget(args), ID_CLIP);
    const footer = trailingBracketFooter(item.output ?? "");
    return footer ? `Messaged ${target} · ${footer}` : `Messaged ${target}`;
  },
  body: HeadClippedOutputBody,
});

// Generic fallback for any other job_*-family tool (e.g. job_watch) not
// explicitly registered above - "match by predicate" per this project's
// own locked ToolRendererDescriptor doc comment. Exact matches above
// always win (toolRenderers.ts's own precedence rule), so this only ever
// resolves for a job_* name none of the specific descriptors claimed.
registerToolRenderer({
  match: (name) => name.startsWith("job_"),
  summary(item: ItemModel) {
    const args = rememberedArgs(item);
    const operation = str(args, "operation");
    return operation ? `${item.toolName}: ${operation}` : (item.toolName ?? "");
  },
  body: HeadClippedOutputBody,
});
