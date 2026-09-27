// The Board's attention model (spec 13.1-13.2) as pure functions over the
// navigation rows the hub already sends. Where the spec wants a fact the rows
// don't carry yet, the fallback from spec 18 lives here, and each server
// addition replaces its fallback in this file: S1 (why text), S2 (approval
// flag), S3 (subagent failures), S5 (activity), S13 (tasks). S4 replaces the
// seen marker, which lives in boardMemory.ts.
import type { NavigationSessionSummary } from "@evener/appwire-client";

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

export function boardState(
	row: NavigationSessionSummary,
	approval: boolean,
	seen: boolean,
): BoardState {
	// A row from an offline source can't be reached, whatever state it last
	// reported: it is never Working, Finished or Needs you.
	if (row.offline) return "shutDown";
	switch (row.state) {
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
	if (row.state === "awaiting" && row.ask_pending) return "question";
	if (approval || row.approval_pending === true) return "approval";
	if (row.state === "active") return "working";
	if (row.dormant || seen) return "idle";
	return "finished";
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
function newestFirst(a: ClassifiedRow, b: ClassifiedRow): number {
	return time(b.row) - time(a.row) || byRef(a, b);
}
function needsYouOrder(a: ClassifiedRow, b: ClassifiedRow): number {
	const rank = (item: ClassifiedRow) => (item.state === "failed" ? 0 : 1);
	return rank(a) - rank(b) || oldestFirst(a, b);
}

/** Splits Live into the spec's four bands. Rows from the needs_you section
 * join when Live's loaded pages don't hold them yet, so a session that needs
 * you is never hidden behind "load more"; a row in both keeps its Live copy,
 * which carries children. Working keeps the hub's Live order (ruling 10). */
export function liveBands(
	live: readonly NavigationSessionSummary[],
	needsYouSection: readonly NavigationSessionSummary[],
	isSeen: (row: NavigationSessionSummary) => boolean,
): LiveBands {
	const approvals = approvalRefs(needsYouSection);
	const rows = new Map<string, NavigationSessionSummary>();
	for (const row of live) rows.set(row.ref, row);
	for (const row of needsYouSection) if (!rows.has(row.ref)) rows.set(row.ref, row);
	const bands: LiveBands = { needsYou: [], finished: [], working: [], idle: [] };
	for (const row of rows.values()) {
		const state = boardState(row, approvals.has(row.ref), isSeen(row));
		const band = bandOf(state);
		if (band) bands[band].push({ row, state });
	}
	bands.needsYou.sort(needsYouOrder);
	bands.finished.sort(newestFirst);
	bands.idle.sort(newestFirst);
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

export function summaryText(band: Band, count: number): string {
	if (band === "needsYou") return `${count} ${count === 1 ? "needs you" : "need you"}`;
	return `${count} ${band}`;
}

export type Hue = "danger" | "attention";

export interface WhyLine {
	word?: string;
	hue?: Hue;
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

export function whyLine(item: ClassifiedRow): WhyLine | null {
	if (item.state === "working") return { text: workingActivity(item.row) };
	const reason = REASONS[item.state];
	return reason ? { word: WORDS[item.state], ...reason } : null;
}

/** What a working session is doing, from what its row carries (S5 adds the
 * current step and quiet spells). */
export function workingActivity(row: NavigationSessionSummary): string {
	const subagents = row.children.filter((child) => child.state === "active").length;
	// The hub caps a row's children; until S3 tallies the whole tree, the
	// waiting line says how many more there are rather than undercounting
	// (spec 18, S3's fallback). With no loaded child active, nothing says the
	// session is waiting on subagents, so it falls through.
	const more = row.more_subagents ?? 0;
	if (subagents > 0)
		return `Waiting on ${subagents} ${subagents === 1 ? "subagent" : "subagents"}${more > 0 ? ` (+${more} more)` : ""}`;
	const command = row.running_jobs?.find((job) => job.command)?.command;
	if (command) return `Running ${command}`;
	return "Working";
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

export interface LastLine {
	project?: string;
	host?: string;
}

/** The row's last line on the fallbacks: task progress waits for S13 and
 * subagent failures for S3, so today it is the unusual project and host. */
export function lastLine(
	row: NavigationSessionSummary,
	usual: Usual,
	hostLabel: (hostId: string) => string,
): LastLine | null {
	const line: LastLine = {};
	if (row.project && row.project !== usual.project) line.project = row.project;
	if (row.host_id !== usual.host) line.host = hostLabel(row.host_id);
	return line.project || line.host ? line : null;
}
