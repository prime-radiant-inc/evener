// The Board's attention model (spec 13.1-13.2) as pure functions over the
// navigation rows the hub already sends. Where the spec wants a fact the rows
// don't carry yet, the fallback from spec 18 lives here, and each server
// addition replaces its fallback in this file: S1 (why text), S2 (approval
// flag), S3 (subagent counts). S4's seen marker lives in hubSeen.ts, beside
// boardMemory.ts's fallback. S5 (activity) has landed: whyLine and liveBands
// take the activity poll's own data (its caller polls evener/activity/read
// and hands the read back in - this file has no client of its own), with no
// fallback left when it's given. A subagent failure never puts a Board row in
// Needs you; the row's subagent chip (subagentChip) counts it, and the
// session's Subagents list holds the detail.
import type { NavigationSessionSummary, SessionActivity } from "@evener/appwire-client";
import { quietState } from "@evener/appwire-client";
import { relativeAge, subagentTallyToShow } from "@evener/appwire-client/state/navigation";
import { compactDuration } from "../session/format";

export type BoardState =
	| "failed"
	| "question"
	| "approval"
	| "warning"
	| "restartNeeded"
	| "working"
	| "finished"
	| "idle"
	| "shutDown";

export type Band = "needsYou" | "finished" | "working" | "idle";

export interface ClassifiedRow {
	row: NavigationSessionSummary;
	state: BoardState;
}

export interface LiveBands {
	needsYou: ClassifiedRow[];
	finished: ClassifiedRow[];
	working: ClassifiedRow[];
	idle: ClassifiedRow[];
}

const WORDS: Record<BoardState, string> = {
	failed: "Failed",
	question: "Question",
	approval: "Approval",
	warning: "Warning",
	restartNeeded: "Restart needed",
	working: "Working",
	finished: "Finished",
	idle: "Idle",
	shutDown: "Shut down",
};

export function stateWord(state: BoardState): string {
	return WORDS[state];
}

// The states that put a session in the hub's needs_you section on their own.
const EXPLAINED = new Set(["awaiting", "warning", "restartRequired", "errored"]);

/** The hub promotes a session with a pending sandbox escalation into its
 * needs_you section but leaves the row "active" (promotedAttentionLevel in
 * cmd/evener-hub/internal/hubcore/attention.go), so a row there whose state
 * explains nothing else is there for an approval. Since S2a (#2508), the row
 * also carries approval_pending itself, which boardState reads directly; this
 * inference stays because a hub without S2a never sets that flag, so its rows
 * still need the needs_you section's membership to say why they're there. */
export function approvalRefs(needsYouSection: readonly NavigationSessionSummary[]): Set<string> {
	const refs = new Set<string>();
	for (const row of needsYouSection) if (!EXPLAINED.has(row.state)) refs.add(row.ref);
	return refs;
}

/** The Board state a hub state decides on its own, whatever flags ride
 * along, or null for one that leaves it to the row's other facts. A Board
 * row and a search result both start here. */
export function decisiveState(state: string): BoardState | null {
	switch (state) {
		case "errored":
			return "failed";
		case "restartRequired":
			return "restartNeeded";
		case "warning":
			return "warning";
		case "ended":
		case "notLoaded":
			return "shutDown";
	}
	return null;
}

export function boardState(row: NavigationSessionSummary, approval: boolean, seen: boolean): BoardState {
	// A row from an offline source can't be reached, whatever state it last
	// reported: it is never Working, Finished or Needs you.
	if (row.offline) return "shutDown";
	const decisive = decisiveState(row.state);
	// Only a nonblocking warning yields to live child work. Search keeps its
	// existing decisiveState rule, and failed children never decide this mark.
	if (decisive && (decisive !== "warning" || row.ask_pending || approval || row.approval_pending)) return decisive;
	if (row.state === "awaiting" && row.ask_pending) return "question";
	if (approval || row.approval_pending === true) return "approval";
	if (row.state === "active") return "working";
	const runningSubagents = row.kind === "session" && (subagentTallyToShow(row)?.running ?? 0) > 0;
	if (runningSubagents) return "working";
	if (decisive) return decisive;
	if (row.dormant || seen) return "idle";
	return "finished";
}

/** Classifies any of the Board's rows, Live's or a category's: the needs_you
 * section marks approvals (approvalRefs), and isSeen splits Finished from
 * Idle. */
export function rowClassifier(
	needsYouSection: readonly NavigationSessionSummary[],
	isSeen: (row: NavigationSessionSummary) => boolean,
): (row: NavigationSessionSummary) => ClassifiedRow {
	const approvals = approvalRefs(needsYouSection);
	return (row) => ({ row, state: boardState(row, approvals.has(row.ref), isSeen(row)) });
}

