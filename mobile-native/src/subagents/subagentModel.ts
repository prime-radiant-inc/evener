// The Subagents list (spec 9) as pure functions over the activity tree the
// hub already serves (evener/jobs/list, parsed by the shared ActivityList):
// one flat row per subagent, its state, where it sits, the tallies behind the
// strip and the filter chips, and the words on its row. Where the spec wants
// a fact the tree doesn't carry, the fallback lives here. S3 (whole-tree
// tallies) changes how the phone counts; the rows stay.
import {
	type ActivityDelegate,
	type ActivityJob,
	type ActivitySessionNode,
	type ActivityTree,
	delegateEndingText,
	delegateHasActiveWork,
	delegateModel,
	delegateTiming,
	firstLine,
	formatTokenCount,
	isActivityFailure,
	isFailedDelegateOutcome,
	isTurnContainer,
	jobIsFailed,
	jobStatusDisplay,
	plainQuoteLine,
} from "@evener/appwire-client";
import { compactDuration, spokenDuration } from "../session/format";
import type { SubagentTally } from "../session/sessionState";

export type SubagentState = "running" | "failed" | "done";

export interface SubagentRow {
	kind: "subagent";
	/** The delegate id: stable across reads; the row's key and its stop request's key. */
	id: string;
	/** The subagent's own session ref, which its screen opens (Task 8). */
	ref: string;
	title: string;
	state: SubagentState;
	/** Done because it was stopped or cancelled, not because it finished its work. */
	stopped: boolean;
	/** It, or a subagent it started, is still working: something a stop request
	 * can stop. PR 3's "Ask coordinator to stop it" reads this. */
	active: boolean;
	/** The title of the subagent that started this one; absent for the coordinator's own. */
	parentTitle?: string;
	delegate: ActivityDelegate;
	/** Its place in the tree's depth-first walk: the last tiebreak, so rows never shuffle. */
	order: number;
}

const STATE_WORDS: Record<SubagentState, string> = { running: "Running", failed: "Failed", done: "Done" };

// A parent's stop settles a run or a command as stopped, and the user's as
// cancelled (agent/internal/delegatestore and jobstore record.go).
const STOPPED_STATUSES: ReadonlySet<string> = new Set(["stopped", "cancelled"]);

/** True when a stop, the parent's or the user's, is what ended a run or a
 * command. */
export function isStoppedStatus(status: string | undefined): boolean {
	return STOPPED_STATUSES.has(status ?? "");
}

/** The delegate fields the state rule reads. The Subagents list passes its
 * ActivityDelegate (turns included); the Session chip passes an
 * EvenerDelegateInfo, which is always the stable "delegate" shape and so
 * carries no turns. `subagentState` is the one classifier for both. */
export type SubagentStateSource = Pick<ActivityDelegate, "type" | "terminal" | "outcome" | "status" | "turns">;

/** Running, failed or done, as the hub's job counts are (active, failed,
 * completed; agent/jobs_activity.go aggregateActivity): the subagent's own
 * outcome, never its children's (ruling 4). A stable delegate runs until its
 * run is terminal; a turn container (the wire allows one, though the daemon
 * builds none today) is read by its turns. */
export function subagentState(delegate: SubagentStateSource): SubagentState {
	if (isTurnContainer(delegate)) {
		const turns = delegate.turns ?? [];
		if (turns.some((turn) => !turn.terminal)) return "running";
		return turns.some(jobIsFailed) ? "failed" : "done";
	}
	if (delegate.terminal !== true) return "running";
	// The daemon sets an outcome with every terminal run; a record with only a
	// status still reads by that status.
	const failed =
		delegate.outcome === undefined
			? isActivityFailure(undefined, delegate.status)
			: isFailedDelegateOutcome(delegate.outcome);
	return failed ? "failed" : "done";
}

/** A subagent that is done because a stop ended it, which says Stopped (its
 * outcome, or else its status, names the stop). The one rule for the
 * Subagents list's rows and the transcript's subagent row. */
export function endedInStop(delegate: SubagentStateSource): boolean {
	return subagentState(delegate) === "done" && isStoppedStatus(delegate.outcome ?? delegate.status);
}

export function subagentStateWord(state: SubagentState): string {
	return STATE_WORDS[state];
}

/** How many runs in the subagent's subtree, its own included, ended in a
 * stop: its subagents' runs and the commands they ran. For PR 3's stop
 * request ("Stopped at your request"). Its own run counts by `endedInStop`,
 * the rule its row's "Stopped" reads (ruling 4). */
export function subtreeStops(delegate: ActivityDelegate): number {
	const own = endedInStop(delegate) ? 1 : 0;
	return (delegate.child?.entries ?? []).reduce((count, entry) => {
		if (entry.kind === "delegate") return count + subtreeStops(entry.delegate);
		return count + (entry.job.terminal && isStoppedStatus(entry.job.status) ? 1 : 0);
	}, own);
}

