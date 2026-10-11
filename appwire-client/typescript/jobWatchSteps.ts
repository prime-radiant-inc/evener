// What a job_watch step says, in both clients. Ground truth:
// agent/session_tools_jobs.go's jobWatchToolResult (create/clear/catch-up),
// jobWatchListToolResult (list), and jobWatchInspectToolResult (inspect) ride
// the step's raw as the State field of tool.StateResult; its output is the
// formatJobWatch/formatJobWatchList/formatJobWatchInspect footer text and its
// argumentsJSON carries the operation plus the create args.
//
// Every duration reads humanized (after 300s is "in 5m", progress_interval_ms
// 120000 is "every 2m"). A watch's id is what a list, inspect or clear acted
// on; a create names its source and trigger instead.

import { jobStatusDisplay } from "./activityData";
import type { ItemModel } from "./model";
import { type StepWords, summaryOf, withDetail } from "./stepWords";
import { clip, parseArgs, str } from "./toolCallText";
import { filterSummaryPhrase, watchEventLabel, watchTriggerPhrases } from "./watchConditionPhrase";
import {
  asJsonObject,
  boolField,
  type ConditionSpec,
  conditionSpec,
  humanizeInterval,
  humanizeSeconds,
  type JsonObject,
  normalizeRow,
  numField,
  parseConditionText,
  sourceLabel,
  strArrayField,
  strField,
  type WatchRow,
  watchDisplayState,
} from "./watchRows";

/** The parts of a job_watch step its words read. */
export type JobWatchStep = Pick<ItemModel, "toolName" | "argumentsJSON" | "raw">;

/** watchDeliveryBudget in agent/job_watch.go: the condition-fire budget the Go
 * side's own notices name ("matched 50 times"). It is not the denominator for
 * a watch's deliveries count: cfg.deliveries counts every model-facing
 * delivery including periodic progress/timer ticks that never consume the
 * condition-fire budget (countWatchDeliveryLocked), so "N of 50" could read
 * past the budget ("55 of 50"). The count reads bare, labeled as what it is. */
export const WATCH_DELIVERY_BUDGET = 50;

const NOTE_HEAD_CHARS = 48;

/** A create result is a timer when it carries the timer's own fields
 * (after/repeat seconds plus the admitted note) and no trigger condition
 * (output_match, events, or event filter). Mirrors the producer:
 * marshalWatchResult reports AfterSeconds for a one-shot timer and
 * RepeatSeconds for a repeating one, leaving ProgressIntervalMS zero for
 * timers ("the result speaks in the units the model asked in"). */
export interface TimerSpec {
  afterSeconds?: number;
  repeatSeconds?: number;
  note?: string;
}

export function timerSpec(raw: JsonObject): TimerSpec | undefined {
  const afterSeconds = numField(raw, "after_seconds");
  const repeatSeconds = numField(raw, "repeat_seconds");
  if (afterSeconds === undefined && repeatSeconds === undefined) return undefined;
  if (strField(raw, "output_match") !== undefined) return undefined;
  if (strArrayField(raw, "events").length > 0) return undefined;
  if (asJsonObject(raw.event_filter) !== undefined) return undefined;
  return { afterSeconds, repeatSeconds, note: strField(raw, "note") };
}

// The progress cadence a condition line names ("every 2m"). Undefined when
// the watch has no progress cadence.
function progressCadence(spec: ConditionSpec): string | undefined {
  if (spec.progressIntervalMS === undefined) return undefined;
  return humanizeInterval(spec.progressIntervalMS / 1000);
}

function noteHead(note: string): string {
  const firstLine = note.split("\n")[0] ?? "";
  return clip(firstLine.trim(), NOTE_HEAD_CHARS);
}

