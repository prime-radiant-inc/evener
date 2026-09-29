// What a subagent's row in the transcript says (spec 8.2, "Subagent"): its
// title, its state and how long it has been in it ("failed · 6m"), and the
// latest activity beneath, which reads the same as in the Subagents list.
// Pure: the row re-renders with the transcript, so no clock of its own.
import { delegateEndingText, delegateTiming, type EvenerDelegateInfo } from "@evener/appwire-client";
import { endedInStop, subagentState } from "../subagents/subagentModel";
import { hubTime } from "../board/attention";
import type { TimelineRow } from "../timeline";
import { compactDuration } from "./format";

type Activity = Extract<TimelineRow, { kind: "activity" }>;

export interface SubagentLine {
	title: string;
	state: "running" | "failed" | "stopped" | "done";
	stateText: string;
	activity?: string;
	/** The subagent's own transcript, which tapping the row opens. */
	ref?: string;
	/** The subagent's id, for its outcome in the coordinator's tree. */
	delegateId?: string;
}

/** A subagent is quiet once no update came for this long (the web's
 * liveness threshold, ruling 10). */
const QUIET_AFTER_MS = 20_000;

// The Subagents list's own rule (subagentState), so this row, the list, the
// Subagents chip and the tray always agree; a subagent a stop ended says
// stopped, as its list row does.
function stateOf(delegate: EvenerDelegateInfo): SubagentLine["state"] {
	return endedInStop(delegate) ? "stopped" : subagentState(delegate);
}

function stateFromRow(row: Activity): SubagentLine["state"] {
	return row.state === "running" ? "running" : row.state === "failed" ? "failed" : "done";
}

/** A subagent's title from its own record: its description, else its task's
 * first line. Undefined when it has neither. */
export function delegateTitle(delegate: EvenerDelegateInfo | undefined): string | undefined {
	const task = delegate?.task?.split("\n")[0]?.trim();
	return delegate?.description?.trim() || task || undefined;
}

function titleOf(row: Activity, delegate: EvenerDelegateInfo | undefined): string {
	return delegateTitle(delegate) || row.detail.description?.trim() || "Subagent";
}

export function subagentLine(
	row: Activity,
	delegates: readonly EvenerDelegateInfo[] | undefined,
	now: number,
): SubagentLine {
	const all = delegates ?? [];
	const delegate = all.find(
		(candidate) =>
			candidate.originItemId === row.id ||
			(row.detail.callId !== undefined && candidate.originToolCallId === row.detail.callId),
	);
	const title = titleOf(row, delegate);
	if (!delegate) {
		const state = stateFromRow(row);
		return { title, state, stateText: state };
	}
	const state = stateOf(delegate);
	// The package's timing reads the clock against the run's start and its
	// last activity, so a running subagent keeps counting between updates.
	const timing = state === "running" ? delegateTiming(delegate, now) : undefined;
	const ended = hubTime(delegate.runEndedAt);
	const since = timing ? timing.durationMs : ended === null ? undefined : now - ended;
	const stateText = since === undefined ? state : `${state} · ${compactDuration(since)}`;
	const line: SubagentLine = { title, state, stateText, ref: delegate.transcriptRef, delegateId: delegate.delegateId };
	if (state === "failed") {
		const ending = delegateEndingText(delegate);
		if (ending) line.activity = ending;
	} else if (state === "done" || state === "stopped") {
		// What the roster knows. The report itself is only in the coordinator's
		// tree (thread/read's roster drops it), which the row reads when it can.
		line.activity = state === "done" ? "Finished" : "Stopped";
	} else if (state === "running") {
		const waitingOn = all.filter(
			(child) => child.parentDelegateId === delegate.delegateId && stateOf(child) === "running",
		).length;
		// Quiet since the later of its last activity and this run's start: a
		// resumed subagent can still carry its last run's activity time.
		const quietFor = timing?.quietForMs ?? 0;
		// An agent waiting on its own subagents is never stuck (ruling 10).
		line.activity =
			waitingOn > 0
				? `Waiting on ${waitingOn} ${waitingOn === 1 ? "subagent" : "subagents"}`
				: quietFor >= QUIET_AFTER_MS
					? `Quiet ${compactDuration(quietFor)}`
					: "Working";
	}
	return line;
}

/** Whether a transcript shows a finished subagent's row: what the screen holds
 * the coordinator's tree for (useTranscriptSubagentTree). */
export function hasFinishedSubagentRow(
	rows: readonly TimelineRow[],
	delegates: readonly EvenerDelegateInfo[] | undefined,
): boolean {
	return rows.some(
		(row) => row.kind === "activity" && row.label === "delegate" && subagentLine(row, delegates, 0).state === "done",
	);
}