/** The subagent ended in a stop: its own run, or a run somewhere under it. */
export function subtreeStopped(delegate: ActivityDelegate): boolean {
	return subtreeStops(delegate) > 0;
}

/** The short description (spec 9's "mandate"; the wire's `mandate` is the
 * whole brief, ruling 3), else the brief's first line, else its session. */
export function subagentTitle(delegate: ActivityDelegate): string {
	return (
		delegate.description?.trim() ||
		firstLine(delegate.mandate ?? delegate.task ?? "", 80) ||
		delegate.child?.label.trim() ||
		delegate.childRef
	);
}

/** A shell job in the Activity list: a command a session or subagent ran,
 * as the tree carries it (Jesse's ruling: shell jobs join spec 9's list). */
export interface ShellJobRow {
	kind: "job";
	/** The job id: stable across reads, and the row's key. */
	id: string;
	/** Its description, else its command's first line. */
	title: string;
	/** Who started it: its session's title, or the subagent's. */
	owner: string;
	state: SubagentState;
	job: ActivityJob;
	/** Its place in the tree's depth-first walk, shared with the subagents. */
	order: number;
}

/** A row of the Activity list. */
export type ActivityListRow = SubagentRow | ShellJobRow;

function shellJobState(job: ActivityJob): SubagentState {
	if (jobIsFailed(job)) return "failed";
	return job.terminal ? "done" : "running";
}

/** Every subagent and shell job in the tree, depth first in the tree's own
 * order, each once; a subagent another subagent started names its parent,
 * and a job names the session or subagent that ran it. */
export function flattenActivity(tree: ActivityTree): { subagents: SubagentRow[]; jobs: ShellJobRow[] } {
	const subagents: SubagentRow[] = [];
	const jobs: ShellJobRow[] = [];
	const seen = new Set<string>();
	let order = 0;
	// parentTitle is the subagent whose session this is, absent at the root.
	const visit = (session: ActivitySessionNode, parentTitle: string | undefined) => {
		for (const entry of session.entries) {
			if (entry.kind === "shell") {
				if (seen.has(entry.job.jobId)) continue;
				seen.add(entry.job.jobId);
				const job = entry.job;
				jobs.push({
					kind: "job",
					id: job.jobId,
					title: job.description.trim() || firstLine(job.command ?? "", 80) || job.jobId,
					owner: parentTitle ?? tree.root.label,
					state: shellJobState(job),
					job,
					order: order++,
				});
				continue;
			}
			if (entry.kind !== "delegate" || seen.has(entry.delegate.delegateId)) continue;
			const delegate = entry.delegate;
			seen.add(delegate.delegateId);
			const title = subagentTitle(delegate);
			subagents.push({
				kind: "subagent",
				id: delegate.delegateId,
				ref: delegate.childRef,
				title,
				state: subagentState(delegate),
				stopped: endedInStop(delegate),
				active: delegateHasActiveWork(delegate),
				...(parentTitle === undefined ? {} : { parentTitle }),
				delegate,
				order: order++,
			});
			if (delegate.child) visit(delegate.child, title);
		}
	};
	visit(tree.root, undefined);
	return { subagents, jobs };
}

/** Every subagent in the tree (flattenActivity's), for the views that count
 * subagents alone: the strip, the Session's chip, stop requests. */
export function flattenSubagents(tree: ActivityTree): SubagentRow[] {
	return flattenActivity(tree).subagents;
}

function time(value: string | undefined): number | null {
	if (!value) return null;
	const parsed = Date.parse(value);
	return Number.isFinite(parsed) ? parsed : null;
}

/** When the row entered its state: started (running) or ended. */
function enteredAt(row: ActivityListRow): number | null {
	if (row.kind === "job") return time(row.state === "running" ? row.job.startedAt : row.job.endedAt);
	return time(row.state === "running" ? row.delegate.runStartedAt : row.delegate.runEndedAt);
}

/** Bare time in the current state (spec 9, ruling 7): how long a running
 * subagent has run, how long since one failed or finished. */
export function timeInState(row: SubagentRow, now: number): number | null {
	if (row.state === "running") return delegateTiming(row.delegate, now).durationMs ?? null;
	const ended = time(row.delegate.runEndedAt);
	return ended === null ? null : Math.max(0, now - ended);
}

export interface SubagentSections<Row extends ActivityListRow = SubagentRow> {
	failed: Row[];
	running: Row[];
	done: Row[];
}