// isRecognizedWatchResult gates the structured words and renderers: the raw
// must carry at least one field from the producer's result shapes (create:
// watching/timer/condition/note/catch-up/identity; list: watches arrays;
// inspect: watching/deliveries/created_at/end_reason/watch_id). An
// unrecognized object ({} or a legacy/future shape) falls back to the call's
// own verb and the raw footer text instead of an invented "Watch this
// session". Unknown shapes show the producer's own words, never an empty card.
const WATCH_RESULT_FIELDS = [
  "watching",
  "watches",
  "recent_watches",
  "watch_id",
  "source",
  "after_seconds",
  "repeat_seconds",
  "output_match",
  "events",
  "event_filter",
  "every",
  "progress_interval_ms",
  "note",
  "deliveries",
  "created_at",
  "end_reason",
  "ended_at",
  "terminal_catchup",
  "fired",
  "status",
  "replaced_existing",
];

export function isRecognizedWatchResult(raw: JsonObject): boolean {
  return WATCH_RESULT_FIELDS.some((field) => raw[field] !== undefined);
}

/** The operation a job_watch step ran. It prefers the call's own operation
 * arg (the verb the model used: create/list/inspect/clear) and falls back to
 * the result shape when the args are absent: a stored transcript predating
 * the arg, or a state whose shape already says what it is (list carries
 * watches[], inspect carries deliveries/created_at/end_reason, a clear
 * carries watching:false with a watch_id). */
export function jobWatchOperation(step: Pick<JobWatchStep, "argumentsJSON">, raw: JsonObject | undefined): string {
  const operation = str(parseArgs(step.argumentsJSON), "operation");
  if (operation) return operation;
  if (raw === undefined) return "";
  if (Array.isArray(raw.watches) || Array.isArray(raw.recent_watches)) return "list";
  if (typeof raw.deliveries === "number" || typeof raw.created_at === "string" || typeof raw.end_reason === "string") {
    return "inspect";
  }
  // A send-branch terminal catch-up carries watch_id + watching:false AND
  // terminal_catchup:true (runTerminalCatchup builds from the live config),
  // so the catch-up check runs before the clear fallback below; otherwise
  // an arg-less catch-up misreads as "Cleared …" and drops the terminal
  // outcome. Catch-up is a create serving mode; the default branches handle
  // it from here.
  if (boolField(raw, "terminal_catchup")) return "create";
  // The clear inference needs a create/clear marker beyond watching:false +
  // watch_id: a legacy or current inspect-miss is exactly that shape
  // ({watch_id, watching:false}, no other markers) and must read as an
  // inspect saying "not found", never "Cleared …". Genuine clear/create
  // results carry replaced_existing/fired explicitly (marshalWatchResult
  // serializes both with no omitempty, even when false: key presence, not
  // truthiness, is the test). Source is not a marker: a pending inspect
  // carries watching:false + source with no end_reason (a detached watch on
  // the terminal-flush rail), and must read as an inspect saying pending,
  // never "Cleared …".
  if (raw.watching === false && typeof raw.watch_id === "string") {
    if ("replaced_existing" in raw || "fired" in raw) return "clear";
    return "inspect";
  }
  if (raw.watching === true || typeof raw.note === "string" || typeof raw.output_match === "string") {
    return "create";
  }
  return "";
}

