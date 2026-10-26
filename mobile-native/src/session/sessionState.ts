// What the Session's header says: the nav bar's state line with its still
// mark (spec 8.1), and the context chips under it. The mark reuses the
// Board's states (src/board/attention.ts). A session you are looking at is
// never unread, so one whose turn ended is Idle, with no dot.
import { summaryTally } from "../subagents/subagentModel";
import type { ThreadModel, SessionActivityCounts } from "@evener/appwire-client";
import { compactDuration, taskSettledCount } from "@evener/appwire-client";
import { type BoardState, hubTime } from "../board/attention";
import { timeAgo } from "./format";
import type { TrayLine } from "./trayLine";

export interface SessionStateLine {
	state: BoardState;
	text: string;
	/** While the agent works, what it is doing now, which the title draws as
	 * the meter's companion: the running turn's live line, the same words the
	 * tray used to show. Absent on every other state, and while the tray has
	 * no line. */
	live?: TrayLine;
}

export type StateSource = Pick<
	ThreadModel,
	"status" | "askPending" | "pendingEscalations" | "activeTurnStartedAt" | "turns"
>;

/** The statuses `sessionStateLine` reads as "Shut down" - also the ones a
 * session can't be shut down FROM again, since it already is. */
export const SHUT_DOWN = new Set(["notLoaded", "closed", "ended"]);

/** `runMs` is a subagent's time in its run, as its Subagents row shows it:
 * its screen says "Working" with that number rather than its current turn's,
 * so a subagent has one time everywhere. `live` is the running turn's own
 * line (trayLine over the same source), shown in Working's place with the
 * session's elapsed time kept after it. */
export function sessionStateLine(
	session: StateSource,
	now: number,
	runMs?: number | null,
	live?: TrayLine | null,
): SessionStateLine {
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
	// Awaiting without a question: the turn ended on needs_response (#4093).
	if (type === "awaiting") return { state: "needsYou", text: "Needs you" };
	if (type === "active") {
		const started = hubTime(session.activeTurnStartedAt);
		// The running-turn elapsed, which the session's own clock reports: the
		// subagent run's time when the screen has one, else the time since the
		// turn started.
		const elapsed = typeof runMs === "number" ? runMs : started === null ? null : now - started;
		if (live) {
			// A line that already carries its own clock keeps it alone.
			if (live.timed || elapsed === null) return { state: "working", text: live.text, live };
			return {
				state: "working",
				text: `${live.text} · ${compactDuration(elapsed)}`,
				live,
			};
		}
		return { state: "working", text: elapsed === null ? "Working" : `Working · ${compactDuration(elapsed)}` };
	}
	const ended = lastCompletion(session.turns);
	return {
		state: "idle",
		text: ended === null ? "Idle" : `Idle · ${timeAgo(now - ended)}`,
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

export type ChipKind = "subagents" | "files" | "tasks" | "goal" | "queue";

export interface ContextChip {
	kind: ChipKind;
	label: string;
	/** Amber: the goal is blocked. */
	attention: boolean;
	/** A blue dot after the label: a document is new or changed (Files). */
	dot?: boolean;
	accessibilityLabel: string;
}

/** The chips under the nav bar, each only when it has content (spec 8.1).
 * Agents and Tasks open live views (the Agents list, the Tasks sheet),
 * so they hide while disconnected rather than looking tappable and doing
 * nothing (Calm). Files opens the documents the session wrote or linked, as
 * the screen last read them; Goal (opens the local Session sheet) and Queue
 * (a local toggle) need no connection either, and all three always show when
 * they have content. `files` counts the session's documents, and `fresh`
 * says one is new or changed since you last opened it. `subagents` supplies
 * authoritative summary counts; a transcript roster is never a count source,
 * only evidence that subagents exist while the count isn't known. A shut-down
 * session's count can stay unknown for good, so the Agents chip shows no
 * count until the summary knows it. */
export function contextChips(
	session: Pick<ThreadModel, "delegates" | "tasks" | "goal" | "queue">,
	connected: boolean,
	files: { count: number; fresh: boolean } = { count: 0, fresh: false },
	subagents: SessionActivityCounts | null = null,
): ContextChip[] {
	const chips: ContextChip[] = [];
	const tally = summaryTally(subagents ?? undefined);
	if (connected && tally && tally.total > 0) {
		chips.push({
			kind: "subagents",
			label: `Agents ${tally.total}`,
			attention: false,
			accessibilityLabel: `Agents, ${tally.total}`,
		});
	} else if (connected && !tally && (session.delegates?.length ?? 0) > 0) {
		chips.push({
			kind: "subagents",
			label: "Agents",
			attention: false,
			accessibilityLabel: "Agents, count unknown",
		});
	}
	if (files.count > 0)
		chips.push({
			kind: "files",
			label: `Files ${files.count}`,
			attention: false,
			dot: files.fresh,
			accessibilityLabel: `Files, ${files.count}${files.fresh ? ", new or changed" : ""}`,
		});
	const tasks = session.tasks;
	// The Tasks sheet's groups and the web's activity chips read the same
	// count, so the chip must too.
	if (connected && tasks && tasks.total > 0) {
		const settled = taskSettledCount(tasks);
		chips.push({
			kind: "tasks",
			label: `Tasks ${settled}/${tasks.total}`,
			attention: false,
			accessibilityLabel: `Tasks, ${settled} of ${tasks.total} settled`,
		});
	}
	if (session.goal) {
		const blocked = session.goal.status === "blocked";
		chips.push({
			kind: "goal",
			label: "Goal",
			attention: blocked,
			accessibilityLabel: blocked ? "Goal, blocked" : "Goal",
		});
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