function newestFirst(a: ActivityListRow, b: ActivityListRow): number {
	const difference = (enteredAt(b) ?? Number.NEGATIVE_INFINITY) - (enteredAt(a) ?? Number.NEGATIVE_INFINITY);
	return (Number.isNaN(difference) ? 0 : difference) || a.order - b.order;
}

/** Failed, then running, then done, each newest first by when it entered
 * that state (spec 9 gives running this order; ruling 5 extends it). */
export function subagentSections<Row extends ActivityListRow>(rows: readonly Row[]): SubagentSections<Row> {
	const sections: SubagentSections<Row> = { failed: [], running: [], done: [] };
	for (const row of rows) sections[row.state].push(row);
	sections.failed.sort(newestFirst);
	sections.running.sort(newestFirst);
	sections.done.sort(newestFirst);
	return sections;
}

/** The loaded subagents by state: S3's fallback until the hub counts whole
 * trees for the phone. The type is the Session's Subagents chip's (phase 3). */
export function tallySubagents(rows: readonly { state: SubagentState }[]): SubagentTally {
	const tally: SubagentTally = { total: rows.length, running: 0, failed: 0, done: 0 };
	for (const row of rows) tally[row.state] += 1;
	return tally;
}

/** "55", or "55+" while part of the tree couldn't be listed and so couldn't
 * be counted (ruling 2). */
export function countLabel(count: number, partial: boolean): string {
	return partial ? `${count}+` : String(count);
}

export interface StripSegment {
	state: SubagentState;
	width: number;
}

/** The order states take everywhere: the strip, the list and the chips. */
export const STATE_ORDER: readonly SubagentState[] = ["failed", "running", "done"];
const STRIP_MIN: Record<SubagentState, number> = { failed: 3, running: 1, done: 0 };

/** The strip's segments in the list's own order, sized by count (spec 9):
 * failures never thinner than 3pt, so 2 of 55 still shows; running never
 * thinner than 1pt; done takes the rest. No strip once nothing is running or
 * failed. `gap` is the space between segments. */
export function stripSegments(
	tally: Pick<SubagentTally, "failed" | "running" | "done">,
	width: number,
	gap = 1,
): StripSegment[] {
	if (tally.failed === 0 && tally.running === 0) return [];
	const present = STATE_ORDER.filter((state) => tally[state] > 0);
	const available = Math.max(0, width - gap * (present.length - 1));
	const total = present.reduce((sum, state) => sum + tally[state], 0);
	const floored = new Set(present.filter((state) => (available * tally[state]) / total < STRIP_MIN[state]));
	const reserved = [...floored].reduce((sum, state) => sum + STRIP_MIN[state], 0);
	const rest = present.filter((state) => !floored.has(state)).reduce((sum, state) => sum + tally[state], 0);
	return present.map((state) => ({
		state,
		width: floored.has(state) ? STRIP_MIN[state] : ((available - reserved) * tally[state]) / rest,
	}));
}

export interface SubagentWhy {
	/** "Failed", semibold in the danger ink; only the word takes the hue. */
	word?: "Failed";
	text: string;
}

const QUIET_AFTER_MS = 3 * 60_000;

function runningCommand(session: ActivitySessionNode | undefined): string | undefined {
	for (const entry of session?.entries ?? [])
		if (entry.kind === "shell" && !entry.job.terminal && entry.job.command) return firstLine(entry.job.command, 80);
	return undefined;
}

/** The latest activity or outcome (spec 9) from what the tree carries
 * (ruling 6). */
export function subagentWhy(row: SubagentRow, now: number): SubagentWhy {
	const delegate = row.delegate;
	if (row.state === "failed") return { word: "Failed", text: delegateEndingText(delegate) ?? "" };
	if (row.state === "done") {
		if (row.stopped) return { text: "Stopped" };
		const report = typeof delegate.message === "string" ? firstLine(plainQuoteLine(delegate.message), 120) : "";
		return { text: report || "Finished" };
	}
	const command = runningCommand(delegate.child);
	if (command) return { text: `Running ${command}` };
	const waiting = (delegate.child?.entries ?? []).filter(
		(entry) => entry.kind === "delegate" && delegateHasActiveWork(entry.delegate),
	).length;
	if (waiting > 0) return { text: `Waiting on ${waiting} ${waiting === 1 ? "subagent" : "subagents"}` };
	const quiet = delegateTiming(delegate, now).quietForMs;
	if (quiet !== undefined && quiet >= QUIET_AFTER_MS) return { text: `Quiet ${compactDuration(quiet)}` };
	return { text: "Working" };
}

// Each tree's rows by id, built once however many transcript rows ask of it.
const rowsByTree = new WeakMap<ActivityTree, Map<string, SubagentRow>>();

/** A finished subagent's outcome line from the coordinator's tree, as the
 * Subagents list gives it (subagentWhy): its report's opening line, or
 * "Finished" or "Stopped". Undefined while the tree doesn't show it done. */