function createWords(raw: JsonObject, step: JobWatchStep): StepWords {
  if (boolField(raw, "terminal_catchup")) {
    const source = strField(raw, "source") ?? "";
    const status = strField(raw, "status");
    const reason = strField(raw, "reason");
    // The terminal outcome is the whole reason the condition can never
    // match, so the line names it ("Watch on job_a1b2 ended — job completed
    // before it could fire"). A catch-up that fired matched on the terminal
    // scan instead: same shape, opposite outcome.
    if (boolField(raw, "fired")) {
      return status
        ? { verb: "Watch on", target: source, after: `fired on terminal scan — ${jobStatusDisplay(status, reason)}` }
        : { verb: "Watch on", target: source, after: "fired" };
    }
    if (status) {
      // Machine statuses keep the "job <status>" subject ("job completed");
      // the command-outcome display words already name the subject.
      const display = jobStatusDisplay(status, reason);
      const subject = display === status ? `job ${status}` : display;
      return { verb: "Watch on", target: source, after: `ended — ${subject} before it could fire` };
    }
    return { verb: "Watch on", target: source, after: "ended — job ended before it could fire" };
  }
  const timer = timerSpec(raw);
  if (timer) {
    // A one-shot timer reminds once ("Remind me in 5m"); a repeating timer
    // keeps reminding on its cadence ("Reminds every 5m"). after_seconds
    // wins when both are somehow present; marshalWatchResult only ever sets
    // one. The note's head is what the reminder will say.
    const seconds = timer.afterSeconds ?? timer.repeatSeconds ?? 0;
    const head = timer.note ? noteHead(timer.note) : undefined;
    if (timer.afterSeconds !== undefined) return withDetail({ verb: `Remind me ${humanizeSeconds(seconds)}` }, head);
    const cadence = humanizeInterval(seconds).replace(/^every /, "");
    return withDetail({ verb: `Reminds every ${cadence}` }, head);
  }
  const source = sourceLabel(strField(raw, "source"));
  const condition = conditionSpec(raw, asJsonObject(parseArgs(step.argumentsJSON)));
  if (!condition) return { verb: `Watch ${source}` };
  // A note-only watch arms no trigger: there are no clauses to name, but the
  // note heads the line so the note is never lost. Mirrors the timer-note
  // shape ("Remind me in 5m · <head>") minus the cadence.
  if (
    condition.outputMatch === undefined &&
    condition.progressIntervalMS === undefined &&
    condition.events.length === 0 &&
    condition.filterToolName === undefined &&
    condition.filterStatus === undefined
  ) {
    return withDetail({ verb: `Watch ${source}` }, condition.note ? noteHead(condition.note) : undefined);
  }
  // Trigger clauses compose: output_match, events (+every throttle, filter),
  // and the progress heartbeat combine freely on a live watch (only timer
  // fields are mutually exclusive with conditions; the producer's own
  // watchConditionSummary joins every populated clause with "; "). The line
  // names every armed clause so none is silently dropped.
  const clauses: string[] = [];
  if (condition.outputMatch) clauses.push(`“${condition.outputMatch}”`);
  if (condition.events.length > 0) {
    const throttle = condition.every !== undefined ? ` (every ${condition.every})` : "";
    clauses.push(`${condition.events.map(watchEventLabel).join(", ")}${throttle}`);
  }
  // An event-filter watch names the watched shape in words, both statuses
  // explicitly, never the raw filter keys.
  if (condition.filterStatus || condition.filterToolName) clauses.push(filterSummaryPhrase(condition));
  const cadence = progressCadence(condition);
  if (cadence) clauses.push(cadence);
  if (clauses.length === 0) return { verb: `Watch ${source}` };
  // A bare heartbeat keeps its "Watch X · every 2m" shape: "for" needs a
  // trigger to read against.
  if (clauses.length === 1 && cadence) return { verb: `Watch ${source} · ${cadence}` };
  return { verb: `Watch ${source} for ${clauses.join(" · ")}` };
}

// endReasonPhrase renders a watch end_reason id in words, shared by list rows
// and inspect bodies so the two never drift. The ids are the whole set the
// backend records: cleared (explicit clear, agent/job_watch.go clearWatch,
// "watch cleared"), replaced (a newer watch took the key, "watch replaced"),
// fired (a one-shot timer retired by its only fire,
// clearWatchByIDMatchingWithReason), budget_exhausted (the condition-fire
// budget tripped, the Go notice's own "matched 50 times",
// watchBudgetClearedMessage), auto_removed_terminal (the watched job went
// terminal before the condition could match), and job_manager_closed
// (agent/jobs.go). Anything else falls back to the raw id, never an invented
// meaning.
export function endReasonPhrase(endReason: string | undefined): string {
  switch (endReason) {
    case "budget_exhausted":
      return `matched ${WATCH_DELIVERY_BUDGET} times (budget exhausted)`;
    case "auto_removed_terminal":
      return "watched job finished before it could fire";
    case "replaced":
      return "replaced by a newer watch";
    case "fired":
      return "fired";
    case "cleared":
      return "cleared";
    case "job_manager_closed":
      return "job manager closed";
    default:
      return endReason ?? "ended";
  }
}

