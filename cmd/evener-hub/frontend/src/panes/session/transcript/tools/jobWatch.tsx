// job_watch descriptor (mockups 23-job-watch §A-D). Ground truth:
// agent/session_tools_jobs.go's jobWatchToolResult (create/clear/catch-up),
// jobWatchListToolResult (list), and jobWatchInspectToolResult (inspect)
// ride item.raw as the State field of tool.StateResult; item.output is the
// formatJobWatch/formatJobWatchList/formatJobWatchInspect footer text and
// item.argumentsJSON carries the operation plus the create args.
//
// Decisions locked in the mockup pass: humanize every duration (after 300s
// → "in 5m", progress_interval_ms 120000 → "every 2m"; absolute clock only
// for created_at); notes render in full to ~20 lines, disclosure only
// beyond; no watch id on single-watch surfaces (list rows, inspect
// summaries, raw disclosures only); status chips only in list rows, where
// watching vs ended varies per row; clear and terminal catch-up are quiet
// one-liners — the summary line IS the rendering, expanded body empty.

import type { ReactNode } from "react";
import { useState } from "react";
import type { ItemModel } from "../../../../protocol/model";
import { clip, clipJobID, parseArgs, str } from "../../../../protocol/toolCallText";
import { Chip } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { HeadClippedOutputBody } from "./bodies";
import styles from "./jobWatch.module.css";

const CLASS = {
  card: requireClass(styles.card, "jobWatch.module.css", "card"),
  list: requireClass(styles.list, "jobWatch.module.css", "list"),
  section: requireClass(styles.section, "jobWatch.module.css", "section"),
  note: requireClass(styles.note, "jobWatch.module.css", "note"),
  noteClamped: requireClass(styles.noteClamped, "jobWatch.module.css", "noteClamped"),
  trigger: requireClass(styles.trigger, "jobWatch.module.css", "trigger"),
  mono: requireClass(styles.mono, "jobWatch.module.css", "mono"),
  row: requireClass(styles.row, "jobWatch.module.css", "row"),
  rowStatic: requireClass(styles.rowStatic, "jobWatch.module.css", "rowStatic"),
  rowId: requireClass(styles.rowId, "jobWatch.module.css", "rowId"),
  rowCondition: requireClass(styles.rowCondition, "jobWatch.module.css", "rowCondition"),
  disclosureSummary: requireClass(styles.disclosureSummary, "jobWatch.module.css", "disclosureSummary"),
};

// watchDeliveryBudget in agent/job_watch.go: the condition-fire budget the
// Go side's own notices name ("matched 50 times"). It is NOT the denominator
// for the deliveries count below: cfg.deliveries counts every model-facing
// delivery including periodic progress/timer ticks that never consume the
// condition-fire budget (countWatchDeliveryLocked), so "N of 50" can read
// past the budget ("55 of 50"). The count renders bare, labeled as what it
// is (combined RoboRev review).
const WATCH_DELIVERY_BUDGET = 50;

// NOTE_CLAMP_LINES is the note policy, not a measurement: notes at or under
// ~20 lines render in full with no disclosure; longer notes clamp behind
// "Show full note". The line count is a rough split on "\n" (a prose note's
// lines are display lines, not data), so the boundary is approximate by
// design — the mockup's "~20 lines".
const NOTE_CLAMP_LINES = 20;

const NOTE_HEAD_CHARS = 48;

type JsonObject = Record<string, unknown>;

function asJsonObject(value: unknown): JsonObject | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as JsonObject) : undefined;
}

