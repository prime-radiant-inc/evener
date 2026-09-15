import type { ItemModel } from "./model";
import { parseArgs } from "./toolCallText";

export type JsonObject = Record<string, unknown>;

export function asJsonObject(value: unknown): JsonObject | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as JsonObject) : undefined;
}

export function strField(object: JsonObject, key: string): string | undefined {
  const value = object[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

export function numField(object: JsonObject, key: string): number | undefined {
  const value = object[key];
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : undefined;
}

export function boolField(object: JsonObject, key: string): boolean {
  return object[key] === true;
}

export function strArrayField(object: JsonObject, key: string): string[] {
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
// A condition watch's trigger: the output pattern, the heartbeat cadence
// (progress_interval_ms from the wire), the event list plus its every-Nth
// throttle (RoboRev PR #954: `every: 3` fires on every third event, and
// dropping it claims every event fires), and the event-filter shape
// (assistant.tool errors on a delegate). Empty when the result names no
// condition at all — a bare source watch the summary still names.
export interface ConditionSpec {
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

export function conditionSpec(raw: JsonObject, args?: JsonObject): ConditionSpec | undefined {
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

// createConditionText mirrors watchConditionSummary (agent/job_watch.go): it
// converges a create-shaped result on the same "; "-joined Condition string
// that list/inspect rows carry. Keep its clause order and event-filter grammar
// pinned to that producer function; parseConditionText below is the one reader
// for both result shapes.
function createConditionText(raw: JsonObject, args?: JsonObject): string | undefined {
  const spec = conditionSpec(raw, args);
  const afterSeconds = numField(raw, "after_seconds");
  const repeatSeconds = numField(raw, "repeat_seconds");
  const parts: string[] = [];
  if (spec?.outputMatch) {
    parts.push(`output_match: ${limitWatchText(spec.outputMatch, WATCH_TRIGGER_MAX_CHARS)}`);
  }
  if (afterSeconds !== undefined) parts.push(`after_seconds: ${afterSeconds}`);
  else if (repeatSeconds !== undefined) parts.push(`repeat_seconds: ${repeatSeconds}`);
  else if (spec?.progressIntervalMS !== undefined) {
    parts.push(`progress_interval_ms: ${spec.progressIntervalMS}`);
  }
  if (spec?.note) parts.push(`note: ${spec.note}`);
  if (spec?.events.includes("*")) {
    parts.push("events: [*]");
  } else if (spec && spec.events.length > 0) {
    let events = `events: [${spec.events.join(", ")}]`;
    if (spec.every !== undefined) events += ` every ${spec.every}`;
    const filter: string[] = [];
    if (spec.filterToolName) filter.push(`tool_name=${spec.filterToolName}`);
    if (spec.filterStatus) filter.push(`status=${spec.filterStatus}`);
    if (filter.length > 0) events += ` where ${filter.join(", ")}`;
    parts.push(events);
  }
  return parts.length > 0 ? parts.join("; ") : undefined;
}

export function sourceLabel(source: string | undefined): string {
  // The producer's self source is internal vocabulary (watchPublicSource):
  // readers see "this session" (mockups 23-job-watch), never a bare "self".
  return source === undefined || source === "self" ? "this session" : source;
}
export interface WatchRow {
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

export function normalizeRow(value: unknown): WatchRow | undefined {
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

// These bounds and the indicator mirror the producer (agent/job_watch.go).
// The Condition string embeds output_match with watchTriggerMaxChars and note
// with watchMessageMaxChars. limitWatchText truncates by rune, includes the
// indicator inside the bound, and otherwise preserves the value verbatim.
const WATCH_TRIGGER_MAX_CHARS = 1024;
const WATCH_MESSAGE_MAX_CHARS = 2048;
const WATCH_TRUNCATED_INDICATOR = "\n[truncated]";

function limitWatchText(text: string, maxChars: number): string {
  const runes = Array.from(text);
  if (maxChars <= 0 || runes.length <= maxChars) return text;
  const indicator = Array.from(WATCH_TRUNCATED_INDICATOR);
  const keep = maxChars - indicator.length;
  if (keep <= 0) return runes.slice(0, maxChars).join("");
  return runes.slice(0, keep).join("") + WATCH_TRUNCATED_INDICATOR;
}

// embeddedNoteCandidates lists the exact strings the producer may have
// embedded as the note: clause for a structured note value: the value itself
// (cfg.note is already bounded at storage, so this is the live case), plus
// the limitWatchText truncation for oversized values from stored frames.
function embeddedNoteCandidates(note: string): string[] {
  const truncated = limitWatchText(note, WATCH_MESSAGE_MAX_CHARS);
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

export function parseConditionText(condition: string, note?: string): ParsedCondition {
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

export type WatchDisplayState = "watching" | "pending" | "missing" | "ended" | "cleared" | "terminal-catch-up";

export interface WatchSummary {
  id: string;
  state: WatchDisplayState;
  source?: string;
  condition?: string;
  deliveries?: number;
  note?: string;
  endReason?: string;
}

interface WatchStateMarkers {
  watchingPresent?: boolean;
  clear?: boolean;
  terminalCatchup?: boolean;
}

export function watchDisplayState(
  entry: { watching: boolean; source?: string; endReason?: string },
  markers: WatchStateMarkers = {},
): WatchDisplayState {
  if (markers.terminalCatchup) return "terminal-catch-up";
  if (entry.endReason) return "ended";
  if (entry.watching) return "watching";
  if (markers.clear) return "cleared";
  if (markers.watchingPresent === false) return "pending";
  return watchState(entry);
}

interface SummaryRow extends WatchRow {
  state: WatchDisplayState;
  statePresent: boolean;
}

function normalizeSummaryRow(value: unknown, args?: JsonObject): SummaryRow | undefined {
  const raw = asJsonObject(value);
  const row = normalizeRow(value);
  if (!raw || !row) return undefined;
  const terminalCatchup = boolField(raw, "terminal_catchup");
  const clear = raw.watching === false && ("replaced_existing" in raw || "fired" in raw);
  const watchingPresent = typeof raw.watching === "boolean";
  return {
    ...row,
    condition: row.condition ?? createConditionText(raw, args),
    state: watchDisplayState(row, { watchingPresent, clear, terminalCatchup }),
    statePresent: terminalCatchup || row.endReason !== undefined || watchingPresent,
  };
}

function presentFields(row: WatchRow): Partial<WatchSummary> {
  const fields: Partial<WatchSummary> = {};
  if (row.source !== undefined) fields.source = row.source;
  if (row.condition !== undefined) fields.condition = row.condition;
  if (row.deliveries !== undefined) fields.deliveries = row.deliveries;
  if (row.note !== undefined) fields.note = row.note;
  if (row.endReason !== undefined) fields.endReason = row.endReason;
  return fields;
}

function summaryOf(row: SummaryRow): WatchSummary {
  return { id: row.id, state: row.state, ...presentFields(row) };
}

function mergePresent(_prior: WatchSummary, row: SummaryRow): Partial<WatchSummary> {
  return {
    ...(row.statePresent ? { state: row.state } : {}),
    ...presentFields(row),
  };
}

// Orders watch snapshots by where they sit in the transcript, with no opinion
// about items that carry no position: those keep their caller order, so both
// the fold and the memo key stay stable for well-ordered input.
export function compareTranscriptPosition(left: ItemModel, right: ItemModel): number {
  const leftPosition = left.position;
  const rightPosition = right.position;
  if (!leftPosition || !rightPosition) return 0;
  return leftPosition.entry - rightPosition.entry || leftPosition.item - rightPosition.item;
}

export function foldWatchSummaries(items: ItemModel[]): Map<string, WatchSummary> {
  const byId = new Map<string, WatchSummary>();
  for (const item of [...items].sort(compareTranscriptPosition)) {
    const raw = asJsonObject(item.raw);
    if (!raw || item.toolName !== "job_watch") continue;
    const args = asJsonObject(parseArgs(item.argumentsJSON));
    const entries =
      Array.isArray(raw.watches) || Array.isArray(raw.recent_watches)
        ? [
            ...(Array.isArray(raw.watches) ? raw.watches : []),
            ...(Array.isArray(raw.recent_watches) ? raw.recent_watches : []),
          ]
        : [raw];
    for (const entry of entries) {
      const row = normalizeSummaryRow(entry, args);
      if (!row) continue;
      const prior = byId.get(row.id);
      byId.set(row.id, prior ? { ...prior, ...mergePresent(prior, row) } : summaryOf(row));
    }
  }
  return byId;
}