function listCounts(raw: JsonObject): { active: number; pending: number; ended: number } {
  const live = Array.isArray(raw.watches) ? raw.watches : [];
  const recent = Array.isArray(raw.recent_watches) ? raw.recent_watches : [];
  let active = 0;
  let pending = 0;
  let liveEnded = 0;
  for (const entry of live) {
    const row = normalizeRow(entry);
    if (!row) continue;
    // Count by the same state a row's chip shows (watchDisplayState): a live
    // row carrying an end_reason is ended, not pending, so the count can
    // never disagree with its rows.
    const state = watchDisplayState(row);
    if (state === "watching") active += 1;
    else if (state === "pending") pending += 1;
    else liveEnded += 1;
  }
  // Recent watches are history entries: they ended (inspectResultFromWatchHistory).
  const ended = liveEnded + recent.filter((entry) => normalizeRow(entry) !== undefined).length;
  return { active, pending, ended };
}

function listWords(raw: JsonObject): StepWords {
  const { active, pending, ended } = listCounts(raw);
  const counts = [`${active} active`];
  if (pending > 0) counts.push(`${pending} pending`);
  if (ended > 0) counts.push(`${ended} ended`);
  return { verb: "Listed watches", after: `(${counts.join(" · ")})` };
}

function inspectWords(step: JobWatchStep, raw: JsonObject): StepWords {
  const id = strField(raw, "watch_id") ?? str(parseArgs(step.argumentsJSON), "watch_id");
  // No id anywhere (neither the result nor the call names one): there is
  // nothing to point at, so the line is the operation's verb.
  if (!id) return { verb: "job_watch: inspect" };
  const state = watchDisplayState({
    watching: boolField(raw, "watching"),
    source: strField(raw, "source"),
    endReason: strField(raw, "end_reason"),
  });
  // Deliveries counts every model-facing delivery (condition fires plus
  // periodic progress/timer ticks), bare, with no budget denominator.
  if (typeof raw.deliveries === "number" && state === "watching") {
    return { verb: "Inspected", target: id, detail: `watching · ${raw.deliveries} deliveries` };
  }
  // The line speaks the producer's footer words ("not found"), not the
  // internal state name (formatJobWatchInspect).
  return { verb: "Inspected", target: id, detail: state === "missing" ? "not found" : state };
}

/** A job_watch step's words: "Remind me in 5m" · "<the note's head>", "Listed
 * watches" "(2 active)", "Inspected" "watch_x" · "watching", "Cleared"
 * "watch_x". */
export function jobWatchWords(step: JobWatchStep): StepWords {
  const raw = asJsonObject(step.raw);
  // Without structured state (a stored transcript predating it, or a shape
  // the normalizer doesn't recognize) the line is the call's own verb, so it
  // never regresses to a bare tool name.
  if (!raw || !isRecognizedWatchResult(raw)) {
    const operation = str(parseArgs(step.argumentsJSON), "operation");
    return { verb: operation ? `job_watch: ${operation}` : (step.toolName ?? "job_watch") };
  }
  switch (jobWatchOperation(step, raw)) {
    case "list":
      return listWords(raw);
    case "inspect":
      return inspectWords(step, raw);
    case "clear": {
      const id = strField(raw, "watch_id") ?? str(parseArgs(step.argumentsJSON), "watch_id");
      return id ? { verb: "Cleared", target: id } : { verb: "Cleared watch" };
    }
    default:
      return createWords(raw, step);
  }
}

export const jobWatchSummary = summaryOf(jobWatchWords);

/** A watch row's state as a word: "watching", "pending", "ended", "not
 * found". */
