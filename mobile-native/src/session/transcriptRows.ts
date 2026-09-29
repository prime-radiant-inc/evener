// The Session's transcript rows (spec 8.2):
// - consecutive steps fold into one run line ("12 steps · 8m · read 6 files,
//   ran go test (2 failed), edited 3 files"), and a step's images ride with it;
// - the step in progress, and a live thought, are left to the status tray
//   (ruling 10);
// - a question still waiting is left to the ask dock, which is the question
//   while it is open;
// - a time marker introduces the first turn (once no older history is left
//   to load), a turn that starts after ten quiet minutes, and a new day.
import {
	answeredAskUserSuffix,
	type AskUserQuestion,
	filePathOf,
	type ItemModel,
	mcpToolParts,
	parseArgs,
	parseAskUserQuestions,
	shellCommand,
	skillName,
	type ThreadModel,
	type ToolFamily,
	type TurnModel,
	toolFamily,
	words,
} from "@evener/appwire-client";
import { hubTime } from "../board/attention";
import { readerKey } from "../readerPosition";
import { type RunStep, rowTurnId, type TimelineRow } from "../timeline";
import { compactDuration } from "./format";

type RunRow = Extract<TimelineRow, { kind: "run" }>;
type TimeRow = Extract<TimelineRow, { kind: "time" }>;
type TurnTimes = Pick<TurnModel, "id" | "startedAt" | "completedAt">;

export const TIME_GAP_MS = 10 * 60_000;

// A subagent and a question are items of their own (spec 8.2), never steps.
const OWN_ROW_TOOLS = new Set(["delegate", "ask_user"]);

export function isStep(row: TimelineRow): row is Extract<TimelineRow, { kind: "activity" }> {
	return row.kind === "activity" && row.family !== "reasoning" && !OWN_ROW_TOOLS.has(row.label);
}

// The step in progress is the tray's one live line, and so is a live thought.
function inTray(row: TimelineRow): boolean {
	return row.kind === "activity" && row.state === "running" && !OWN_ROW_TOOLS.has(row.label);
}

export interface SessionRowsOptions {
	/** The zone that decides where a new day starts; the device's own when unset. */
	timeZone?: string;
	/** Older history is still to load above these rows. The first loaded turn
	 * then gets no time marker: it would sit at index 0, and the page above
	 * can remove it (its turn ended under ten minutes before), taking the
	 * list's first key with it, which the list's position keeping needs to
	 * find again. The marker appears once the history is whole. */
	olderToLoad?: boolean;
}

