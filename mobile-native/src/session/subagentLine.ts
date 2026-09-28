// What a subagent's row in the transcript says (spec 8.2, "Subagent"): its
// title, its state and how long it has been in it ("failed · 6m"), and the
// latest activity beneath, which reads the same as in the Subagents list.
// Pure: the row re-renders with the transcript, so no clock of its own.
import type { EvenerDelegateInfo } from "@evener/appwire-client";
import { projectDelegateEntry } from "../../../mobile/src/services/activity";
import { hubTime } from "../board/attention";
import type { TimelineRow } from "../timeline";
import { compactDuration } from "./format";

type Activity = Extract<TimelineRow, { kind: "activity" }>;

export interface SubagentLine {
	title: string;
	state: "running" | "failed" | "done";
	stateText: string;
	activity?: string;
	/** The subagent's own transcript, which tapping the row opens. */
	ref?: string;
}

/** A subagent is quiet once no update came for this long (the web's
 * liveness threshold, ruling 10). */
const QUIET_AFTER_MS = 20_000;

// How long since `at`, read against the clock so a running subagent keeps
// counting between updates. The snapshot's own measure (true when the
// delegate last arrived) stands in when the hub sent no time.
function elapsed(at: string | undefined, measured: number | undefined, now: number): number | undefined {
	const since = hubTime(at);
	return since === null ? measured : Math.max(0, now - since);
}

function stateOf(delegate: EvenerDelegateInfo): SubagentLine["state"] {
	const { tone } = projectDelegateEntry(delegate);
	return tone === "running" ? "running" : tone === "failed" ? "failed" : "done";
}

function stateFromRow(row: Activity): SubagentLine["state"] {
	return row.state === "running" ? "running" : row.state === "failed" ? "failed" : "done";
}

function titleOf(row: Activity, delegate: EvenerDelegateInfo | undefined): string {
	const task = delegate?.task?.split("\n")[0]?.trim();
	return delegate?.description?.trim() || task || row.detail.description?.trim() || "Subagent";
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
	const ended = hubTime(delegate.runEndedAt);
	const since =
		state === "running"
			? elapsed(delegate.runStartedAt, delegate.runningForMs, now)
			: ended === null
				? undefined
				: now - ended;
	const stateText = since === undefined ? state : `${state} · ${compactDuration(since)}`;
	const line: SubagentLine = { title, state, stateText, ref: delegate.transcriptRef };
	if (state === "failed") {
		if (delegate.reason) line.activity = delegate.reason;
	} else if (state === "running") {
		const waitingOn = all.filter(
			(child) => child.parentDelegateId === delegate.delegateId && stateOf(child) === "running",
		).length;
		const quietFor = elapsed(delegate.latestActivityAt, delegate.quietForMs, now) ?? 0;
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