const BANDS: Record<BoardState, Band | null> = {
	failed: "needsYou",
	question: "needsYou",
	approval: "needsYou",
	warning: "needsYou",
	restartNeeded: "needsYou",
	working: "working",
	finished: "finished",
	idle: "idle",
	shutDown: null,
};

export function bandOf(state: BoardState): Band | null {
	return BANDS[state];
}

/** A hub timestamp in milliseconds, or null when the hub sent none or one
 * that doesn't parse. */
export function hubTime(value?: string | null): number | null {
	if (!value) return null;
	const time = Date.parse(value);
	return Number.isFinite(time) ? time : null;
}

// A row without a readable updated_at sorts as the oldest.
function time(row: NavigationSessionSummary): number {
	return hubTime(row.updated_at) ?? 0;
}
function byRef(a: ClassifiedRow, b: ClassifiedRow): number {
	return a.row.ref < b.row.ref ? -1 : a.row.ref > b.row.ref ? 1 : 0;
}
function oldestFirst(a: ClassifiedRow, b: ClassifiedRow): number {
	return time(a.row) - time(b.row) || byRef(a, b);
}
// Finished and Idle order by when the turn ended (S4): updated_at moves on
// renames and model rounds too, so it stands in only for a row without a
// readable turn_ended_at.
function endedTime(row: NavigationSessionSummary): number {
	return hubTime(row.turn_ended_at) ?? time(row);
}
function newestEndedFirst(a: ClassifiedRow, b: ClassifiedRow): number {
	return endedTime(b.row) - endedTime(a.row) || byRef(a, b);
}
// Spec 7.1's order: failed leads, then a question or approval, then a
// warning or restart-needed, regardless of age; age breaks ties within a
// band. The hub sorts its needs_you section into the same bands
// (hubapi.NeedsYouBand: failed first, then any row with a pending question or
// approval whatever its own state, then everything else). boardState's mark
// precedence returns "warning"/"restartNeeded" for a row before ever
// consulting ask_pending/approval_pending, so a warning or restart-needed row
// that also carries one of those flags must still read it here directly - the
// mark stays "warning"/"restartNeeded", but the row is blocked on you either
// way. The approval mark is also checked directly, since a hub older than
// S2a carries no raw approval_pending and the mark's own fallback
// (approvalRefs, inferred from needs_you section membership) is the only
// signal such a row has.
function needsYouRank(item: ClassifiedRow): number {
	if (item.state === "failed") return 0;
	if (item.row.ask_pending || item.row.approval_pending || item.state === "approval") return 1;
	return 2;
}
function needsYouOrder(a: ClassifiedRow, b: ClassifiedRow): number {
	return needsYouRank(a) - needsYouRank(b) || oldestFirst(a, b);
}
// Stuck first, else keep relative order: Array.prototype.sort is stable, so a
// comparator that only distinguishes stuck from not leaves the hub's own
// order (ruling 10) untouched within each group (spec 7.1, S5).
function workingOrder(isStuck: (row: NavigationSessionSummary) => boolean) {
	return (a: ClassifiedRow, b: ClassifiedRow): number => Number(isStuck(b.row)) - Number(isStuck(a.row));
}

/** Splits Live into the spec's four bands. Rows from the needs_you section
 * join when Live's loaded pages don't hold them yet, so a session that needs
 * you is never hidden behind "load more"; a row in both keeps its Live copy,
 * which carries the row's children (fork originals and cluster members).
 * Working keeps the hub's Live order (ruling 10),
 * except a row isStuck marks (S5's quietState "stuck", from the activity
 * poll), which floats to the top of the band (spec 7.1). */
export function liveBands(
	live: readonly NavigationSessionSummary[],
	needsYouSection: readonly NavigationSessionSummary[],
	isSeen: (row: NavigationSessionSummary) => boolean,
	isStuck: (row: NavigationSessionSummary) => boolean = () => false,
): LiveBands {
	const classify = rowClassifier(needsYouSection, isSeen);
	const rows = new Map<string, NavigationSessionSummary>();
	for (const row of live) rows.set(row.ref, row);
	for (const row of needsYouSection) if (!rows.has(row.ref)) rows.set(row.ref, row);
	const bands: LiveBands = { needsYou: [], finished: [], working: [], idle: [] };
	for (const row of rows.values()) {
		const item = classify(row);
		const band = bandOf(item.state);
		if (band) bands[band].push(item);
	}
	bands.needsYou.sort(needsYouOrder);
	bands.finished.sort(newestEndedFirst);
	bands.idle.sort(newestEndedFirst);
	bands.working.sort(workingOrder(isStuck));
	return bands;
}

export interface LiveSummary {
	needsYou: number;
	finished: number;
	working: number;
	idle: number;
}

/** The Live summary line's counts, or null when fewer than two bands have
 * sessions and the band headers already say it all (spec 7.1). */