export function subagentOutcome(tree: ActivityTree, delegateId: string, now: number): string | undefined {
	let rows = rowsByTree.get(tree);
	if (!rows) {
		rows = new Map(flattenSubagents(tree).map((row) => [row.id, row]));
		rowsByTree.set(tree, rows);
	}
	const row = rows.get(delegateId);
	return row?.state === "done" ? subagentWhy(row, now).text : undefined;
}

export interface SubagentLastLine {
	/** Who started it, for a subagent another subagent started. */
	parent?: string;
	/** The model's display name, only when it differs from the coordinator's. */
	model?: string;
	/** Its own worktree's branch, in the machine face. */
	branch?: string;
	tokens?: string;
}

/** Two names name one model when their model parts match: the coordinator's
 * modelProvider can carry a "provider/" prefix a delegate's resolved model
 * doesn't. */
export function sameModel(a: string, b: string): boolean {
	const part = (value: string) =>
		value
			.slice(value.lastIndexOf("/") + 1)
			.trim()
			.toLowerCase();
	return part(a) === part(b);
}

export function subagentLastLine(
	row: SubagentRow,
	coordinatorModel: string | null,
	modelName: (model: string) => string,
): SubagentLastLine | null {
	const line: SubagentLastLine = {};
	if (row.parentTitle) line.parent = row.parentTitle;
	const model = delegateModel(row.delegate).model;
	if (model && coordinatorModel && !sameModel(model, coordinatorModel)) line.model = modelName(model);
	const branch = row.delegate.worktree?.branch.trim();
	if (branch) line.branch = branch;
	const usage = row.delegate.usage;
	if (usage) line.tokens = `${formatTokenCount(usage.totalTokens ?? usage.inputTokens + usage.outputTokens)} tokens`;
	return Object.keys(line).length > 0 ? line : null;
}

/** The Activity list offers its search field past this many rows,
 * subagents and shell jobs together (ruling 8). */
export const SEARCH_AFTER = 8;

/** The search field's filter (spec 9): the title, ignoring case, and for a
 * shell job its command and who started it too. */
export function matchesSearch(row: ActivityListRow, query: string): boolean {
	const needle = query.trim().toLowerCase();
	if (needle === "") return true;
	const words = row.kind === "job" ? [row.title, row.job.command ?? "", row.owner] : [row.title];
	return words.some((text) => text.toLowerCase().includes(needle));
}

/** How an ended shell job ended, in words: "Command failed" or "Command
 * killed" (jobStatusDisplay), "Stopped" or "Cancelled", else its state's word
 * ("Failed", "Done"). */
export function shellJobEnding(row: ShellJobRow): string {
	const { status, reason } = row.job;
	const display = jobStatusDisplay(status, reason);
	if (display !== status) return display;
	if (status === "stopped") return "Stopped";
	if (status === "cancelled") return "Cancelled";
	return subagentStateWord(row.state);
}

/** A shell job's status in parts, which its meta and its spoken label each
 * word their own way: while it runs, its status (jobStatusDisplay) and how
 * long it has been quiet; once it ends, how it ended and how long it ran.
 * `clean` marks a job that finished well, whose ending the meta leaves to
 * its hue. */
function shellJobStatus(row: ShellJobRow, now: number): { words: string; clean: boolean; ms: number | null } {
	const { job } = row;
	if (!job.terminal) {
		const since = time(job.lastOutputAt) ?? time(job.startedAt);
		return {
			words: jobStatusDisplay(job.status, job.reason),
			clean: false,
			ms: since === null ? null : Math.max(0, now - since),
		};
	}
	const words = shellJobEnding(row);
	const started = time(job.startedAt);
	const ended = time(job.endedAt);
	return {
		words,
		clean: words === subagentStateWord("done"),
		ms: started === null || ended === null ? null : Math.max(0, ended - started),
	};
}

/** A shell job's trailing words (the web's ActivityTree meta): "running ·
 * 2m", "Command failed · 1m", or for a job that finished well just "1m". */
export function shellJobMeta(row: ShellJobRow, now: number): string {
	const { words, clean, ms } = shellJobStatus(row, now);
	if (ms === null) return words;
	return clean ? compactDuration(ms) : `${words} · ${compactDuration(ms)}`;
}

/** A shell job's row as VoiceOver reads it: what it is, how it's doing or
 * how it ended (a clean finish too), the time in words, and who started it. */
export function shellJobLabel(row: ShellJobRow, now: number): string {
	const { words, ms } = shellJobStatus(row, now);
	return ["Shell job", row.title, words, ...(ms === null ? [] : [spokenDuration(ms)]), `under ${row.owner}`].join(", ");
}
