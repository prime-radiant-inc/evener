// The Session's transcript rows (spec 8.2):
// - consecutive steps fold into one run line ("12 steps · 8m · read 6 files,
//   ran go test (2 failed), edited 3 files"), and a step's images ride with it;
// - the step in progress, and a live thought, are left to the status tray
//   (ruling 10);
// - a time marker introduces the first turn, a turn that starts after ten
//   quiet minutes, and a new day.
import { parseArgs, str, type TurnModel } from "@evener/appwire-client";
import { type RunStep, rowTurnId, type TimelineRow } from "../timeline";
import { compactDuration } from "./format";

type RunRow = Extract<TimelineRow, { kind: "run" }>;
type TimeRow = Extract<TimelineRow, { kind: "time" }>;
type TurnTimes = Pick<TurnModel, "id" | "startedAt" | "completedAt">;

export const TIME_GAP_MS = 10 * 60_000;

// A subagent and a question are items of their own (spec 8.2), never steps.
const OWN_ROW_TOOLS = new Set(["delegate", "delegate_send", "ask_user"]);

function isStep(row: TimelineRow): row is Extract<TimelineRow, { kind: "activity" }> {
	return row.kind === "activity" && row.family !== "reasoning" && !OWN_ROW_TOOLS.has(row.label);
}

// The step in progress is the tray's one live line, and so is a live thought.
function inTray(row: TimelineRow): boolean {
	return row.kind === "activity" && row.state === "running" && !OWN_ROW_TOOLS.has(row.label);
}

export function sessionRows(rows: readonly TimelineRow[], turns: readonly TurnTimes[], timeZone?: string): TimelineRow[] {
	const byId = new Map(turns.map((turn, index) => [turn.id, index]));
	const out: TimelineRow[] = [];
	let run: RunRow | null = null;
	let lastTurn: string | undefined;
	for (const row of rows) {
		if (inTray(row)) continue;
		const turnId = rowTurnId(row);
		if (turnId !== undefined && turnId !== lastTurn) {
			const marker = timeMarker(turns, byId, turnId, lastTurn, timeZone);
			if (marker) out.push(marker);
			// A run never spans a turn change, marked or not: an idle gap too
			// short for a marker (a goal continuation) still ends the run, or its
			// duration would cover the gap and it could not be found by turn.
			run = null;
			lastTurn = turnId;
		}
		if (isStep(row)) {
			if (!run) {
				run = {
					kind: "run",
					id: `run:${row.id}`,
					steps: [],
					...(row.turnId ? { turnId: row.turnId } : {}),
					...(row.transcriptKey ? { transcriptKey: row.transcriptKey } : {}),
					...(row.position ? { position: row.position } : {}),
				};
				out.push(run);
			}
			run.steps.push({ ...row });
			continue;
		}
		if (row.kind === "attachments" && run) {
			const owner = run.steps.find((candidate) => (candidate.transcriptKey ?? candidate.id) === row.sourceTranscriptKey);
			if (owner) {
				owner.images = [...(owner.images ?? []), ...row.items];
				continue;
			}
		}
		run = null;
		out.push(row);
	}
	return out;
}

/** The run that is still growing: the last run of the turn in progress. A
 * live run never folds (spec 8.2). */
export function liveRunId(rows: readonly TimelineRow[], activeTurnId: string | undefined): string | undefined {
	if (activeTurnId === undefined) return undefined;
	for (let index = rows.length - 1; index >= 0; index -= 1) {
		const row = rows[index];
		if (row.kind === "run" && row.turnId === activeTurnId) return row.id;
	}
	return undefined;
}

function timeOf(value: string | undefined): number | undefined {
	if (!value) return undefined;
	const time = Date.parse(value);
	return Number.isFinite(time) ? time : undefined;
}

// Every Intl.DateTimeFormat this module needs, for one time zone. sessionRows
// runs on every publish (every streaming frame), and constructing a
// DateTimeFormat is expensive enough on iOS to pay for it only once per zone
// (undefined keys the device's own zone) rather than once per row.
interface ZoneFormatters {
	dayKey: Intl.DateTimeFormat;
	clock: Intl.DateTimeFormat;
	weekday: Intl.DateTimeFormat;
	monthDay: Intl.DateTimeFormat;
}