function strField(object: JsonObject, key: string): string | undefined {
  const value = object[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

function numField(object: JsonObject, key: string): number | undefined {
  const value = object[key];
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : undefined;
}

function boolField(object: JsonObject, key: string): boolean {
  return object[key] === true;
}

function strArrayField(object: JsonObject, key: string): string[] {
  const value = object[key];
  if (!Array.isArray(value)) return [];
  return value.filter((entry): entry is string => typeof entry === "string" && entry !== "");
}

// humanizeSeconds renders a caller-supplied duration in the units the model
// asked in: sub-minute stays in seconds ("in 45s", fractional "in 1.5s" for
// sub-second producer precision such as progress_interval_ms 1500);
// whole minutes collapse ("in 5m", "in 1m"); leftover seconds are kept
// ("in 1m30s", never a lossy "in 1m" — RoboRev PR #954); an hour or more
// names hours and leftover minutes ("in 1h05m"). Rounding happens FIRST, so
// a 60s carry can never surface ("in 1m60s" — combined RoboRev review):
// the seconds path runs iff the rounded value is below 60, and the minute
// path divides the rounded value, whose remainder is always below 60.
// Zero/negative never reaches here (numField filters it) — the caller falls
// back to the raw footer text instead of inventing one.
export function humanizeSeconds(totalSeconds: number): string {
  const rounded = Math.round(totalSeconds);
  if (rounded < 60) {
    if (Number.isInteger(totalSeconds)) return `in ${rounded}s`;
    return `in ${(Math.round(totalSeconds * 10) / 10).toString()}s`;
  }
  const totalMinutes = Math.floor(rounded / 60);
  const leftoverSeconds = rounded % 60;
  if (totalMinutes < 60) {
    return leftoverSeconds === 0
      ? `in ${totalMinutes}m`
      : `in ${totalMinutes}m${String(leftoverSeconds).padStart(2, "0")}s`;
  }
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return minutes === 0 ? `in ${hours}h` : `in ${hours}h${String(minutes).padStart(2, "0")}m`;
}

// humanizeInterval renders a caller-supplied cadence: sub-minute stays in
// seconds ("every 45s", fractional "every 1.5s"), whole minutes collapse
// ("every 2m"), leftover seconds are kept ("every 1m30s"); hours name hours
// ("every 1h"). Same round-first carry contract as humanizeSeconds.
export function humanizeInterval(totalSeconds: number): string {
  const rounded = Math.round(totalSeconds);
  if (rounded < 60) {
    if (Number.isInteger(totalSeconds)) return `every ${rounded}s`;
    return `every ${(Math.round(totalSeconds * 10) / 10).toString()}s`;
  }
  const totalMinutes = Math.floor(rounded / 60);
  const leftoverSeconds = rounded % 60;
  if (totalMinutes < 60) {
    return leftoverSeconds === 0
      ? `every ${totalMinutes}m`
      : `every ${totalMinutes}m${String(leftoverSeconds).padStart(2, "0")}s`;
  }
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return minutes === 0 ? `every ${hours}h` : `every ${hours}h${String(minutes).padStart(2, "0")}m`;
}

// A create result is a timer when it carries the timer's own fields
// (after/repeat seconds plus the admitted note) and no trigger condition
// (output_match, events, or event filter). Mirrors the producer:
// marshalWatchResult reports AfterSeconds for a one-shot timer and
// RepeatSeconds for a repeating one, leaving ProgressIntervalMS zero for
// timers ("the result speaks in the units the model asked in").
interface TimerSpec {
  afterSeconds?: number;
  repeatSeconds?: number;
  note?: string;
}

function timerSpec(raw: JsonObject): TimerSpec | undefined {
  const afterSeconds = numField(raw, "after_seconds");
  const repeatSeconds = numField(raw, "repeat_seconds");
  if (afterSeconds === undefined && repeatSeconds === undefined) return undefined;
  if (strField(raw, "output_match") !== undefined) return undefined;
  if (strArrayField(raw, "events").length > 0) return undefined;
  if (asJsonObject(raw.event_filter) !== undefined) return undefined;
  return { afterSeconds, repeatSeconds, note: strField(raw, "note") };
}

// A condition watch's trigger: the output pattern, the heartbeat cadence
// (progress_interval_ms from the wire), the event list plus its every-Nth
// throttle (RoboRev PR #954: `every: 3` fires on every third event, and
// dropping it claims every event fires), and the event-filter shape
// (assistant.tool errors on a delegate). Empty when the result names no
// condition at all — a bare source watch the summary still names.
interface ConditionSpec {
  outputMatch?: string;
  progressIntervalMS?: number;
  events: string[];
  every?: number;
  filterToolName?: string;
  filterStatus?: string;
  // Any watch carries a note (backend #995), delivered as raw.note on the
  // create result — not only timers. The create body renders it as a full
  // section alongside the condition sentence.
  note?: string;
}

function numArg(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? Math.floor(value) : undefined;
}

// everyArg reads the create-args throttle the way the backend stores it:
// every==1 is the semantic default (fire on each occurrence), normalized to
// unset everywhere downstream (normalizeWatchArgs), so the renderer treats
// every<=1 as absent — otherwise the same watch reads throttled in create
// but unthrottled in list/inspect. numArg's other callers keep raw numerics.
function everyArg(value: unknown): number | undefined {
  const n = numArg(value);
  return n !== undefined && n > 1 ? n : undefined;
}

function conditionSpec(raw: JsonObject, args?: JsonObject): ConditionSpec | undefined {
  const outputMatch = strField(raw, "output_match");
  const progressIntervalMS = numField(raw, "progress_interval_ms");
  const events = strArrayField(raw, "events");
  // `every` rides the create ARGS (DefJobWatch), not the result state —
  // read args first, falling back to the raw in case a producer echoes it.
  // Both sides normalize every<=1 to unset (see everyArg); the Condition
  // string itself never carries every:1 (the producer zeroes it before the
  // summary renders), only model-written args can.
  const every = (args ? everyArg(args.every) : undefined) ?? everyArg(raw.every);
  const filter = asJsonObject(raw.event_filter);
  const filterToolName = filter ? strField(filter, "tool_name") : undefined;
  const filterStatus = filter ? strField(filter, "status") : undefined;
  // The note rides raw.note on every create result (backend #995) — it is
  // the watch's own prose, rendered as a full section, never folded into
  // the condition sentence. A note alone still yields a spec: a valid watch
  // may carry only a note and no trigger, and the create body renders the
  // note section even without a trigger clause.
  const note = strField(raw, "note");
  if (
    outputMatch === undefined &&
    progressIntervalMS === undefined &&
    events.length === 0 &&
    !filter &&
    note === undefined
  ) {
    return undefined;
  }
  return { outputMatch, progressIntervalMS, events, every, filterToolName, filterStatus, note };
}

// The heartbeat phrase a condition sentence ends with, if the watch carries
// a progress cadence ("heartbeat every 2m"). Undefined when the watch has
// no progress_interval_ms — the sentence simply has no cadence clause.
function heartbeatPhrase(spec: ConditionSpec): string | undefined {
  if (spec.progressIntervalMS === undefined) return undefined;
  return `heartbeat ${humanizeInterval(spec.progressIntervalMS / 1000)}`;
}

// The cadence suffix a condition summary carries ("· every 2m").
// Undefined when the watch has no progress cadence.
function cadenceSuffix(spec: ConditionSpec): string | undefined {
  if (spec.progressIntervalMS === undefined) return undefined;
  return `· ${humanizeInterval(spec.progressIntervalMS / 1000)}`;
}

function sourceLabel(source: string | undefined): string {
  // The producer's self source is internal vocabulary (watchPublicSource):
  // readers see "this session" (mockups 23-job-watch), never a bare "self".
  return source === undefined || source === "self" ? "this session" : source;
}

// eventDisplayName renders one watched event kind in words. The producer's
// wildcard "*" reads as "any event" everywhere — rows, summaries, sentences,
// details — never as a bare "*" (combined RoboRev review).
function eventDisplayName(name: string): string {
  return name === "*" ? "any event" : name;
}

function noteHead(note: string): string {
  const firstLine = note.split("\n")[0] ?? "";
  return clip(firstLine.trim(), NOTE_HEAD_CHARS);
}

function isTerminalCatchup(raw: JsonObject): boolean {
  return boolField(raw, "terminal_catchup");
}

function isWatching(raw: JsonObject): boolean {
  return boolField(raw, "watching");
}

// isRecognizedWatchResult gates the structured renderer: the raw must carry
// at least one field from the producer's result shapes (create:
// watching/timer/condition/note/catch-up/identity; list: watches arrays;
// inspect: watching/deliveries/created_at/end_reason/watch_id). An
// unrecognized object — {} or a legacy/future shape — falls back to the raw
// footer text instead of rendering an empty card with an invented "Watch this
// session" summary (RoboRev PR #954 combined review). The fallback direction
// is deliberate: unknown shapes show the producer's own words, never an
// empty card.
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

function isRecognizedWatchResult(raw: JsonObject): boolean {
  return WATCH_RESULT_FIELDS.some((field) => raw[field] !== undefined);
}

// jobWatchOperation prefers the call's own operation arg (the verb the model
// used: create/list/inspect/clear) and falls back to the result shape when
// the args are absent — a stored transcript predating the arg, or a state
// whose shape already says what it is (list carries watches[], inspect
// carries deliveries/created_at/end_reason, a clear carries watching:false
// with a watch_id).
function jobWatchOperation(item: ItemModel, raw: JsonObject | undefined): string {
  const args = parseArgs(item.argumentsJSON);
  const operation = str(args, "operation");
  if (operation) return operation;
  if (raw === undefined) return "";
  if (Array.isArray(raw.watches) || Array.isArray(raw.recent_watches)) return "list";
  if (typeof raw.deliveries === "number" || typeof raw.created_at === "string" || typeof raw.end_reason === "string") {
    return "inspect";
  }
  // A send-branch terminal catch-up carries watch_id + watching:false AND
  // terminal_catchup:true (runTerminalCatchup builds from the live config),
  // so the catch-up check runs before the clear fallback below — otherwise
  // an arg-less catch-up misreads as "Cleared …" and drops the terminal
  // outcome. Catch-up is a create serving mode; the default branches handle
  // it from here.
  if (boolField(raw, "terminal_catchup")) return "create";
  // The clear inference needs a create/clear marker beyond watching:false +
  // watch_id: a legacy or current inspect-miss is exactly that shape
  // ({watch_id, watching:false}, no other markers) and must read as an
  // inspect rendering "not found", never "Cleared …". Genuine clear/create
  // results carry replaced_existing/fired explicitly (marshalWatchResult
  // serializes both with no omitempty, even when false — key presence, not
  // truthiness, is the test). Source is NOT a marker: a pending inspect
  // carries watching:false + source with no end_reason (a detached watch on
  // the terminal-flush rail), and must read as an inspect rendering pending,
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

function summarizeCreate(raw: JsonObject, item: ItemModel): string {
  if (isTerminalCatchup(raw)) {
    const source = strField(raw, "source") ?? "";
    const status = strField(raw, "status");
    // The terminal outcome is the whole reason the condition can never
    // match, so the one-liner names it ("Watch on job_a1b2 ended — job
    // completed before it could fire"). A catch-up that FIRED matched on
    // the terminal scan instead — same shape, opposite outcome.
    if (boolField(raw, "fired")) {
      return status ? `Watch on ${source} fired on terminal scan — ${status}` : `Watch on ${source} fired`;
    }
    return status
      ? `Watch on ${source} ended — job ${status} before it could fire`
      : `Watch on ${source} ended — job ended before it could fire`;
  }
  const timer = timerSpec(raw);
  if (timer) {
    // A one-shot timer reminds once ("Remind me in 5m"); a repeating timer
    // keeps reminding on its cadence ("Reminds every 5m"). after_seconds
    // wins when both are somehow present — marshalWatchResult only ever
    // sets one.
    const seconds = timer.afterSeconds ?? timer.repeatSeconds ?? 0;
    const head = timer.note ? noteHead(timer.note) : "";
    if (timer.afterSeconds !== undefined) {
      return head ? `Remind me ${humanizeSeconds(seconds)} · ${head}` : `Remind me ${humanizeSeconds(seconds)}`;
    }
    const cadence = humanizeInterval(seconds).replace(/^every /, "");
    return head ? `Reminds every ${cadence} · ${head}` : `Reminds every ${cadence}`;
  }
  const source = sourceLabel(strField(raw, "source"));
  const condition = conditionSpec(raw, asJsonObject(parseArgs(item.argumentsJSON)));
  if (!condition) return `Watch ${source}`;
  // A note-only watch arms no trigger: there are no clauses to name, but the
  // note heads the summary so the note is never lost. Mirrors the timer-note
  // shape ("Remind me in 5m · <head>") minus the cadence.
  if (
    condition.outputMatch === undefined &&
    condition.progressIntervalMS === undefined &&
    condition.events.length === 0 &&
    condition.filterToolName === undefined &&
    condition.filterStatus === undefined
  ) {
    return condition.note ? `Watch ${source} · ${noteHead(condition.note)}` : `Watch ${source}`;
  }
  // Trigger clauses compose: output_match, events (+every throttle, filter),
  // and the progress heartbeat combine freely on a live watch (only timer
  // fields are mutually exclusive with conditions — the producer's own
  // watchConditionSummary joins every populated clause with "; "). The
  // summary names every armed clause so none is silently dropped (combined
  // RoboRev review).
  const clauses: string[] = [];
  if (condition.outputMatch) clauses.push(`“${condition.outputMatch}”`);
  if (condition.events.length > 0) {
    const throttle = condition.every !== undefined ? ` (every ${condition.every})` : "";
    clauses.push(`${condition.events.map(eventDisplayName).join(", ")}${throttle}`);
  }
  if (condition.filterStatus || condition.filterToolName) {
    // An event-filter watch names the watched shape in words — both
    // statuses explicitly (RoboRev PR #954: status "ok" was discarded).
    // Never the raw filter keys.
    clauses.push(filterSummaryPhrase(condition));
  }
  const cadence = cadenceSuffix(condition);
  if (cadence) clauses.push(cadence.replace(/^· /, ""));
  if (clauses.length === 0) return `Watch ${source}`;
  // A bare heartbeat keeps its established "Watch X · every 2m" shape
  // (RoboRev PR #954 review 3, finding F) — "for" needs a trigger to read
  // against.
  if (clauses.length === 1 && cadence) return `Watch ${source} ${cadence}`;
  return `Watch ${source} for ${clauses.join(" · ")}`;
}

// filterSummaryPhrase names an event-filter watch's shape in words, shared by
// summaries, list rows, and row details so the three never drift (RoboRev PR
// #954 review 3): error and ok both explicit with the tool named when
// present; a status-less filter still names the tool ("calls on …"), and a
// bare filter with neither reads "matching events".
interface FilterPhrase {
  filterToolName?: string;
  filterStatus?: string;
}

function filterSummaryPhrase(condition: FilterPhrase): string {
  const tool = condition.filterToolName ? ` on ${condition.filterToolName}` : "";
  if (condition.filterStatus === "error") return `failed tool calls${tool}`;
  if (condition.filterStatus === "ok") return `successful tool calls${tool}`;
  return condition.filterToolName ? `calls on ${condition.filterToolName}` : "matching events";
}

interface WatchRow {
  id: string;
  watching: boolean;
  source?: string;
  condition?: string;
  // The structured note field beside the Condition string (verbatim even
  // when the note contains delimiter-looking text). Preferred over the
  // note: clause embedded in the Condition string.
  note?: string;
  endReason?: string;
  deliveries?: number;
}

function normalizeRow(value: unknown): WatchRow | undefined {
  const row = asJsonObject(value);
  const id = row ? strField(row, "watch_id") : undefined;
  if (!row || !id) return undefined;
  const deliveries = typeof row.deliveries === "number" ? row.deliveries : undefined;
  return {
    id,
    watching: row.watching === true,
    source: strField(row, "source"),
    condition: strField(row, "condition"),
    note: strField(row, "note"),
    endReason: strField(row, "end_reason"),
    deliveries,
  };
}

// A parsed inspect/list Condition string. The producer renders a watch
// config's trigger as one "; "-joined line (watchConditionSummary,
// agent/job_watch.go:2460-2494, filter grammar watchEventFilterSummary
// :2497-2508): `output_match: …`; `after_seconds: N` / `repeat_seconds: N`
// / `progress_interval_ms: N`; `note: …`; `events: [*]` or
// `events: [a, b]` with optional `every N` and `where tool_name=X,
// status=Y`. The reader parses that embedded grammar back — inventing
// nothing. The note: clause is the fallback source for the note: the raw
// also carries the note in its own structured field (verbatim, immune to
// delimiter-looking text), which readers prefer; this parse covers stored
// frames that predate it.
interface ParsedCondition {
  outputMatch?: string;
  afterSeconds?: number;
  repeatSeconds?: number;
  progressIntervalMS?: number;
  events: string[];
  every?: number;
  filterToolName?: string;
  filterStatus?: string;
  // The watch's own prose payload (backend #995: any watch carries one). The
  // value is dot-all — notes are multiline prose, and patterns may also span
  // lines — so all value patterns below use [\s\S], never dot.
  note?: string;
}

function numAfter(value: string | undefined): number | undefined {
  if (value === undefined) return undefined;
  const n = Number(value);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

// Split only on semicolons that introduce a recognized Condition field.
// output_match is caller-supplied and unbounded, so a pattern may itself
// contain ";" — splitting on every one truncates the pattern (RoboRev PR
// #954 review 3). The heads below are the producer's exact grammar
// (watchConditionSummary, agent/job_watch.go:2460-2494): "output_match: ",
// "after_seconds: N" / "repeat_seconds: N" / "progress_interval_ms: N",
// "note: ", "events: ...", joined with "; ". (A pattern literally containing
// "; events: " stays ambiguous even to the producer's own join — the split
// takes the field reading, matching what list/inspect show.)
const CONDITION_PART_SPLIT = /;\s*(?=(?:output_match|after_seconds|repeat_seconds|progress_interval_ms|note|events):)/;

// WATCH_MESSAGE_MAX_CHARS and WATCH_TRUNCATED_INDICATOR mirror the
// producer's bounds (agent/job_watch.go): the Condition string embeds the
// note via limitWatchText(cfg.note, watchMessageMaxChars), which truncates by
// rune with a "\n[truncated]" indicator and performs no whitespace or line
// folding — the embedding is otherwise the verbatim note value.
const WATCH_MESSAGE_MAX_CHARS = 2048;
const WATCH_TRUNCATED_INDICATOR = "\n[truncated]";

// embeddedNoteCandidates lists the exact strings the producer may have
// embedded as the note: clause for a structured note value: the value itself
// (cfg.note is already bounded at storage, so this is the live case), plus
// the limitWatchText truncation for oversized values from stored frames.
function embeddedNoteCandidates(note: string): string[] {
  const runes = Array.from(note);
  if (runes.length <= WATCH_MESSAGE_MAX_CHARS) return [note];
  const keep = WATCH_MESSAGE_MAX_CHARS - Array.from(WATCH_TRUNCATED_INDICATOR).length;
  const truncated =
    keep <= 0
      ? runes.slice(0, WATCH_MESSAGE_MAX_CHARS).join("")
      : runes.slice(0, keep).join("") + WATCH_TRUNCATED_INDICATOR;
  return truncated === note ? [note] : [note, truncated];
}

// stripStructuredNoteClause removes the exact note: clause the producer
// embedded in the flattened Condition string, keyed by the structured note
// field's verbatim value. The note is free prose that may itself contain
// delimiter-looking text ("; events: […]") which the head-based split below
// would otherwise parse as real armed triggers. Only a clause-boundary match
// is removed — the note: head at the string start or right after the "; "
// join — and only one adjacent join separator goes with it, so neighboring
// trigger clauses rejoin intact. Without a structured note (legacy stored
// frames) the condition parses unchanged.
function stripStructuredNoteClause(condition: string, note: string | undefined): string {
  if (!note) return condition;
  for (const value of embeddedNoteCandidates(note)) {
    const needle = `note: ${value}`;
    let index = condition.indexOf(needle);
    while (index !== -1) {
      const before = condition.slice(0, index);
      if (index === 0 || /;\s*$/.test(before)) {
        let start = index;
        const leading = /;\s*$/.exec(before);
        if (leading) start = leading.index;
        let end = index + needle.length;
        if (index === 0) {
          const trailing = /^;\s*/.exec(condition.slice(end));
          if (trailing) end += trailing[0].length;
        }
        return condition.slice(0, start) + condition.slice(end);
      }
      index = condition.indexOf(needle, index + 1);
    }
  }
  return condition;
}

function parseConditionText(condition: string, note?: string): ParsedCondition {
  const parsed: ParsedCondition = { events: [] };
  // The note head is one clause among the split parts: the producer's join
  // order puts note: before the events clause (watchConditionSummary), so a
  // legacy persisted Condition reads "output_match: …; note: …; events: […]"
  // and every trigger clause parses beside the note (RoboRev PR #954). The
  // note value runs to the next head or end-of-string — a note that itself
  // contains delimiter-looking text ("; events: […]") truncates here, which
  // is why list/inspect raws also carry the note in its own structured field
  // (readers prefer that field; this parse is the fallback for stored frames
  // that predate it). Slice-free: every part parses independently, so the
  // note's own whitespace survives verbatim.
  // When the caller passes the structured note, its exact embedded clause is
  // stripped first (stripStructuredNoteClause) so delimiter-looking prose
  // inside the note never parses as armed triggers.
  // (A caller-supplied output_match containing "; note: " stays ambiguous
  // even to the producer's own join — the split takes the field reading,
  // matching what list/inspect show.)
  for (const part of stripStructuredNoteClause(condition, note).split(CONDITION_PART_SPLIT)) {
    const text = part.trim();
    // A leading "note:" head (a bare note-only string, which the producer
    // never emits but a stored transcript could carry) is the whole note.
    const note = /^(?:note:\s*)([\s\S]+)$/.exec(text)?.[1]?.trim();
    if (note) {
      if (!parsed.note) parsed.note = note;
      continue;
    }
    const outputMatch = /^output_match:\s*([\s\S]+)$/.exec(text)?.[1]?.trim();
    if (outputMatch) {
      parsed.outputMatch = outputMatch;
      continue;
    }
    const afterSeconds = /^after_seconds:\s*(\d+)/.exec(text)?.[1];
    if (afterSeconds !== undefined) {
      parsed.afterSeconds = numAfter(afterSeconds);
      continue;
    }
    const repeatSeconds = /^repeat_seconds:\s*(\d+)/.exec(text)?.[1];
    if (repeatSeconds !== undefined) {
      parsed.repeatSeconds = numAfter(repeatSeconds);
      continue;
    }
    const progressMS = /^progress_interval_ms:\s*(\d+)/.exec(text)?.[1];
    if (progressMS !== undefined) {
      parsed.progressIntervalMS = numAfter(progressMS);
      continue;
    }
    const eventsClause = /^events:\s*\[(.*)\]\s*(?:every\s+(\d+))?\s*(?:where\s+(.+))?$/.exec(text);
    if (eventsClause) {
      parsed.events = (eventsClause[1] ?? "")
        .split(",")
        .map((name) => name.trim())
        .filter((name) => name !== "");
      parsed.every = numAfter(eventsClause[2]);
      const whereClause = (eventsClause[3] ?? "").trim();
      if (whereClause) {
        const tool = /tool_name=([^,\s]+)/.exec(whereClause)?.[1];
        const status = /status=([^,\s]+)/.exec(whereClause)?.[1];
        if (tool) parsed.filterToolName = tool;
        if (status) parsed.filterStatus = status;
      }
    }
    // Anything unrecognized degrades to the fallback below rather than
    // inventing rendering. (The note head is terminal and handled above,
    // so reaching here means this part is genuinely something else.)
  }
  return parsed;
}

// conditionSentence renders one humanized trigger sentence from a parsed
// Condition: pattern, timer cadence, heartbeat, events, and filter in
// prose, machine tokens in mono. Shared by list rows (short form) and
// inspect bodies (full form) so the two never drift.
function rowConditionPhrase(row: WatchRow): string {
  const state = watchState(row);
  if (state === "watching") {
    if (row.condition) {
      const parsed = parseConditionText(row.condition, row.note);
      const source = sourceLabel(row.source);
      if (parsed.afterSeconds !== undefined) return `${humanizeSeconds(parsed.afterSeconds)} · ${source}`;
      if (parsed.repeatSeconds !== undefined) {
        return `every ${humanizeInterval(parsed.repeatSeconds).replace(/^every /, "")} · ${source}`;
      }
      const bits: string[] = [];
      if (parsed.outputMatch) bits.push(`“${parsed.outputMatch}”`);
      // The every throttle rides the events bit when one renders, else the
      // filter bit — it is one shared throttle ("events: […] every N where …"),
      // so it must never print twice. Parens match the create summary's
      // "(every N)" shape (RoboRev PR #954 review 3).
      const every = parsed.every !== undefined ? ` (every ${parsed.every})` : "";
      if (parsed.events.length > 0) {
        const names = parsed.events.map(eventDisplayName).join(", ");
        bits.push(`${names}${every}`);
      }
      if (parsed.filterToolName || parsed.filterStatus) {
        bits.push(`${filterSummaryPhrase(parsed)}${parsed.events.length === 0 ? every : ""}`);
      }
      if (parsed.progressIntervalMS !== undefined) {
        bits.push(humanizeInterval(parsed.progressIntervalMS / 1000));
      }
      if (bits.length > 0) return `${bits.join(" · ")} · ${source}`;
      // No trigger bits parsed: the condition is either a bare note or
      // unrecognized grammar. A note-only row still names its note (the
      // structured field verbatim, else the parsed note: clause) — never the
      // raw Condition grammar ("note: …"). Anything else names just the
      // source rather than echoing machine tokens.
      const fallbackNote = row.note ?? parsed.note;
      if (fallbackNote) return `${fallbackNote} · ${source}`;
      return source;
    }
    return sourceLabel(row.source);
  }
  // A missing watch has no source to name — sourceLabel would invent "this
  // session" for a watch that is not there (RoboRev PR #954 combined review).
  if (state === "missing") return "not found";
  if (state === "pending") return `pending · ${sourceLabel(row.source)}`;
  return row.endReason ? `ended: ${endReasonPhrase(row.endReason)}` : "ended";
}

// watchState reads a watch row/inspect result's lifecycle state in the
// producer's own three-way grammar (agent/session_tools_jobs.go
// formatJobWatchInspect, which is also what watchInspectFound in the same file
// gates on): watching; end_reason set (ended); source set without end_reason
// (pending — a detached watch on the terminal-flush rail still holding
// frames); neither (not found — inspectWatchByID's empty return). Collapsing
// pending and missing into "ended" misreports both (RoboRev PR #954 combined
// review).
type WatchState = "watching" | "pending" | "ended" | "missing";

function watchState(entry: { watching: boolean; source?: string; endReason?: string }): WatchState {
  if (entry.watching) return "watching";
  if (entry.endReason) return "ended";
  if (entry.source) return "pending";
  return "missing";
}

// endReasonPhrase renders a watch end_reason id in words, shared by list rows
// and inspect bodies so the two never drift (8a56741 finding 4). The ids are
// the whole set the backend records: cleared (explicit clear,
// agent/job_watch.go clearWatch, "watch cleared"), replaced (a newer watch
// took the key, agent/job_watch.go:724 "watch replaced"), fired (a one-shot
// timer retired by its only fire, agent/job_watch.go:3459
// clearWatchByIDMatchingWithReason), budget_exhausted (the condition-fire
// budget tripped — the Go notice's own "matched 50 times", agent/job_watch.go
// watchBudgetClearedMessage), auto_removed_terminal (the watched job went
// terminal before the condition could match, agent/job_watch.go:3060), and
// job_manager_closed (agent/jobs.go:737). Anything else falls back to the raw
// id — never an invented meaning.
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
    // Count by the same state the row chip renders (watchState): a live row
    // carrying an end_reason is ended, not pending, so the summary can never
    // disagree with its rows (combined RoboRev review).
    const state = watchState(row);
    if (state === "watching") active += 1;
    else if (state === "pending") pending += 1;
    else liveEnded += 1;
  }
  // Recent watches are history entries: they ended (inspectResultFromWatchHistory).
  const ended = liveEnded + recent.filter((entry) => normalizeRow(entry) !== undefined).length;
  return { active, pending, ended };
}

function summarizeList(raw: JsonObject): string {
  const { active, pending, ended } = listCounts(raw);
  const activeWord = active === 1 ? "1 active" : `${active} active`;
  const rest: string[] = [];
  if (pending > 0) rest.push(pending === 1 ? "1 pending" : `${pending} pending`);
  if (ended > 0) rest.push(ended === 1 ? "1 ended" : `${ended} ended`);
  return rest.length > 0 ? `Listed watches (${activeWord} · ${rest.join(" · ")})` : `Listed watches (${activeWord})`;
}

function summarizeInspect(item: ItemModel, raw: JsonObject): string {
  const args = parseArgs(item.argumentsJSON);
  const id = strField(raw, "watch_id") ?? str(args, "watch_id") ?? "";
  // No id anywhere (neither the result nor the call names one): there is
  // nothing to point at, so degrade to the operation verb rather than
  // rendering "Inspected  · …" with an empty id (RoboRev PR #954 combined
  // review).
  if (!id) return "job_watch: inspect";
  const state = watchState({
    watching: isWatching(raw),
    source: strField(raw, "source"),
    endReason: strField(raw, "end_reason"),
  });
  const deliveries = typeof raw.deliveries === "number" ? raw.deliveries : undefined;
  // Deliveries counts every model-facing delivery (condition fires plus
  // periodic progress/timer ticks) — bare, with no budget denominator.
  if (deliveries !== undefined && state === "watching") {
    return `Inspected ${id} · watching · ${deliveries} deliveries`;
  }
  // The summary speaks the producer's footer words ("not found"), not the
  // internal state name (formatJobWatchInspect).
  return `Inspected ${id} · ${state === "missing" ? "not found" : state}`;
}

function jobWatchSummary(item: ItemModel): string {
  const raw = asJsonObject(item.raw);
  // Without structured state (a stored transcript predating it, or a shape
  // the normalizer doesn't recognize) fall back to the call's own verb —
  // the same "job_watch: <operation>" the family fallback rendered, so the
  // row never regresses to a bare tool name.
  if (!raw || !isRecognizedWatchResult(raw)) {
    const args = parseArgs(item.argumentsJSON);
    const operation = str(args, "operation");
    return operation ? `job_watch: ${operation}` : (item.toolName ?? "job_watch");
  }
  const operation = jobWatchOperation(item, raw);
  switch (operation) {
    case "list":
      return summarizeList(raw);
    case "inspect":
      return summarizeInspect(item, raw);
    case "clear": {
      const args = parseArgs(item.argumentsJSON);
      const id = strField(raw, "watch_id") ?? str(args, "watch_id") ?? "";
      return id ? `Cleared ${clipJobID(id)}` : "Cleared watch";
    }
    default:
      return summarizeCreate(raw, item);
  }
}

function NoteSection({ note }: { note: string }) {
  const [open, setOpen] = useState(false);
  const lineCount = note.split("\n").length;
  const clamped = !open && lineCount > NOTE_CLAMP_LINES;
  return (
    <div className={CLASS.section}>
      <div className={clamped ? `${CLASS.note} ${CLASS.noteClamped}` : CLASS.note} data-testid="job-watch-note">
        {note}
      </div>
      {clamped || open ? (
        <details data-testid="job-watch-note-disclosure" open={open}>
          {/* biome-ignore lint/a11y/noStaticElementInteractions: <summary> is natively keyboard-operable; controlled for the same single-source-of-truth reason as ToolRow */}
          <summary
            className={CLASS.disclosureSummary}
            onClick={(event) => {
              event.preventDefault();
              setOpen((previous) => !previous);
            }}
          >
            {open ? "Show less" : "Show full note"}
          </summary>
        </details>
      ) : null}
    </div>
  );
}

// conditionSentence renders the one-sentence body for a condition watch:
// source + condition + cadence in prose, patterns in mono. Raw field names
// (progress_interval_ms, output_match:) never surface — the cadence is a
// heartbeat phrase, the pattern is quoted. Clauses compose: a watch may arm
// a pattern AND event/filter triggers AND a heartbeat together (only timer
// fields exclude conditions), so every populated clause renders instead of
// returning after the first (combined RoboRev review).
// ConditionSentence owns the sentence's single terminal period (8a56741
// finding 1): callers with delivery/creation metadata pass it as meta
// ("3 deliveries, created Sep 6, 09:41") and it joins with an em dash before
// that one period — the caller never appends its own ("matches. — 3
// deliveries.").
function ConditionSentence({ source, spec, meta }: { source: string; spec: ConditionSpec; meta?: string }) {
  const tail = meta ? ` — ${meta}` : "";
  const heartbeat = heartbeatPhrase(spec);
  // Top-level clauses join with ", " exactly once, in the final render
  // below. Clause nodes carry NO separators of their own — neither leading
  // spaces nor commas — or the join doubles them ("outputs ready, ,
  // heartbeat …"). The filter's sub-clauses (tool + outcome + event +
  // every) are one clause: they join with spaces inside a single node
  // (combined RoboRev review).
  const head: ReactNode[] = [];
  // A budgeted trigger is what the 50-match auto-clear bounds: a pattern, an
  // event list, or an event filter. A heartbeat alone never consumes the
  // budget (periodic ticks count deliveries but never trip it), so a
  // heartbeat-only watch claims no auto-clear (combined RoboRev review).
  const budgeted =
    spec.outputMatch !== undefined ||
    spec.events.length > 0 ||
    spec.filterStatus !== undefined ||
    spec.filterToolName !== undefined;
  if (spec.outputMatch) {
    head.push(
      <span key="pattern">
        outputs <span className={CLASS.mono}>{spec.outputMatch}</span>
      </span>,
    );
  }
  if (spec.filterStatus || spec.filterToolName) {
    // Both filter statuses read explicitly (RoboRev PR #954: status "ok"
    // was discarded into a bare tool name). The tool rides along when
    // present; the event name disambiguates in list/inspect context.
    const parts: ReactNode[] = [];
    if (spec.filterToolName) {
      parts.push(
        <span key="filter-tool">
          makes a tool call on <span className={CLASS.mono}>{spec.filterToolName}</span>
        </span>,
      );
    } else {
      parts.push(<span key="filter-tool">makes a tool call</span>);
    }
    // Both non-status filters (tool-only, or a bare filter with neither
    // field) read "matching" — a single path, no dead ternary (RoboRev PR
    // #954 combined review).
    if (spec.filterStatus === "error" || spec.filterStatus === "ok") {
      parts.push(
        <span key="filter-outcome">
          ending in <span className={CLASS.mono}>{spec.filterStatus}</span>
        </span>,
      );
    } else {
      parts.push(<span key="filter-outcome">matching</span>);
    }
    // Name the filtered event: in inspect/list context the events array is
    // not shown separately, and the filter only ever attaches to
    // assistant.tool — without the name the sentence loses what fires.
    if (spec.events.length === 1) {
      parts.push(
        <span key="filter-event">
          (<span className={CLASS.mono}>{eventDisplayName(spec.events[0] ?? "")}</span>)
        </span>,
      );
    }
    // The every throttle rides the filter sentence too (RoboRev PR #954
    // review 3): a filter Condition carries it ("events: […] every N where
    // …"), and dropping it claims every event fires. Same "(every N)" shape
    // as the events branch below.
    if (spec.every !== undefined) parts.push(<span key="filter-every">(every {spec.every})</span>);
    head.push(<span key="filter">{joinNodes(parts, " ")}</span>);
  } else if (spec.events.length > 0) {
    const throttle = spec.every !== undefined ? ` (every ${spec.every})` : "";
    head.push(
      <span key="events">
        wakes on <span className={CLASS.mono}>{spec.events.map(eventDisplayName).join(", ")}</span>
        {throttle}
      </span>,
    );
  }
  if (heartbeat) head.push(<span key="heartbeat">{heartbeat}</span>);
  if (head.length === 0) {
    return (
      <span>
        Watches <span className={CLASS.mono}>{source}</span>
        {tail}.
      </span>
    );
  }
  return (
    <span>
      Wakes you when <span className={CLASS.mono}>{source}</span> {joinNodes(head, ", ")}
      {budgeted ? (
        <>
          , auto-clears after {WATCH_DELIVERY_BUDGET} matches{tail}.
        </>
      ) : (
        <>{tail}.</>
      )}
    </span>
  );
}

// joinNodes joins rendered clause nodes with a separator string, keyed so
// React needs no index keys.
function joinNodes(nodes: ReactNode[], separator: string): ReactNode[] {
  return nodes.flatMap((node, i) => (i === 0 ? [node] : [separator, node]));
}

function CreateBody({ raw, item }: { raw: JsonObject; item: ItemModel }) {
  if (isTerminalCatchup(raw)) return null;
  const timer = timerSpec(raw);
  if (timer?.note) return <NoteSection note={timer.note} />;
  if (timer) return null;
  const source = sourceLabel(strField(raw, "source"));
  const condition = conditionSpec(raw, asJsonObject(parseArgs(item.argumentsJSON)));
  if (!condition) return null;
  // A note-only watch arms no trigger: there is no sentence, but the note
  // still renders as its own clamped section (backend #995) rather than
  // vanishing behind the summary-only rule for triggerless creates.
  if (
    condition.outputMatch === undefined &&
    condition.progressIntervalMS === undefined &&
    condition.events.length === 0 &&
    condition.filterToolName === undefined &&
    condition.filterStatus === undefined
  ) {
    return condition.note ? <NoteSection note={condition.note} /> : null;
  }
  // Any watch carries a note (backend #995): the sentence names the trigger
  // and the note renders as its own clamped section below, reusing the
  // timer-note disclosure — never folded into the sentence.
  return (
    <>
      <div className={CLASS.section}>
        <div className={CLASS.trigger} data-testid="job-watch-trigger">
          <ConditionSentence source={source} spec={condition} />
        </div>
      </div>
      {condition.note ? <NoteSection note={condition.note} /> : null}
    </>
  );
}

function WatchRow({ row }: { row: WatchRow }) {
  const [open, setOpen] = useState(false);
  // Rows are real buttons (mockup §C: tappable rows opening the watch's
  // details) — but only when they CAN expand. Expanding shows the row's own
  // detail sentence inline — the same humanized grammar as the inspect body,
  // minus deliveries/created which list raw does not carry. A row with no
  // detail sentence (an ended/pending/missing row, or a watching row whose
  // condition parses to nothing) renders as a plain div: a focusable button
  // with a no-op onClick is a control that does nothing (RoboRev PR #954
  // combined review).
  const detail = row.watching ? rowDetailPhrase(row) : undefined;
  const state = watchState(row);
  const chip = state === "watching" ? "watching" : state === "missing" ? "not found" : state;
  // No per-row wrapper div: rows are direct children of the list container
  // so the container's :not(:first-child) separator applies (a wrapper
  // would make every row a first child and erase all separators).
  if (!detail) {
    return (
      <div className={CLASS.rowStatic} data-testid="job-watch-row">
        <Chip>{chip}</Chip>
        <span className={CLASS.rowId} title={row.id}>
          {clipJobID(row.id)}
        </span>
        <span className={CLASS.rowCondition}>{rowConditionPhrase(row)}</span>
      </div>
    );
  }
  return (
    <>
      <button
        type="button"
        className={CLASS.row}
        data-testid="job-watch-row"
        aria-expanded={open}
        onClick={() => {
          setOpen((previous) => !previous);
        }}
      >
        <Chip>{chip}</Chip>
        <span className={CLASS.rowId} title={row.id}>
          {clipJobID(row.id)}
        </span>
        <span className={CLASS.rowCondition}>{rowConditionPhrase(row)}</span>
      </button>
      {open ? (
        <div className={CLASS.section} data-testid="job-watch-row-detail">
          <div className={CLASS.trigger}>{detail}</div>
        </div>
      ) : null}
    </>
  );
}

// rowDetailPhrase renders the expanded sentence behind a tapped list row:
// the row's condition plus its deliveries context when the raw carries it.
// Every supported form renders — an expandable row must never be a no-op
// (RoboRev PR #954).
function rowDetailPhrase(row: WatchRow): string | undefined {
  if (!row.watching || !row.condition) return undefined;
  const parsed = parseConditionText(row.condition, row.note);
  // The structured note wins over the note: clause: it is verbatim even
  // when the note contains delimiter-looking text that truncates the
  // clause parse. The clause is the fallback for stored frames that
  // predate the structured field.
  const detailNote = row.note ?? parsed.note;
  const source = sourceLabel(row.source);
  const deliveries = row.deliveries !== undefined ? ` — ${row.deliveries} deliveries` : "";
  if (parsed.outputMatch) {
    const heartbeat =
      parsed.progressIntervalMS !== undefined
        ? `, heartbeat ${humanizeInterval(parsed.progressIntervalMS / 1000)}`
        : "";
    // Clauses compose here exactly as in the row phrase and the create
    // summary: a composite watch arms a pattern AND event/filter triggers
    // together, so the detail names every armed clause, never just the
    // pattern (combined RoboRev review). The embedded note rides along as
    // trailing prose — the row has no disclosure section to host it.
    const bits: string[] = [`“${parsed.outputMatch}”`];
    const every = parsed.every !== undefined ? ` (every ${parsed.every})` : "";
    if (parsed.events.length > 0) {
      const names = parsed.events.map(eventDisplayName).join(", ");
      bits.push(`${names}${every}`);
    }
    if (parsed.filterToolName || parsed.filterStatus) {
      bits.push(`${filterSummaryPhrase(parsed)}${parsed.events.length === 0 ? every : ""}`);
    }
    const note = detailNote ? ` Note: ${detailNote}.` : "";
    return `Watching ${source} for ${bits.join(" · ")}${heartbeat}${deliveries}.${note}`;
  }
  if (parsed.afterSeconds !== undefined || parsed.repeatSeconds !== undefined) {
    const seconds = parsed.afterSeconds ?? parsed.repeatSeconds ?? 0;
    const when = parsed.afterSeconds !== undefined ? humanizeSeconds(seconds) : humanizeInterval(seconds);
    // Timer Condition strings carry note: too (backend #995) — the detail
    // names it like every other branch, not just the cadence.
    const note = detailNote ? ` Note: ${detailNote}.` : "";
    // A one-shot timer reminds once ("Remind me in 5m", mirroring the create
    // summary); a repeating timer keeps reminding ("Reminds every 5m").
    const verb = parsed.afterSeconds !== undefined ? "Remind me" : "Reminds";
    return `${verb} ${when}${deliveries}.${note}`;
  }
  const bits: string[] = [];
  const every = parsed.every !== undefined ? ` (every ${parsed.every})` : "";
  if (parsed.events.length > 0) {
    const names = parsed.events.map(eventDisplayName).join(", ");
    bits.push(`${names}${every}`);
  }
  if (parsed.filterToolName || parsed.filterStatus) {
    // The same shared filter phrase as rows and summaries (RoboRev PR #954
    // review 3); the throttle rides here only when no events bit carries it.
    bits.push(`${filterSummaryPhrase(parsed)}${parsed.events.length === 0 ? every : ""}`);
  }
  if (parsed.progressIntervalMS !== undefined) {
    bits.push(`heartbeat ${humanizeInterval(parsed.progressIntervalMS / 1000)}`);
  }
  if (bits.length > 0) {
    const note = detailNote ? ` Note: ${detailNote}.` : "";
    return `Watches ${source}: ${bits.join(" · ")}${deliveries}.${note}`;
  }
  return undefined;
}

function ListBody({ raw }: { raw: JsonObject }) {
  const live = Array.isArray(raw.watches) ? raw.watches : [];
  const recent = Array.isArray(raw.recent_watches) ? raw.recent_watches : [];
  const rows: WatchRow[] = [];
  for (const entry of [...live, ...recent]) {
    const row = normalizeRow(entry);
    if (row) rows.push(row);
  }
  if (rows.length === 0) {
    return (
      <div className={CLASS.section}>
        <div className={CLASS.trigger} data-testid="job-watch-empty">
          No watches.
        </div>
      </div>
    );
  }
  return (
    <div className={CLASS.list}>
      {rows.map((row) => (
        <WatchRow key={row.id} row={row} />
      ))}
    </div>
  );
}

// formatCreatedDate renders the inspect created_at as an absolute clock
// ("created Sep 6, 09:41") — the one place the mockup keeps a clock: a
// duration since creation would go stale on every render, while the moment
// the watch was armed stays true.
function formatCreatedDate(createdAt: string | undefined): string | undefined {
  if (!createdAt) return undefined;
  const parsed = new Date(createdAt);
  if (Number.isNaN(parsed.getTime())) return undefined;
  const month = parsed.toLocaleString("en-US", { month: "short" });
  const day = parsed.getDate();
  const hours = String(parsed.getHours()).padStart(2, "0");
  const minutes = String(parsed.getMinutes()).padStart(2, "0");
  return `created ${month} ${day}, ${hours}:${minutes}`;
}

function InspectBody({ raw }: { raw: JsonObject }) {
  const state = watchState({
    watching: isWatching(raw),
    source: strField(raw, "source"),
    endReason: strField(raw, "end_reason"),
  });
  // A missing watch has no source to name — sourceLabel would invent "this
  // session" for a watch that is not there. Pending is a live detached watch
  // on the terminal-flush rail, not an ending (RoboRev PR #954 combined
  // review; grammar mirrors formatJobWatchInspect).
  if (state === "missing") {
    return (
      <div className={CLASS.section}>
        <div className={CLASS.trigger} data-testid="job-watch-trigger">
          <span>Watch not found.</span>
        </div>
      </div>
    );
  }
  if (state === "pending") {
    const source = sourceLabel(strField(raw, "source"));
    // The pending raw carries the same trigger/detail fields as any other
    // inspect (condition, note, deliveries, created_at —
    // jobWatchInspectToolResult, agent/session_tools_jobs.go:1578) — a bare
    // "is pending" drops everything the watch is pending FOR (8a56741 finding
    // 3). The watching branches below own the full trigger grammar; read them
    // through the same helpers so pending never drifts.
    const condition = strField(raw, "condition");
    const structuredNote = strField(raw, "note");
    const parsed = condition ? parseConditionText(condition, structuredNote) : undefined;
    const note = structuredNote ?? parsed?.note;
    const deliveries = typeof raw.deliveries === "number" ? raw.deliveries : undefined;
    const created = formatCreatedDate(strField(raw, "created_at"));
    const meta = [deliveries !== undefined ? `${deliveries} deliveries` : "", created ?? ""]
      .filter((bit) => bit !== "")
      .join(", ");
    const spec =
      parsed && (parsed.outputMatch || parsed.events.length > 0 || parsed.filterToolName || parsed.filterStatus)
        ? {
            outputMatch: parsed.outputMatch,
            events: parsed.events,
            every: parsed.every,
            progressIntervalMS: parsed.progressIntervalMS,
            filterToolName: parsed.filterToolName,
            filterStatus: parsed.filterStatus,
          }
        : undefined;
    const metaSuffix = meta ? ` — ${meta}` : "";
    if (spec) {
      // ConditionSentence owns the condition trigger's terminal period
      // (finding 1), so pendingTrigger keeps the pending sentence's own
      // period too: the sentence names the pending state first, then the
      // same trigger clauses the watching body would render ("Watch on X is
      // pending: wakes you when …").
      const pendingTrigger = (
        <span>
          Watch on <span className={CLASS.mono}>{source}</span> is pending:{" "}
          <ConditionSentence
            source={source}
            spec={{
              outputMatch: spec.outputMatch,
              events: spec.events,
              every: spec.every,
              progressIntervalMS: spec.progressIntervalMS,
              filterToolName: spec.filterToolName,
              filterStatus: spec.filterStatus,
            }}
            meta={meta === "" ? undefined : meta}
          />
        </span>
      );
      return (
        <>
          <div className={CLASS.section}>
            <div className={CLASS.trigger} data-testid="job-watch-trigger">
              {pendingTrigger}
            </div>
          </div>
          {note ? <NoteSection note={note} /> : null}
        </>
      );
    }
    const timerSeconds =
      parsed?.afterSeconds !== undefined || parsed?.repeatSeconds !== undefined
        ? (parsed.afterSeconds ?? parsed.repeatSeconds ?? 0)
        : undefined;
    if (timerSeconds !== undefined) {
      const when = parsed?.afterSeconds !== undefined ? humanizeSeconds(timerSeconds) : humanizeInterval(timerSeconds);
      const verb = parsed?.afterSeconds !== undefined ? "Remind me" : "Reminds";
      return (
        <>
          <div className={CLASS.section}>
            <div className={CLASS.trigger} data-testid="job-watch-trigger">
              <span>
                Watch on <span className={CLASS.mono}>{source}</span> is pending: {verb} {when}
                {metaSuffix}.
              </span>
            </div>
          </div>
          {note ? <NoteSection note={note} /> : null}
        </>
      );
    }
    const heartbeat =
      parsed?.progressIntervalMS !== undefined
        ? `heartbeat ${humanizeInterval(parsed.progressIntervalMS / 1000)}`
        : undefined;
    if (heartbeat) {
      return (
        <>
          <div className={CLASS.section}>
            <div className={CLASS.trigger} data-testid="job-watch-trigger">
              <span>
                Watch on <span className={CLASS.mono}>{source}</span> is pending: {heartbeat}
                {metaSuffix}.
              </span>
            </div>
          </div>
          {note ? <NoteSection note={note} /> : null}
        </>
      );
    }
    return (
      <>
        <div className={CLASS.section}>
          <div className={CLASS.trigger} data-testid="job-watch-trigger">
            <span>
              Watch on <span className={CLASS.mono}>{source}</span> is pending{metaSuffix}.
            </span>
          </div>
        </div>
        {note ? <NoteSection note={note} /> : null}
      </>
    );
  }
  const source = sourceLabel(strField(raw, "source"));
  if (state === "ended") {
    const endReason = strField(raw, "end_reason");
    // The ended raw carries the same trigger/detail fields as any other
    // inspect (condition, note, deliveries — inspectResultFromWatchHistory,
    // agent/job_watch.go:2363; the structured note rides beside the Condition
    // string verbatim — jobWatchInspectToolResult, agent/session_tools_jobs.go).
    // The end sentence names the outcome first, then the same trigger clauses
    // the watching body renders, so an ended watch still says what it was
    // watching for — mirroring the pending branch above — with the note as
    // its own section below.
    const condition = strField(raw, "condition");
    const structuredNote = strField(raw, "note");
    const parsed = condition ? parseConditionText(condition, structuredNote) : undefined;
    const note = structuredNote ?? parsed?.note;
    const deliveries = typeof raw.deliveries === "number" ? raw.deliveries : undefined;
    const meta = deliveries !== undefined ? `${deliveries} deliveries` : undefined;
    const endSentence = endReason ? (
      <span>
        Watch on <span className={CLASS.mono}>{source}</span> ended — {endReasonPhrase(endReason)}.
      </span>
    ) : (
      <span>
        Watch on <span className={CLASS.mono}>{source}</span> ended.
      </span>
    );
    const spec =
      parsed && (parsed.outputMatch || parsed.events.length > 0 || parsed.filterToolName || parsed.filterStatus)
        ? {
            outputMatch: parsed.outputMatch,
            events: parsed.events,
            every: parsed.every,
            progressIntervalMS: parsed.progressIntervalMS,
            filterToolName: parsed.filterToolName,
            filterStatus: parsed.filterStatus,
          }
        : undefined;
    if (spec) {
      return (
        <>
          <div className={CLASS.section}>
            <div className={CLASS.trigger} data-testid="job-watch-trigger">
              {endSentence} <ConditionSentence source={source} spec={spec} meta={meta} />
            </div>
          </div>
          {note ? <NoteSection note={note} /> : null}
        </>
      );
    }
    const timerSeconds =
      parsed?.afterSeconds !== undefined || parsed?.repeatSeconds !== undefined
        ? (parsed.afterSeconds ?? parsed.repeatSeconds ?? 0)
        : undefined;
    if (timerSeconds !== undefined) {
      const when = parsed?.afterSeconds !== undefined ? humanizeSeconds(timerSeconds) : humanizeInterval(timerSeconds);
      const verb = parsed?.afterSeconds !== undefined ? "Remind me" : "Reminds";
      const metaSuffix = meta ? ` — ${meta}` : "";
      return (
        <>
          <div className={CLASS.section}>
            <div className={CLASS.trigger} data-testid="job-watch-trigger">
              <span>
                {endSentence} {verb} {when}
                {metaSuffix}.
              </span>
            </div>
          </div>
          {note ? <NoteSection note={note} /> : null}
        </>
      );
    }
    const heartbeat =
      parsed?.progressIntervalMS !== undefined
        ? `heartbeat ${humanizeInterval(parsed.progressIntervalMS / 1000)}`
        : undefined;
    if (heartbeat) {
      return (
        <>
          <div className={CLASS.section}>
            <div className={CLASS.trigger} data-testid="job-watch-trigger">
              <span>
                {endSentence} {heartbeat}
                {meta ? ` — ${meta}` : ""}.
              </span>
            </div>
          </div>
          {note ? <NoteSection note={note} /> : null}
        </>
      );
    }
    if (note || meta) {
      return (
        <>
          <div className={CLASS.section}>
            <div className={CLASS.trigger} data-testid="job-watch-trigger">
              <span>
                {endSentence}
                {meta ? ` ${meta}.` : ""}
              </span>
            </div>
          </div>
          {note ? <NoteSection note={note} /> : null}
        </>
      );
    }
    return (
      <div className={CLASS.section}>
        <div className={CLASS.trigger} data-testid="job-watch-trigger">
          {endSentence}
        </div>
      </div>
    );
  }
  const condition = strField(raw, "condition");
  const structuredNote = strField(raw, "note");
  const parsed = condition ? parseConditionText(condition, structuredNote) : undefined;
  // The structured note wins over the note: clause for the same reason as
  // list rows: it is verbatim even when the note contains delimiter-looking
  // text. Its exact embedded clause is stripped before parsing (see
  // stripStructuredNoteClause) so delimiter-looking prose never invents
  // triggers. The clause is the fallback for stored frames that predate it.
  const note = structuredNote ?? parsed?.note;
  const deliveries = typeof raw.deliveries === "number" ? raw.deliveries : undefined;
  const created = formatCreatedDate(strField(raw, "created_at"));
  // ConditionSentence owns the sentence's single terminal period (8a56741
  // finding 1): metadata arrives as its `meta` clause ("3 deliveries, created
  // Sep 6, 09:41"), joined with an em dash before the one period — never
  // appended beside it ("matches. — 3 deliveries.").
  const meta =
    [deliveries !== undefined ? `${deliveries} deliveries` : "", created ?? ""]
      .filter((bit) => bit !== "")
      .join(", ") || undefined;
  // Every embedded condition form renders humanized: pattern, timer
  // cadence, heartbeat, events (+every throttle), and filter — the same
  // sentence grammar as the create/inspect one-liners, never raw keys. The
  // output-match branch renders through the shared ConditionSentence so a
  // composite watch names every armed clause, not just the pattern
  // (combined RoboRev review).
  if (parsed?.outputMatch) {
    return (
      <>
        <div className={CLASS.section}>
          <div className={CLASS.trigger} data-testid="job-watch-trigger">
            <ConditionSentence
              source={source}
              spec={{
                outputMatch: parsed.outputMatch,
                events: parsed.events,
                every: parsed.every,
                progressIntervalMS: parsed.progressIntervalMS,
                filterToolName: parsed.filterToolName,
                filterStatus: parsed.filterStatus,
              }}
              meta={meta}
            />
          </div>
        </div>
        {note ? <NoteSection note={note} /> : null}
      </>
    );
  }
  if (parsed?.afterSeconds !== undefined || parsed?.repeatSeconds !== undefined) {
    const seconds = parsed.afterSeconds ?? parsed.repeatSeconds ?? 0;
    const when = parsed.afterSeconds !== undefined ? humanizeSeconds(seconds) : humanizeInterval(seconds);
    // A one-shot timer reminds once ("Remind me in 5m", mirroring the create
    // summary); a repeating timer keeps reminding ("Reminds every 5m" —
    // 8a56741 finding 2).
    const verb = parsed.afterSeconds !== undefined ? "Remind me" : "Reminds";
    const metaSuffix = meta ? ` — ${meta}` : "";
    return (
      <>
        <div className={CLASS.section}>
          <div className={CLASS.trigger} data-testid="job-watch-trigger">
            <span>
              {verb} {when}
              {metaSuffix}.
            </span>
          </div>
        </div>
        {note ? <NoteSection note={note} /> : null}
      </>
    );
  }
  if (
    parsed &&
    (parsed.events.length > 0 ||
      parsed.filterToolName ||
      parsed.filterStatus ||
      parsed.progressIntervalMS !== undefined)
  ) {
    return (
      <>
        <div className={CLASS.section}>
          <div className={CLASS.trigger} data-testid="job-watch-trigger">
            <ConditionSentence
              source={source}
              spec={{
                events: parsed.events,
                every: parsed.every,
                progressIntervalMS: parsed.progressIntervalMS,
                filterToolName: parsed.filterToolName,
                filterStatus: parsed.filterStatus,
              }}
              meta={meta}
            />
          </div>
        </div>
        {note ? <NoteSection note={note} /> : null}
      </>
    );
  }
  return (
    <div className={CLASS.section}>
      <div className={CLASS.trigger} data-testid="job-watch-trigger">
        <span>
          Watching <span className={CLASS.mono}>{source}</span>
          {meta ? ` — ${meta}` : ""}.
        </span>
      </div>
    </div>
  );
}

function JobWatchBody(props: ToolRenderProps) {
  const { item, live } = props;
  const raw = asJsonObject(item.raw);
  // Without structured state (a stored transcript predating it, or a shape
  // the normalizer doesn't recognize) fall back to the raw footer text —
  // the call's only useful output (RoboRev PR #954). The structured bodies
  // above replace the mono wall only when there is structure to render.
  if (!raw || !isRecognizedWatchResult(raw)) return <HeadClippedOutputBody item={item} live={live} />;
  const operation = jobWatchOperation(item, raw);
  switch (operation) {
    case "list":
      return (
        <div className={CLASS.card} data-testid="job-watch-body">
          <ListBody raw={raw} />
        </div>
      );
    case "inspect":
      return (
        <div className={CLASS.card} data-testid="job-watch-body">
          <InspectBody raw={raw} />
        </div>
      );
    case "clear":
      return null;
    default: {
      if (isTerminalCatchup(raw)) return null;
      const timer = timerSpec(raw);
      if (timer && !timer.note) return null;
      // A recognized create with only a source has no sentence to render —
      // the summary ("Watch this session") IS the rendering. Returning null
      // here instead of an empty bordered card (CreateBody also returns null
      // for this shape, but the wrapper div would still draw the card chrome
      // around nothing — combined RoboRev review).
      if (!timer && !conditionSpec(raw, asJsonObject(parseArgs(item.argumentsJSON)))) return null;
      return (
        <div className={CLASS.card} data-testid="job-watch-body">
          <CreateBody raw={raw} item={item} />
        </div>
      );
    }
  }
}

// jobWatchHasBody mirrors JobWatchBody's null conditions exactly: clear
// results and terminal catch-ups are summary-only (the summary line IS the
// rendering), as are noteless timers and sourceless-condition creates.
// ToolCallItem consults this to withhold the disclosure control that would
// otherwise open to nothing.
function jobWatchHasBody(item: ItemModel): boolean {
  const raw = asJsonObject(item.raw);
  if (!raw || !isRecognizedWatchResult(raw)) return true;
  const operation = jobWatchOperation(item, raw);
  if (operation === "clear") return false;
  if (operation === "list" || operation === "inspect") return true;
  if (isTerminalCatchup(raw)) return false;
  const timer = timerSpec(raw);
  if (timer && !timer.note) return false;
  if (!timer && !conditionSpec(raw, asJsonObject(parseArgs(item.argumentsJSON)))) return false;
  return true;
}

registerToolRenderer({
  match: "job_watch",
  icon: "job",
  // A watch card is state a reader tracks across a turn (armed timer,
  // standing condition, inventory) — it always stands on its own line,
  // like every other job descriptor after #947.
  fold: "never",
  summary: jobWatchSummary,
  body: JobWatchBody,
  hasBody: jobWatchHasBody,
});