export function watchRowStateWord(row: WatchRow): string {
  const state = watchDisplayState(row);
  return state === "missing" ? "not found" : state;
}

// A watch row's trigger and source in words ("in 5m · this session"),
// whatever became of the watch, and whether those words already name its
// note: a note-only watch's trigger is its note.
function rowTrigger(row: WatchRow): { phrase: string; namesNote: boolean } {
  const source = sourceLabel(row.source);
  if (!row.condition) return { phrase: source, namesNote: false };
  const parsed = parseConditionText(row.condition, row.note);
  // The trigger wording comes from the shared composer, so a row and the
  // watch card can never word the same condition differently.
  const { timer, bits } = watchTriggerPhrases(parsed);
  if (timer) return { phrase: `${timer} · ${source}`, namesNote: false };
  if (bits.length > 0) return { phrase: `${bits.join(" · ")} · ${source}`, namesNote: false };
  // No trigger bits parsed: the condition is either a bare note or
  // unrecognized grammar. A note-only row still names its note (the
  // structured field verbatim, else the parsed note: clause), never the raw
  // Condition grammar ("note: …"). Anything else names just the source
  // rather than echoing machine tokens.
  const fallbackNote = row.note ?? parsed.note;
  if (fallbackNote) return { phrase: `${fallbackNote} · ${source}`, namesNote: true };
  return { phrase: source, namesNote: false };
}

/** A watch row's trigger and source in words ("in 5m · this session"), or
 * what became of it ("ended: fired", "not found"). List rows and inspect
 * bodies share it so the two never drift. */
export function rowConditionPhrase(row: WatchRow): string {
  const state = watchDisplayState(row);
  if (state === "watching") return rowTrigger(row).phrase;
  // A missing watch has no source to name: sourceLabel would invent "this
  // session" for a watch that is not there.
  if (state === "missing") return "not found";
  if (state === "pending") return `pending · ${sourceLabel(row.source)}`;
  return row.endReason ? `ended: ${endReasonPhrase(row.endReason)}` : "ended";
}

/** What a job_watch step shows when opened, in words, as the web's body shows
 * it: a list's rows ("watching  watch_x  in 5m · this session"), an inspected
 * watch's trigger (after what became of it, once it ended or waits) and note,
 * a create's note. "" when the line says it all (a
 * clear, a terminal catch-up, a create with no note); undefined when the
 * step's state isn't one this build reads, so a client shows what the tool
 * printed. */
export function jobWatchEvidence(step: JobWatchStep): string | undefined {
  const raw = asJsonObject(step.raw);
  if (!raw || !isRecognizedWatchResult(raw)) return undefined;
  switch (jobWatchOperation(step, raw)) {
    case "list": {
      const entries = [
        ...(Array.isArray(raw.watches) ? raw.watches : []),
        ...(Array.isArray(raw.recent_watches) ? raw.recent_watches : []),
      ];
      const rows = entries.map(normalizeRow).filter((row) => row !== undefined);
      if (rows.length === 0) return "No watches.";
      return rows.map((row) => `${watchRowStateWord(row)}  ${row.id}  ${rowConditionPhrase(row)}`).join("\n");
    }
    case "inspect": {
      const row = normalizeRow(raw);
      if (!row) return undefined;
      // The line names the watch and its state; a watch that isn't there has
      // nothing more to show.
      const state = watchDisplayState(row);
      if (state === "missing") return "";
      // What it watched for, in every state: an ended or pending watch says
      // what became of it first. Its note follows, unless the trigger's
      // words already are the note.
      const trigger = rowTrigger(row);
      const lines = state === "watching" ? [] : [state === "pending" ? "pending" : rowConditionPhrase(row)];
      lines.push(trigger.phrase);
      if (row.note && !trigger.namesNote) lines.push(row.note);
      return lines.join("\n");
    }
    case "clear":
      return "";
    default:
      if (boolField(raw, "terminal_catchup")) return "";
      return strField(raw, "note") ?? "";
  }
}