const formattersByZone = new Map<string | undefined, ZoneFormatters>();

function formattersFor(timeZone: string | undefined): ZoneFormatters {
	const cached = formattersByZone.get(timeZone);
	if (cached) return cached;
	const formatters: ZoneFormatters = {
		dayKey: new Intl.DateTimeFormat("en-CA", { year: "numeric", month: "2-digit", day: "2-digit", timeZone }),
		clock: new Intl.DateTimeFormat("en-US", { hour: "numeric", minute: "2-digit", hour12: true, timeZone }),
		weekday: new Intl.DateTimeFormat("en-US", { weekday: "short", timeZone }),
		monthDay: new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric", timeZone }),
	};
	formattersByZone.set(timeZone, formatters);
	return formatters;
}

function dayKey(at: number, timeZone?: string): string {
	return formattersFor(timeZone).dayKey.format(at);
}

// An instant's calendar date in the zone, as a count of days since the Unix
// epoch. Reads year/month/day as numbers off the cached day formatter's own
// parts and turns them into a UTC-midnight instant (Date.UTC), so two of
// these subtract to a whole number of calendar days regardless of a DST
// change between them. Subtracting the instants' raw milliseconds and
// re-formatting the result can land a wall-clock hour off instead (fix
// round 1, finding 1: "yesterday" read as a weekday the day after a
// spring-forward).
function dayNumber(at: number, timeZone: string | undefined): number {
	const parts = formattersFor(timeZone).dayKey.formatToParts(at);
	const value = (type: Intl.DateTimeFormatPartTypes) => Number(parts.find((candidate) => candidate.type === type)?.value);
	return Date.UTC(value("year"), value("month") - 1, value("day")) / 86_400_000;
}

function timeMarker(
	turns: readonly TurnTimes[],
	byId: ReadonlyMap<string, number>,
	turnId: string,
	previousId: string | undefined,
	timeZone?: string,
): TimeRow | null {
	const index = byId.get(turnId);
	const start = timeOf(index === undefined ? undefined : turns[index]?.startedAt);
	if (start === undefined) return null;
	const previousIndex = previousId === undefined ? undefined : byId.get(previousId);
	const previous = previousIndex === undefined ? undefined : turns[previousIndex];
	const previousEnd = timeOf(previous?.completedAt) ?? timeOf(previous?.startedAt);
	const show =
		previousEnd === undefined || start - previousEnd >= TIME_GAP_MS || dayKey(start, timeZone) !== dayKey(previousEnd, timeZone);
	return show ? { kind: "time", id: `time:${turnId}`, turnId, at: start } : null;
}

// "2:14 PM" from parts, so the space before the period is always a plain one
// (some ICU versions print a narrow no-break space there).
function clockText(at: number, timeZone?: string): string {
	const parts = formattersFor(timeZone).clock.formatToParts(at);
	const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((candidate) => candidate.type === type)?.value ?? "";
	return `${part("hour")}:${part("minute")} ${part("dayPeriod")}`;
}

export function timeMarkerText(at: number, now: number, timeZone?: string): string {
	const formatters = formattersFor(timeZone);
	const clock = clockText(at, timeZone);
	// Whole calendar days between the two dates in the zone, never a raw
	// millisecond span: 0 is today, 1 is yesterday, 2-6 is the short weekday,
	// anything else (older, or a future `at` from clock skew) is a date.
	const daysAgo = dayNumber(now, timeZone) - dayNumber(at, timeZone);
	if (daysAgo === 0) return `Today ${clock}`;
	if (daysAgo === 1) return `Yesterday ${clock}`;
	if (daysAgo >= 2 && daysAgo <= 6) return `${formatters.weekday.format(at)} ${clock}`;
	return `${formatters.monthDay.format(at)}, ${clock}`;
}

export interface RunPart {
	text: string;
	/** Drawn after the text as "(2 failed)", in red ink even when folded. */
	failed: number;
}

export interface RunSummary {
	steps: number;
	durationMs?: number;
	parts: RunPart[];
	failed: number;
}