export function sessionRows(
	rows: readonly TimelineRow[],
	turns: readonly TurnTimes[],
	{ timeZone, olderToLoad = false }: SessionRowsOptions = {},
): TimelineRow[] {
	const byId = new Map(turns.map((turn) => [turn.id, turn]));
	const out: TimelineRow[] = [];
	let run: RunRow | null = null;
	let lastTurn: string | undefined;
	const marked = new Set<string>();
	for (const row of rows) {
		if (inTray(row) || row.kind === "question") continue;
		const turnId = rowTurnId(row);
		if (turnId !== undefined && turnId !== lastTurn) {
			// One turn can own rows another turn's row sits between: the reducer
			// seats an overlay notice in the display turn that holds its recorded
			// item, and the notice keeps its own turn id. When the outer turn
			// resumes, its start would be compared against the notice's turn (no
			// times, read as a gap) and a second marker for a turn already marked
			// would appear. A turn is marked at most once.
			if (!marked.has(turnId)) {
				marked.add(turnId);
				const firstLoadedTurn = lastTurn === undefined;
				const marker = olderToLoad && firstLoadedTurn ? null : timeMarker(byId, turnId, lastTurn, timeZone);
				if (marker) out.push(marker);
			}
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
			const owner = run.steps.find(
				(candidate) => (candidate.transcriptKey ?? candidate.id) === row.sourceTranscriptKey,
			);
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

/** The questions an earlier ask_user row shows (QuestionHistory), or
 * undefined when its arguments name none it can read. */
export function askRowQuestions(row: Extract<TimelineRow, { kind: "activity" }>): AskUserQuestion[] | undefined {
	return row.label === "ask_user"
		? parseAskUserQuestions({ argumentsJSON: row.detail.arguments } as ItemModel)
		: undefined;
}

// A composed answer reply: "[answers]" and then numbered lines like
// '1. [Choice] → "Drop them"' (the package's composeAskAnswers).
const ANSWER_REPLY = /^\[answers\]\n\d+\. \[/;

/** Drops the "[answers]" message you sent a question, once a question row
 * that shows its questions came before it: that row shows your answer
 * beneath the question (spec 8.2, "Question (history)"). A message that only
 * looks like answers, or whose own question row can't show itself, stays. */
export function hideAnswerMessages(rows: readonly TimelineRow[]): TimelineRow[] {
	let asked = false;
	return rows.filter((row) => {
		if (row.kind === "activity" && askRowQuestions(row)) asked = true;
		if (asked && row.kind === "user" && ANSWER_REPLY.test(row.text)) {
			// The reply now shows beneath its question, so clear the flag: a later
			// answer needs a question of its own before it too is hidden.
			asked = false;
			return false;
		}
		return true;
	});
}

/** Your answer to the question the ask_user item `itemId` asked, for its
 * history row ("You answered: Drop them"), or undefined before you answer.
 * The package's suffix reads " — answered: Drop them"; the row wants the
 * answer alone. */
export function answerTo(model: Pick<ThreadModel, "turns"> | null, itemId: string): string | undefined {
	if (!model) return undefined;
	for (const turn of model.turns) {
		const item = turn.items.find((candidate) => candidate.id === itemId);
		if (!item) continue;
		return answeredAskUserSuffix(model as ThreadModel, item)?.replace(/^ \u2014 answered: /, "");
	}
	return undefined;
}

/** The run that is still growing: the last run of the turn in progress
 * (spec 8.2). */
export function liveRunId(rows: readonly TimelineRow[], activeTurnId: string | undefined): string | undefined {
	if (activeTurnId === undefined) return undefined;
	for (let index = rows.length - 1; index >= 0; index -= 1) {
		const row = rows[index];
		if (row.kind === "run" && row.turnId === activeTurnId) return row.id;
	}
	return undefined;
}

/** How many rows arrived below while you read above the end: the rows after
 * the last one whose reader key you had when you left it. Older history that
 * loads above never counts, and neither do time markers. */
export function newRowCount(rows: readonly TimelineRow[], seen: ReadonlySet<string>): number {
	let count = 0;
	for (let index = rows.length - 1; index >= 0; index -= 1) {
		const row = rows[index];
		if (seen.has(readerKey(row))) break;
		if (row.kind !== "time") count += 1;
	}
	return count;
}

/** The latest turn that is no longer in progress: what a reader at the end
 * has seen (ruling 31's turnsSeen). */
export function latestSettledTurn(
	conversation: { turns: readonly Pick<TurnModel, "id" | "status">[] } | null,
): string | undefined {
	const turns = conversation?.turns ?? [];
	for (let index = turns.length - 1; index >= 0; index -= 1) {
		if (turns[index].status !== "inProgress") return turns[index].id;
	}
	return undefined;
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
// re-formatting the result can land a wall-clock hour off instead, so the
// day after a spring-forward would read as a weekday rather than
// "Yesterday".
function dayNumber(at: number, timeZone: string | undefined): number {
	const parts = formattersFor(timeZone).dayKey.formatToParts(at);
	const value = (type: Intl.DateTimeFormatPartTypes) =>
		Number(parts.find((candidate) => candidate.type === type)?.value);
	return Date.UTC(value("year"), value("month") - 1, value("day")) / 86_400_000;
}

function timeMarker(
	byId: ReadonlyMap<string, TurnTimes>,
	turnId: string,
	previousId: string | undefined,
	timeZone?: string,
): TimeRow | null {
	const start = hubTime(byId.get(turnId)?.startedAt);
	if (start === null) return null;
	const previous = previousId === undefined ? undefined : byId.get(previousId);
	const previousEnd = hubTime(previous?.completedAt) ?? hubTime(previous?.startedAt);
	const show =
		previousEnd === null ||
		start - previousEnd >= TIME_GAP_MS ||
		dayKey(start, timeZone) !== dayKey(previousEnd, timeZone);
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
	/** The part's identity while its words change as the run grows: its
	 * family, or for an MCP server or a tool no summary covers, that server or
	 * tool, since each gets a part of its own. */
	key: string;
	family: ToolFamily;
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

/** What a step acted on: the command for a shell step, else the file or path
 * it named. `parsed` is the arguments already decoded, for a caller that read
 * them itself. */
export function stepTarget(
	label: string,
	argumentsJSON: string | undefined,
	parsed?: Record<string, unknown>,
): string | undefined {
	const args = parsed ?? parseArgs(argumentsJSON);
	if (toolFamily(label) === "shell") return shellCommand(args) || undefined;
	return filePathOf(args);
}

/** What a step says it did: the words its row was built with (projectedRows
 * reads them once from the whole step with the package's toolStepSummary),
 * else its label. The one place a renderer reads a step's words from. */
export function stepWords(step: Pick<RunStep, "label" | "detail">): string {
	return step.detail.summary ?? step.label;
}

interface Group {
	key: string;
	family: ToolFamily;
	/** What an MCP part or a tool part names: the server, or the tool, in words. */
	name: string;
	count: number;
	failed: number;
	/** The programs a shell part's commands ran, or the skills a skill part
	 * activated. */
	names: Set<string>;
	unnamed: number;
}

// "go test ./agent/..." runs "go test"; "ls -la" runs "ls".
function programOf(command: string | undefined): string | undefined {
	const [first, second] = command?.trim().split(/\s+/) ?? [];
	if (!first) return undefined;
	return second && /^[a-z][\w-]*$/i.test(second) ? `${first} ${second}` : first;
}

// A step's part: one per family, except that each tool no summary covers gets
// its own ("used compact context once"). MCP tools share one part.
function partOf(label: string): { key: string; family: ToolFamily; name: string } {
	const family = toolFamily(label);
	if (family === "tool") {
		const name = words(label) || "a tool";
		return { key: `tool:${name}`, family, name };
	}
	return { key: family, family, name: "" };
}

// What a step contributes to its part's words: the program a shell command
// ran, the skill a skill step activated, or the server an MCP tool is on.
function namedBy(family: ToolFamily, step: RunStep): string | undefined {
	if (family === "shell") return programOf(stepTarget(step.label, step.detail.arguments));
	if (family === "skill") return skillName({ argumentsJSON: step.detail.arguments }) || undefined;
	if (family === "mcp") return mcpToolParts(step.label)?.server;
	return undefined;
}

function partText(group: Group): string {
	const n = group.count;
	const plural = (one: string, many: string) => (n === 1 ? one : many);
	const times = n === 1 ? "once" : `${n} times`;
	// Named only when every step in the part is known and the same.
	const [only] = [...group.names];
	const oneName = group.names.size === 1 && group.unnamed === 0 ? only : undefined;
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
		case "shell":
			return oneName ? `ran ${oneName}` : `ran ${n} ${plural("command", "commands")}`;
		case "skill":
			return oneName ? `used skill ${oneName}` : `used ${n} ${plural("skill", "skills")}`;
		case "transcript":
			return n === 1 ? "read a transcript" : `read ${n} transcripts`;
		case "sessions":
			return n === 1 ? "searched sessions" : `searched sessions ${n} times`;
		case "mcp":
			// One server reads by name; several read as how many MCP tools ran.
			return oneName ? `used ${oneName} ${times}` : `used ${n} MCP tools`;
		case "tool":
			return `used ${group.name} ${times}`;
	}
}

const isNumber = (value: number | undefined): value is number => value !== undefined;

// How long the run took: from its first step's start to its last step's end,
// only when EVERY step carries both clock times. Every step keeps them at
// every level, a summary-only step included, but a step whose times the hub
// didn't send or that don't parse has none, and a duration read from only
// some of a run's steps would understate the run rather than say nothing.
function runDuration(steps: readonly RunStep[]): number | undefined {
	if (steps.length === 0) return undefined;
	const starts = steps.map((step) => step.detail.startedAtMs);
	const ends = steps.map((step) => step.detail.endedAtMs);
	if (!starts.every(isNumber) || !ends.every(isNumber)) return undefined;
	return Math.max(...ends) - Math.min(...starts);
}

export function runSummary(steps: readonly RunStep[]): RunSummary {
	const groups = new Map<string, Group>();
	let failed = 0;
	for (const step of steps) {
		const part = partOf(step.label);
		let group = groups.get(part.key);
		if (!group) {
			group = { ...part, count: 0, failed: 0, names: new Set(), unnamed: 0 };
			groups.set(part.key, group);
		}
		group.count += 1;
		if (step.state === "failed") {
			group.failed += 1;
			failed += 1;
		}
		if (part.family === "shell" || part.family === "skill" || part.family === "mcp") {
			const name = namedBy(part.family, step);
			if (name) group.names.add(name);
			else group.unnamed += 1;
		}
	}
	const durationMs = runDuration(steps);
	return {
		steps: steps.length,
		...(durationMs === undefined ? {} : { durationMs }),
		parts: [...groups.values()].map((group) => ({
			key: group.key,
			family: group.family,
			text: partText(group),
			failed: group.failed,
		})),
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