export function liveSummary(bands: LiveBands): LiveSummary | null {
	const counts: LiveSummary = {
		needsYou: bands.needsYou.length,
		finished: bands.finished.length,
		working: bands.working.length,
		idle: bands.idle.length,
	};
	return Object.values(counts).filter((count) => count > 0).length >= 2 ? counts : null;
}

/** "1 session", "3 sessions": `separator` joins the count to its noun, a
 * no-break space where the two must stay on one line. */
export const plural = (count: number, noun: string, separator = " ") =>
	`${count}${separator}${noun}${count === 1 ? "" : "s"}`;
/** A section's VoiceOver label, shared by its chip and its header. */
export const sectionLabel = (name: string, count: number, noun: string) => `${name}, ${plural(count, noun)}`;

export function summaryText(band: Band, count: number): string {
	if (band === "needsYou") return `${count} ${count === 1 ? "needs you" : "need you"}`;
	return `${count} ${band}`;
}

export type Hue = "danger" | "attention";

export interface WhyLine {
	word?: string;
	hue?: Hue;
	/** The line reads "May be stuck" (spec 7.1, 13.1): the whole text draws in
	 * the attention ink, since there is no word to color apart from the rest,
	 * and the row's meter echoes it. */
	stuck?: boolean;
	text: string;
}

// Until S1 carries the question, the approval's target and the error, the
// reason says what the person can do next.
const REASONS: Partial<Record<BoardState, { hue: Hue; text: string }>> = {
	failed: { hue: "danger", text: "open the session to see what went wrong" },
	question: { hue: "attention", text: "waiting for your answer" },
	approval: { hue: "attention", text: "waiting for your permission" },
	warning: { hue: "attention", text: "open the session to see it" },
	restartNeeded: { hue: "attention", text: "restart this session to pick up the hub's update" },
};

// relativeAge buckets m/h/d purely from the gap between two instants (now
// minus a timestamp); anchoring both ends of that gap to the epoch reuses its
// buckets for a duration quietState reports directly, instead of copying its
// thresholds here.
function durationLabel(forMs: number): string {
	return relativeAge(new Date(0).toISOString(), forMs) ?? "0m";
}

/** The why line of a session or subagent waiting on its subagents: on the
 * Board, in the tray, in the Activity list and on a subagent's row (spec
 * 13.1). */
export function waitingOnSubagents(count: number): string {
	return `Waiting on ${plural(count, "subagent")}`;
}

/** No update for this long reads "Quiet" in a session's tray, on a
 * subagent's row and in the Activity list: the web transcript's threshold
 * (cmd/evener-hub/frontend/src/panes/session/transcript/flow/liveness.ts).
 * The tray also waits this long before showing a first model retry. The
 * Board's rows read the package's quietState instead. */
export const AGENT_QUIET_AFTER_MS = 20_000;

/** The why line of a running agent with nothing more to say: Quiet once it
 * has gone AGENT_QUIET_AFTER_MS without an update, else Working. */
export function quietOrWorking(silentMs: number): string {
	return silentMs >= AGENT_QUIET_AFTER_MS ? `Quiet ${compactDuration(silentMs)}` : "Working";
}

/** whyLine's working-row text once a real activity read exists (S5): the
 * read's own subagent tally is authoritative and wins outright, never mixed
 * with the row's own tally guess (a stale local count must not survive a
 * fresh read of zero). Quiet and stuck read from quietState, which itself
 * withholds both while a subagent runs. Absent either, the row says what the
 * session last set out to do, else the job it is running, else "Working": this
 * never falls back to the row's own tally, because a real read already
 * answered the subagent question, even when the answer is zero. */
function workingWhyLine(row: NavigationSessionSummary, activity: SessionActivity, msSinceReadMs: number): WhyLine {
	if (activity.runningSubagents > 0) return { text: waitingOnSubagents(activity.runningSubagents) };
	const quiet = quietState(activity, msSinceReadMs);
	if (quiet?.state === "stuck")
		return { text: `May be stuck · no updates for ${durationLabel(quiet.forMs)}`, stuck: true };
	if (quiet?.state === "quiet") return { text: `Quiet ${durationLabel(quiet.forMs)}` };
	return { text: commandOrWorking(row, activity.latestIntent) };
}

/** The row's why line. activity and msSinceReadMs are S5's live read (the
 * Board polls evener/activity/read); omitting them keeps every state exactly
 * as it read before S5, including a working row's pre-S5 fallback
 * (workingActivity). */
export function whyLine(item: ClassifiedRow, activity?: SessionActivity, msSinceReadMs = 0): WhyLine | null {
	if (item.state === "working")
		return activity ? workingWhyLine(item.row, activity, msSinceReadMs) : { text: workingActivity(item.row) };
	const reason = REASONS[item.state];
	return reason ? { word: WORDS[item.state], ...reason } : null;
}

