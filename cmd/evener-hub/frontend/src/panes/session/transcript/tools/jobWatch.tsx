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
import { useState } from "react";
import type { ItemModel } from "../../../../protocol/model";
import { Chip } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { HeadClippedOutputBody } from "./bodies";
import { clip, clipJobID, parseArgs, str } from "./helpers";
import styles from "./jobWatch.module.css";

const CLASS = {
  card: requireClass(styles.card, "jobWatch.module.css", "card"),
  section: requireClass(styles.section, "jobWatch.module.css", "section"),
  note: requireClass(styles.note, "jobWatch.module.css", "note"),
  noteClamped: requireClass(styles.noteClamped, "jobWatch.module.css", "noteClamped"),
  trigger: requireClass(styles.trigger, "jobWatch.module.css", "trigger"),
  mono: requireClass(styles.mono, "jobWatch.module.css", "mono"),
  row: requireClass(styles.row, "jobWatch.module.css", "row"),
  rowId: requireClass(styles.rowId, "jobWatch.module.css", "rowId"),
  rowCondition: requireClass(styles.rowCondition, "jobWatch.module.css", "rowCondition"),
  disclosureSummary: requireClass(styles.disclosureSummary, "jobWatch.module.css", "disclosureSummary"),
};

// watchDeliveryBudget in agent/job_watch.go: the condition-fire budget every
// inspect summary measures deliveries against. Carried as a literal with the
// same "budget" framing the Go side's own notices use ("matched 50 times"),
// not derived — item.raw carries only the used count.
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
// asked in: sub-minute stays in seconds ("in 45s"); whole minutes collapse
// ("in 5m", "in 1m"); leftover seconds are kept ("in 1m30s", never a lossy
// "in 1m" — RoboRev PR #954); an hour or more names hours and leftover
// minutes ("in 1h05m"). Zero/negative never reaches here (numField filters
// it) — the caller falls back to the raw footer text instead of inventing
// one.
export function humanizeSeconds(totalSeconds: number): string {
  if (totalSeconds < 60) return `in ${Math.round(totalSeconds)}s`;
  const totalMinutes = Math.floor(totalSeconds / 60);
  const leftoverSeconds = Math.round(totalSeconds % 60);
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
// seconds ("every 45s"), whole minutes collapse ("every 2m"), leftover
// seconds are kept ("every 1m30s"); hours name hours ("every 1h"). Same
// zero/negative contract as humanizeSeconds.
export function humanizeInterval(totalSeconds: number): string {
  if (totalSeconds < 60) return `every ${Math.round(totalSeconds)}s`;
  const totalMinutes = Math.floor(totalSeconds / 60);
  const leftoverSeconds = Math.round(totalSeconds % 60);
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
}

function numArg(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? Math.floor(value) : undefined;
}

function conditionSpec(raw: JsonObject, args?: JsonObject): ConditionSpec | undefined {
  const outputMatch = strField(raw, "output_match");
  const progressIntervalMS = numField(raw, "progress_interval_ms");
  const events = strArrayField(raw, "events");
  // `every` rides the create ARGS (DefJobWatch), not the result state —
  // read args first, falling back to the raw in case a producer echoes it.
  const every = (args ? numArg(args.every) : undefined) ?? numField(raw, "every");
  const filter = asJsonObject(raw.event_filter);
  const filterToolName = filter ? strField(filter, "tool_name") : undefined;
  const filterStatus = filter ? strField(filter, "status") : undefined;
  if (outputMatch === undefined && progressIntervalMS === undefined && events.length === 0 && !filter) {
    return undefined;
  }
  return { outputMatch, progressIntervalMS, events, every, filterToolName, filterStatus };
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
  return source ?? "this session";
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
  if (raw.watching === false && typeof raw.watch_id === "string") return "clear";
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
  if (condition.outputMatch) {
    const suffix = cadenceSuffix(condition);
    return suffix
      ? `Watch ${source} for “${condition.outputMatch}” ${suffix}`
      : `Watch ${source} for “${condition.outputMatch}”`;
  }
  if (condition.filterStatus || condition.filterToolName) {
    // An event-filter watch names the watched shape in words — both
    // statuses explicitly (RoboRev PR #954: status "ok" was discarded).
    // Never the raw filter keys.
    const what = filterSummaryPhrase(condition);
    return `Watch ${source} for ${what}`;
  }
  if (condition.events.length > 0) {
    const suffix = cadenceSuffix(condition);
    const throttle = condition.every !== undefined ? ` (every ${condition.every})` : "";
    const named = `${condition.events.join(", ")}${throttle}`;
    return suffix ? `Watch ${source} for ${named} ${suffix}` : `Watch ${source} for ${named}`;
  }
  return `Watch ${source}`;
}

// filterSummaryPhrase names an event-filter watch's shape in words for
// summaries: error and ok both explicit, tool named when present.
function filterSummaryPhrase(condition: ConditionSpec): string {
  const tool = condition.filterToolName ? ` on ${condition.filterToolName}` : "";
  if (condition.filterStatus === "error") return `failed tool calls${tool}`;
  if (condition.filterStatus === "ok") return `successful tool calls${tool}`;
  return condition.filterToolName ?? "matching events";
}

interface WatchRow {
  id: string;
  watching: boolean;
  source?: string;
  condition?: string;
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
// nothing, since inspect's raw carries no separate structured fields.
interface ParsedCondition {
  outputMatch?: string;
  afterSeconds?: number;
  repeatSeconds?: number;
  progressIntervalMS?: number;
  events: string[];
  every?: number;
  filterToolName?: string;
  filterStatus?: string;
}

function numAfter(value: string | undefined): number | undefined {
  if (value === undefined) return undefined;
  const n = Number(value);
  return Number.isFinite(n) && n > 0 ? n : undefined;
}

function parseConditionText(condition: string): ParsedCondition {
  const parsed: ParsedCondition = { events: [] };
  for (const part of condition.split(";")) {
    const text = part.trim();
    const outputMatch = /^output_match:\s*(.+)$/.exec(text)?.[1]?.trim();
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
    // `note: …` and anything unrecognized stay out: the note is the watch's
    // own prose (shown by the create body, never by a row), and unknown
    // future parts degrade to the fallback below rather than inventing
    // rendering.
  }
  return parsed;
}

// conditionSentence renders one humanized trigger sentence from a parsed
// Condition: pattern, timer cadence, heartbeat, events, and filter in
// prose, machine tokens in mono. Shared by list rows (short form) and
// inspect bodies (full form) so the two never drift.
function rowConditionPhrase(row: WatchRow): string {
  if (!row.watching) {
    return row.endReason ? `ended: ${row.endReason}` : "ended";
  }
  if (row.condition) {
    const parsed = parseConditionText(row.condition);
    const source = sourceLabel(row.source);
    if (parsed.afterSeconds !== undefined) return `${humanizeSeconds(parsed.afterSeconds)} · ${source}`;
    if (parsed.repeatSeconds !== undefined) {
      return `every ${humanizeInterval(parsed.repeatSeconds).replace(/^every /, "")} · ${source}`;
    }
    const bits: string[] = [];
    if (parsed.outputMatch) bits.push(`“${parsed.outputMatch}”`);
    if (parsed.events.length > 0) {
      const names = parsed.events.includes("*") ? "any event" : parsed.events.join(", ");
      bits.push(parsed.every !== undefined ? `${names} every ${parsed.every}` : names);
    }
    if (parsed.filterToolName || parsed.filterStatus) {
      bits.push(parsed.filterStatus === "error" ? "failed tool calls" : `calls on ${parsed.filterToolName ?? "?"}`);
    }
    if (parsed.progressIntervalMS !== undefined) {
      bits.push(humanizeInterval(parsed.progressIntervalMS / 1000));
    }
    if (bits.length > 0) return `${bits.join(" · ")} · ${source}`;
    return `${row.condition} · ${source}`;
  }
  return sourceLabel(row.source);
}

function listCounts(raw: JsonObject): { active: number; ended: number } {
  const live = Array.isArray(raw.watches) ? raw.watches : [];
  const recent = Array.isArray(raw.recent_watches) ? raw.recent_watches : [];
  let active = 0;
  let ended = 0;
  for (const entry of live) {
    const row = normalizeRow(entry);
    if (!row) continue;
    if (row.watching) active += 1;
    else ended += 1;
  }
  ended += recent.filter((entry) => normalizeRow(entry) !== undefined).length;
  return { active, ended };
}

function summarizeList(raw: JsonObject): string {
  const { active, ended } = listCounts(raw);
  const activeWord = active === 1 ? "1 active" : `${active} active`;
  if (ended === 0) return `Listed watches (${activeWord})`;
  const endedWord = ended === 1 ? "1 ended" : `${ended} ended`;
  return `Listed watches (${activeWord} · ${endedWord})`;
}

function summarizeInspect(item: ItemModel, raw: JsonObject): string {
  const args = parseArgs(item.argumentsJSON);
  const id = strField(raw, "watch_id") ?? str(args, "watch_id") ?? "";
  const state = isWatching(raw) ? "watching" : "ended";
  const deliveries = typeof raw.deliveries === "number" ? raw.deliveries : undefined;
  // Deliveries used measures against the delivery budget ("3 of 50 used")
  // — the same budget the Go side's own notices name. Ended or
  // not-yet-delivered watches carry no count to render.
  if (deliveries !== undefined && isWatching(raw)) {
    return `Inspected ${id} · ${state} · ${deliveries} of ${WATCH_DELIVERY_BUDGET} used`;
  }
  return `Inspected ${id} · ${state}`;
}

function jobWatchSummary(item: ItemModel): string {
  const raw = asJsonObject(item.raw);
  // Without structured state (a stored transcript predating it, or a shape
  // the normalizer doesn't recognize) fall back to the call's own verb —
  // the same "job_watch: <operation>" the family fallback rendered, so the
  // row never regresses to a bare tool name.
  if (!raw) {
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
// heartbeat phrase, the pattern is quoted.
function ConditionSentence({ source, spec }: { source: string; spec: ConditionSpec }) {
  const heartbeat = heartbeatPhrase(spec);
  if (spec.outputMatch) {
    return (
      <span>
        Wakes you when <span className={CLASS.mono}>{source}</span> outputs{" "}
        <span className={CLASS.mono}>{spec.outputMatch}</span>
        {heartbeat ? `, ${heartbeat}` : ""}, auto-clears after {WATCH_DELIVERY_BUDGET} matches.
      </span>
    );
  }
  if (spec.filterStatus || spec.filterToolName) {
    // Both filter statuses read explicitly (RoboRev PR #954: status "ok"
    // was discarded into a bare tool name). The tool rides along when
    // present; the event name disambiguates in list/inspect context.
    const tool = spec.filterToolName ? (
      <>
        {" "}
        on <span className={CLASS.mono}>{spec.filterToolName}</span>
      </>
    ) : null;
    const outcome =
      spec.filterStatus === "error" ? (
        <span>
          ending in <span className={CLASS.mono}>error</span>
        </span>
      ) : spec.filterStatus === "ok" ? (
        <span>
          ending in <span className={CLASS.mono}>ok</span>
        </span>
      ) : spec.filterToolName ? (
        "matching"
      ) : (
        "matching"
      );
    // Name the filtered event: in inspect/list context the events array is
    // not shown separately, and the filter only ever attaches to
    // assistant.tool — without the name the sentence loses what fires.
    const eventName =
      spec.events.length === 1 ? (
        <>
          {" "}
          (<span className={CLASS.mono}>{spec.events[0]}</span>)
        </>
      ) : null;
    return (
      <span>
        Wakes you when <span className={CLASS.mono}>{source}</span> makes a tool call {outcome}
        {tool}
        {eventName}.
      </span>
    );
  }
  if (spec.events.length > 0) {
    const throttle = spec.every !== undefined ? ` (every ${spec.every})` : "";
    return (
      <span>
        Wakes you on <span className={CLASS.mono}>{spec.events.join(", ")}</span>
        {throttle}
        {heartbeat ? `, ${heartbeat}` : ""}.
      </span>
    );
  }
  if (heartbeat) {
    return (
      <span>
        Watches <span className={CLASS.mono}>{source}</span>, {heartbeat}.
      </span>
    );
  }
  return (
    <span>
      Watches <span className={CLASS.mono}>{source}</span>.
    </span>
  );
}

function CreateBody({ raw, item }: { raw: JsonObject; item: ItemModel }) {
  if (isTerminalCatchup(raw)) return null;
  const timer = timerSpec(raw);
  if (timer?.note) return <NoteSection note={timer.note} />;
  if (timer) return null;
  const source = sourceLabel(strField(raw, "source"));
  const condition = conditionSpec(raw, asJsonObject(parseArgs(item.argumentsJSON)));
  if (!condition) return null;
  return (
    <div className={CLASS.section}>
      <div className={CLASS.trigger} data-testid="job-watch-trigger">
        <ConditionSentence source={source} spec={condition} />
      </div>
    </div>
  );
}

function WatchRow({ row }: { row: WatchRow }) {
  const [open, setOpen] = useState(false);
  // Rows are real buttons (mockup §C: tappable rows opening the watch's
  // details). Expanding shows the row's own detail sentence inline — the
  // same humanized grammar as the inspect body, minus deliveries/created
  // which list raw does not carry.
  const detail = row.watching ? rowDetailPhrase(row) : undefined;
  return (
    <div key={row.id}>
      <button
        type="button"
        className={CLASS.row}
        data-testid="job-watch-row"
        aria-expanded={detail ? open : undefined}
        onClick={() => {
          if (detail) setOpen((previous) => !previous);
        }}
      >
        <Chip>{row.watching ? "watching" : "ended"}</Chip>
        <span className={CLASS.rowId} title={row.id}>
          {clipJobID(row.id)}
        </span>
        <span className={CLASS.rowCondition}>{rowConditionPhrase(row)}</span>
      </button>
      {detail && open ? (
        <div className={CLASS.section} data-testid="job-watch-row-detail">
          <div className={CLASS.trigger}>{detail}</div>
        </div>
      ) : null}
    </div>
  );
}

// rowDetailPhrase renders the expanded sentence behind a tapped list row:
// the row's condition plus its deliveries context when the raw carries it.
// Every supported form renders — an expandable row must never be a no-op
// (RoboRev PR #954).
function rowDetailPhrase(row: WatchRow): string | undefined {
  if (!row.watching || !row.condition) return undefined;
  const parsed = parseConditionText(row.condition);
  const source = sourceLabel(row.source);
  const deliveries =
    row.deliveries !== undefined ? ` — ${row.deliveries} of ${WATCH_DELIVERY_BUDGET} deliveries used` : "";
  if (parsed.outputMatch) {
    const heartbeat =
      parsed.progressIntervalMS !== undefined
        ? `, heartbeat ${humanizeInterval(parsed.progressIntervalMS / 1000)}`
        : "";
    return `Watching ${source} for “${parsed.outputMatch}”${heartbeat}${deliveries}.`;
  }
  if (parsed.afterSeconds !== undefined) return `Reminds ${humanizeSeconds(parsed.afterSeconds)}${deliveries}.`;
  if (parsed.repeatSeconds !== undefined) return `Reminds ${humanizeInterval(parsed.repeatSeconds)}${deliveries}.`;
  const bits: string[] = [];
  if (parsed.events.length > 0) {
    const names = parsed.events.includes("*") ? "any event" : parsed.events.join(", ");
    bits.push(parsed.every !== undefined ? `${names} every ${parsed.every}` : names);
  }
  if (parsed.filterToolName || parsed.filterStatus) {
    if (parsed.filterStatus === "error")
      bits.push(`failed tool calls${parsed.filterToolName ? ` on ${parsed.filterToolName}` : ""}`);
    else if (parsed.filterStatus === "ok")
      bits.push(`successful tool calls${parsed.filterToolName ? ` on ${parsed.filterToolName}` : ""}`);
    else bits.push(`calls on ${parsed.filterToolName ?? "?"}`);
  }
  if (parsed.progressIntervalMS !== undefined) {
    bits.push(`heartbeat ${humanizeInterval(parsed.progressIntervalMS / 1000)}`);
  }
  if (bits.length > 0) return `Watches ${source}: ${bits.join(" · ")}${deliveries}.`;
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
    <div>
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
  const source = sourceLabel(strField(raw, "source"));
  if (!isWatching(raw)) {
    const endReason = strField(raw, "end_reason");
    return (
      <div className={CLASS.section}>
        <div className={CLASS.trigger} data-testid="job-watch-trigger">
          {endReason ? (
            <span>
              Watch on <span className={CLASS.mono}>{source}</span> ended — {endReason}.
            </span>
          ) : (
            <span>
              Watch on <span className={CLASS.mono}>{source}</span> ended.
            </span>
          )}
        </div>
      </div>
    );
  }
  const condition = strField(raw, "condition");
  const parsed = condition ? parseConditionText(condition) : undefined;
  const deliveries = typeof raw.deliveries === "number" ? raw.deliveries : undefined;
  const created = formatCreatedDate(strField(raw, "created_at"));
  const used = deliveries !== undefined ? ` — ${deliveries} of ${WATCH_DELIVERY_BUDGET} deliveries used` : "";
  const since = created ? `, ${created}` : "";
  // Every embedded condition form renders humanized: pattern, timer
  // cadence, heartbeat, events (+every throttle), and filter — the same
  // sentence grammar as the create/inspect one-liners, never raw keys.
  if (parsed?.outputMatch) {
    return (
      <div className={CLASS.section}>
        <div className={CLASS.trigger} data-testid="job-watch-trigger">
          <span>
            Watching <span className={CLASS.mono}>{source}</span> for{" "}
            <span className={CLASS.mono}>{parsed.outputMatch}</span>
            {parsed.progressIntervalMS !== undefined
              ? `, heartbeat ${humanizeInterval(parsed.progressIntervalMS / 1000)}`
              : ""}
            {used}
            {since}.
          </span>
        </div>
      </div>
    );
  }
  if (parsed?.afterSeconds !== undefined || parsed?.repeatSeconds !== undefined) {
    const seconds = parsed.afterSeconds ?? parsed.repeatSeconds ?? 0;
    const when = parsed.afterSeconds !== undefined ? humanizeSeconds(seconds) : humanizeInterval(seconds);
    return (
      <div className={CLASS.section}>
        <div className={CLASS.trigger} data-testid="job-watch-trigger">
          <span>
            Reminds {when}
            {used}
            {since}.
          </span>
        </div>
      </div>
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
          />
          <span>
            {used}
            {since}.
          </span>
        </div>
      </div>
    );
  }
  return (
    <div className={CLASS.section}>
      <div className={CLASS.trigger} data-testid="job-watch-trigger">
        <span>
          Watching <span className={CLASS.mono}>{source}</span>
          {used}
          {since}.
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
  if (!raw) return <HeadClippedOutputBody item={item} live={live} />;
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
      return (
        <div className={CLASS.card} data-testid="job-watch-body">
          <CreateBody raw={raw} item={item} />
        </div>
      );
    }
  }
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
});
