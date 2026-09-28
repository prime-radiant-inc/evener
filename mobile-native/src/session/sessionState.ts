// What the Session's header says: the nav bar's state line with its still
// mark (spec 8.1), and the context chips under it. The mark reuses the
// Board's states (src/board/attention.ts). A session you are looking at is
// never unread, so a finished one is Idle, with no dot.
import type { EvenerDelegateInfo, ThreadModel } from "@evener/appwire-client";
import { projectDelegateEntry } from "../../../mobile/src/services/activity";
import { type BoardState, hubTime } from "../board/attention";
import { compactDuration } from "./format";

export interface SessionStateLine {
	state: BoardState;
	text: string;
}

export type StateSource = Pick<
	ThreadModel,
	"status" | "askPending" | "pendingEscalations" | "activeTurnStartedAt" | "turns"
>;

/** The statuses `sessionStateLine` reads as "Shut down" - also the ones a
 * session can't be shut down FROM again, since it already is. */
export const SHUT_DOWN = new Set(["notLoaded", "closed", "ended"]);

export function sessionStateLine(session: StateSource, now: number): SessionStateLine {
	const type = session.status.type;
	// This follows the Board's own attention order (board/attention.ts's
	// boardState, which matches the hub's NeedsYouBand) exactly, so the two
	// surfaces never disagree about the same session: failed, restart needed,
	// warning and "already shut down" are checked before a pending question
	// or approval, so a stale escalation on any of those never masks them.
	if (type === "systemError") return { state: "failed", text: "Failed" };
	if (type === "restartRequired") return { state: "restartNeeded", text: "Restart needed" };
	if (type === "warning") return { state: "warning", text: "Warning" };
	if (SHUT_DOWN.has(type)) return { state: "shutDown", text: "Shut down" };
	// A question outranks an approval, as on the Board.
	if (type === "awaiting" && session.askPending) return { state: "question", text: "Asks a question" };
	if (session.pendingEscalations.length > 0) return { state: "approval", text: "Asks for approval" };
	if (type === "active") {
		const started = hubTime(session.activeTurnStartedAt);
		return { state: "working", text: started === null ? "Working" : `Working · ${compactDuration(now - started)}` };
	}
	const finished = lastCompletion(session.turns);
	return {
		state: "idle",
		text: finished === null ? "Finished" : `Finished · ${compactDuration(now - finished)} ago`,
	};
}

function lastCompletion(turns: StateSource["turns"]): number | null {
	for (let index = turns.length - 1; index >= 0; index -= 1) {
		const time = hubTime(turns[index]?.completedAt);
		if (time !== null) return time;
	}
	return null;
}

export interface SubagentTally {
	total: number;
	running: number;
	failed: number;
	done: number;
}

/** Running, failed or done, as the hub's job counts are (spec 9), over the
 * subagents this session has loaded: S3's fallback until the hub tallies the
 * whole tree. */
export function subagentTally(delegates: readonly EvenerDelegateInfo[] | undefined): SubagentTally {
	const tally: SubagentTally = { total: 0, running: 0, failed: 0, done: 0 };
	for (const delegate of delegates ?? []) {
		tally.total += 1;
		const tone = projectDelegateEntry(delegate).tone;
		if (tone === "running") tally.running += 1;
		else if (tone === "failed") tally.failed += 1;
		else tally.done += 1;
	}
	return tally;
}

export type ChipKind = "subagents" | "tasks" | "goal" | "queue";

export interface ContextChip {
	kind: ChipKind;
	label: string;
	/** "2 failed", drawn in red ink after the label. */
	failed?: string;
	/** Amber: the goal is blocked. */
	attention: boolean;
	accessibilityLabel: string;
}

/** The chips under the nav bar, each only when it has content (spec 8.1).
 * Files waits for phase 4's Reader (ruling 6). Subagents and Tasks open a
 * live sheet (today's ActivitySheet/TasksSheet), so they hide while
 * disconnected rather than looking tappable and doing nothing (Calm); Goal
 * (opens the local Session sheet) and Queue (a local toggle) need no
 * connection and always show when they have content. */
export function contextChips(
	session: Pick<ThreadModel, "delegates" | "tasks" | "goal" | "queue">,
	connected: boolean,
): ContextChip[] {
	const chips: ContextChip[] = [];
	const subagents = subagentTally(session.delegates);
	if (connected && subagents.total > 0) {
		const failed = subagents.failed > 0 ? `${subagents.failed} failed` : undefined;
		chips.push({
			kind: "subagents",
			label: `Subagents ${subagents.total}`,
			failed,
			attention: false,
			accessibilityLabel: failed ? `Subagents, ${subagents.total}, ${failed}` : `Subagents, ${subagents.total}`,
		});
	}
	const tasks = session.tasks;
	if (connected && tasks && tasks.total > 0)
		chips.push({
			kind: "tasks",
			label: `Tasks ${tasks.done}/${tasks.total}`,
			attention: false,
			accessibilityLabel: `Tasks, ${tasks.done} of ${tasks.total} done`,
		});
	if (session.goal) {
		const blocked = session.goal.status === "blocked";
		chips.push({ kind: "goal", label: "Goal", attention: blocked, accessibilityLabel: blocked ? "Goal, blocked" : "Goal" });
	}
	const depth = session.queue?.depth ?? 0;
	if (depth > 0)
		chips.push({
			kind: "queue",
			label: `Queue ${depth}`,
			attention: false,
			accessibilityLabel: `${depth} queued ${depth === 1 ? "message" : "messages"}`,
		});
	return chips;
}