/** A working row's own words, where nothing louder applies: what the session
 * last set out to do (the read's latest tool intent), else the job it is
 * running, else the bare state word. The intent leads the job because it says
 * what the job is for; the daemon cuts one to a line and drops it when a turn
 * begins (activity_meter.go). */
function commandOrWorking(row: NavigationSessionSummary, latestIntent?: string): string {
	if (latestIntent) return latestIntent;
	const command = row.running_job_command;
	return command ? `Running ${command}` : "Working";
}

/** What a working session is doing when there is no activity read at all (an
 * older hub, before the first poll, or while disconnected): the row's own
 * subagents tally stands in for S5's read (S3). The row no longer nests
 * subagents under its children -- those hold only fork originals and cluster
 * members -- so the tally is the only place a subagent count comes from. */
export function workingActivity(row: NavigationSessionSummary): string {
	const running = row.subagents?.running ?? 0;
	if (running > 0) return waitingOnSubagents(running);
	return commandOrWorking(row);
}

/** The subagent chip's text from the counts the shared gate shows
 * (subagentTallyToShow, the same one the web rail reads): "3 running",
 * "2 failed", or "2 running · 3 failed". The chip colors each run on its own,
 * so the running count stays in the neutral ink and only the failure reads in
 * the danger ink (D2): a failed subagent is not something the user must act
 * on, so it must not wear the Needs you attention ink. */
export function subagentChipText(tally: { running: number; failed: number }): string {
	const parts: string[] = [];
	if (tally.running > 0) parts.push(`${tally.running} running`);
	if (tally.failed > 0) parts.push(`${tally.failed} failed`);
	return parts.join(" · ");
}

export interface Usual {
	project?: string;
	host?: string;
}

function mostCommon(values: readonly string[]): string | undefined {
	const counts = new Map<string, number>();
	for (const value of values) if (value) counts.set(value, (counts.get(value) ?? 0) + 1);
	let best: string | undefined;
	let bestCount = 0;
	for (const [value, count] of counts)
		if (count > bestCount || (count === bestCount && best !== undefined && value < best)) {
			best = value;
			bestCount = count;
		}
	return best;
}

/** The fleet's usual project and host: the most common among Live rows. A row
 * prints either only when it differs (spec 7.2). */
export function usualPlace(rows: readonly NavigationSessionSummary[]): Usual {
	return {
		project: mostCommon(rows.map((row) => row.project)),
		host: mostCommon(rows.map((row) => row.host_id)),
	};
}

/** The task in progress, by its own position in the list ("Task 4 of 7 · Fix
 * the settle/drain race", spec 7.2). Null while no task is in progress,
 * finished lists included: the hub omits `current` then (NavigationTaskProgress's
 * doc comment). current_id is the task's stable list position (a session's
 * tasks are only ever appended, never reordered or removed, so id order is
 * list order) and is the number to show: counting done-or-cancelled tasks
 * instead would overstate the position whenever a later task settles before
 * this one, which dependency-driven completion allows. That count is only a
 * fallback for a payload that, contrary to the contract, carries `current`
 * without `current_id`. */
export function taskLine(row: NavigationSessionSummary): string | null {
	const tasks = row.tasks;
	if (!tasks?.current) return null;
	const position = tasks.current_id ?? tasks.done + (tasks.cancelled ?? 0) + 1;
	return `Task ${position} of ${tasks.total} · ${tasks.current}`;
}

export interface LastLine {
	task?: string;
	project?: string;
	host?: string;
	model?: string;
}

/** The row's last line (spec 7.2): the task in progress first, then the
 * project and host, each only when it differs from the fleet's usual one,
 * then the model's display name (S17) when "Show model on Board rows" is on.
 * Subagent failures never appear here; they show only in the session's
 * Subagents chip and list. */
export function lastLine(
	row: NavigationSessionSummary,
	usual: Usual,
	hostLabel: (hostId: string) => string,
	showModel = false,
): LastLine | null {
	const line: LastLine = {};
	const task = taskLine(row);
	if (task) line.task = task;
	if (row.project && row.project !== usual.project) line.project = row.project;
	if (row.host_id !== usual.host) line.host = hostLabel(row.host_id);
	if (showModel && row.model_name) line.model = row.model_name;
	return Object.keys(line).length > 0 ? line : null;
}

/** Names a host by its manifest source's label. A host the manifest doesn't
 * name, or every host before the manifest loads, goes to the fallback. */
export function hostLabeler(
	sources: readonly { id: string; label: string }[] | undefined,
	fallback: (hostId: string) => string = (hostId) => hostId,
): (hostId: string) => string {
	const labels = new Map((sources ?? []).map((source) => [source.id, source.label]));
	return (hostId) => labels.get(hostId) ?? fallback(hostId);
}
