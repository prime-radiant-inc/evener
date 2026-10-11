// What a job step says, in both clients: which job a job_status or job_stop
// call acted on and what it found, and what a job_list call listed. The web's
// job rows and the phone's step lines read these.

import { jobStatusDisplay } from "./activityData";
import type { ItemModel } from "./model";
import { type StepWords, summaryOf, withDetail } from "./stepWords";
import { parseArgs, str } from "./toolCallText";
import { toolJSONResult } from "./toolEvidence";

/** The parts of a job step its words read. */
export type JobStep = Pick<ItemModel, "argumentsJSON" | "output">;

// The job a call named: `target`, or job_id in the retired job_read_output.
function namedJob(step: Pick<JobStep, "argumentsJSON">): string | undefined {
  const args = parseArgs(step.argumentsJSON);
  return str(args, "target") ?? str(args, "job_id");
}

// "<verb> <job>", the job its target, or "<verb> a job" when the call names
// none.
function onJob(verb: string, job: string | undefined): StepWords {
  return job ? { verb, target: job } : { verb: `${verb} a job` };
}

/** "Checked job_x · running": the job's id as its result echoes it, else as
 * the call named it, and its status once the result says. */
export function jobStatusWords(step: JobStep): StepWords {
  const result = toolJSONResult(step.output);
  const job = (result && (str(result, "id") ?? str(result, "job_id"))) ?? namedJob(step);
  const status = result ? str(result, "status") : undefined;
  const reason = result ? str(result, "reason") : undefined;
  return withDetail(onJob("Checked", job), status ? jobStatusDisplay(status, reason) : undefined);
}

/** "Listed jobs", or "Listed jobs (running, failed)" for a list filtered by
 * status. */
export function jobListWords(step: JobStep): StepWords {
  const status = parseArgs(step.argumentsJSON).status;
  const filter = Array.isArray(status) ? status.filter((s) => typeof s === "string").join(", ") : "";
  return filter ? { verb: "Listed jobs", after: `(${filter})` } : { verb: "Listed jobs" };
}

// A stop's result opens with its footer (agent/session_tools_jobs.go's
// formatJobStop): "[<kind> <id> · <status> · <outcome> · <reason> · …]", a
// subagent's followed by lines of its own. The line words the status as every
// job surface does, with the reason that can change it ("failed" with
// exit_nonzero is "Command failed"); the kind, the repeated id and the codes
// stay in the evidence.
const STOP_FOOTER_RE = /^\[([^\]\n]*)\]/;

/** "Stopped job_x · cancelled": the status its result reports. */
export function jobStopWords(step: JobStep): StepWords {
  const segments = STOP_FOOTER_RE.exec(step.output ?? "")?.[1]?.split(" · ") ?? [];
  const status = segments[1];
  const reason = segments[3]?.startsWith("was ") ? undefined : segments[3];
  return withDetail(onJob("Stopped", namedJob(step)), status ? jobStatusDisplay(status, reason) : undefined);
}

export const jobStatusSummary = summaryOf(jobStatusWords);
export const jobListSummary = summaryOf(jobListWords);
export const jobStopSummary = summaryOf(jobStopWords);

/** What a running job call is doing: "Checking job_x", "Listing jobs". */
export function jobProgress(toolName: string, step: Pick<JobStep, "argumentsJSON">): string | undefined {
  const job = namedJob(step);
  if (toolName === "job_status" || toolName === "job_read_output") return job ? `Checking ${job}` : "Checking a job";
  if (toolName === "job_list") return "Listing jobs";
  if (toolName === "job_stop") return job ? `Stopping ${job}` : "Stopping a job";
  return undefined;
}