type Family = "read" | "edit" | "search" | "fetch" | "webSearch" | "shell" | "other";

const FAMILIES: Record<string, Family> = {
	read_file: "read",
	edit_file: "edit",
	write_file: "edit",
	apply_patch: "edit",
	grep: "search",
	glob: "search",
	list_dir: "search",
	web_fetch: "fetch",
	web_search: "webSearch",
	shell: "shell",
};

interface Group {
	family: Family;
	count: number;
	failed: number;
	programs: Set<string>;
	unnamed: number;
}

// "go test ./agent/..." runs "go test"; "ls -la" runs "ls".
function programOf(command: string | undefined): string | undefined {
	const [first, second] = command?.trim().split(/\s+/) ?? [];
	if (!first) return undefined;
	return second && /^[a-z][\w-]*$/i.test(second) ? `${first} ${second}` : first;
}

function partText(group: Group): string {
	const n = group.count;
	const plural = (one: string, many: string) => (n === 1 ? one : many);
	const times = n === 1 ? "once" : `${n} times`;
	switch (group.family) {
		case "read":
			return `read ${n} ${plural("file", "files")}`;
		case "edit":
			return `edited ${n} ${plural("file", "files")}`;
		case "search":
			return `searched ${times}`;
		case "fetch":
			return `fetched ${n} ${plural("page", "pages")}`;
		case "webSearch":
			return `searched the web ${times}`;
		case "shell": {
			// Named only when every command in the run is known and the same.
			const [only] = [...group.programs];
			return group.programs.size === 1 && group.unnamed === 0 && only
				? `ran ${only}`
				: `ran ${n} ${plural("command", "commands")}`;
		}
		default:
			return `${n} other ${plural("step", "steps")}`;
	}
}

const isNumber = (value: number | undefined): value is number => value !== undefined;

// How long the run took: from its first step's start to its last step's end,
// only when EVERY step carries both clock times. A settled step at a compact
// detail level carries no clock times at all (Jesse's summary-only ruling), so
// reading a duration from only some of a run's steps (say, just its failed
// ones) would understate the run rather than say nothing.
function runDuration(steps: readonly RunStep[]): number | undefined {
	if (steps.length === 0) return undefined;
	const starts = steps.map((step) => step.detail.startedAtMs);
	const ends = steps.map((step) => step.detail.endedAtMs);
	if (!starts.every(isNumber) || !ends.every(isNumber)) return undefined;
	return Math.max(...ends) - Math.min(...starts);
}

export function runSummary(steps: readonly RunStep[]): RunSummary {
	const groups = new Map<Family, Group>();
	let failed = 0;
	for (const step of steps) {
		const family = FAMILIES[step.label] ?? "other";
		let group = groups.get(family);
		if (!group) {
			group = { family, count: 0, failed: 0, programs: new Set(), unnamed: 0 };
			groups.set(family, group);
		}
		group.count += 1;
		if (step.state === "failed") {
			group.failed += 1;
			failed += 1;
		}
		if (family === "shell") {
			const program = programOf(str(parseArgs(step.detail.arguments), "command"));
			if (program) group.programs.add(program);
			else group.unnamed += 1;
		}
	}
	const durationMs = runDuration(steps);
	return {
		steps: steps.length,
		...(durationMs === undefined ? {} : { durationMs }),
		parts: [...groups.values()].map((group) => ({ text: partText(group), failed: group.failed })),
		failed,
	};
}

/** The run line's opening: "12 steps · 8m". */
export function runHeadText(summary: RunSummary): string {
	const head = [`${summary.steps} ${summary.steps === 1 ? "step" : "steps"}`];
	if (summary.durationMs !== undefined) head.push(compactDuration(summary.durationMs));
	return head.join(" · ");
}

/** What follows a part's text: " (2 failed)", or nothing. */
export function runPartFailedText(part: RunPart): string {
	return part.failed > 0 ? ` (${part.failed} failed)` : "";
}

/** The run's line as one string: its accessibility label, and what tests read. */
export function runSummaryText(summary: RunSummary): string {
	const parts = summary.parts.map((part) => `${part.text}${runPartFailedText(part)}`);
	return [runHeadText(summary), parts.join(", ")].join(" · ");
}
