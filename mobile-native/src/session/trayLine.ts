// The status tray's one line while the agent works (spec 8.3), and the counts
// behind its pulse meter (spec 16.4). Built only from what the session
// already holds: the running turn's items, the running subagents, the
// model's retry state, and lastFrameAt, which the package's reducer restamps
// on every streamed frame (appwire-client/typescript/model.ts).
import {
	type ItemModel,
	isActiveItem,
	formatTokenCount,
	type ModelRetryState,
	pendingTextJoined,
	type ThreadModel,
	toolStepProgress,
} from "@evener/appwire-client";
import { subagentState } from "../subagents/subagentModel";
import { PULSE_BARS } from "../board/pulse";
import { compactDuration } from "./format";

/** No frame for this long reads "Quiet": the web's threshold
 * (cmd/evener-hub/frontend/src/panes/session/transcript/flow/liveness.ts). */
export const QUIET_AFTER_MS = 20_000;
/** No frame for this long reads "May be stuck" (spec 13.1). */
export const STUCK_AFTER_MS = 10 * 60_000;

export interface TrayLine {
	text: string;
	/** Amber ink: the agent may be stuck. */
	attention: boolean;
}

export type TraySource = Pick<
	ThreadModel,
	"status" | "turns" | "activeTurnId" | "runningTurnId" | "delegates" | "modelRetry" | "lastFrameAt"
> & {
	/** The session's directory, which a running command's leading cd to it
	 * only repeats. */
	cwd?: string;
};

interface Step {
	text: string;
	startedAt?: number;
	waitsOnSubagents: boolean;
}

// An intent-less delegating or job step falls back to what it waits on.
const WAITING_TOOLS = new Set(["delegate", "delegate_send", "job_watch", "job_status", "job_list"]);

export function trayLine(session: TraySource, now: number): TrayLine | null {
	if (session.status.type !== "active") return null;
	const intent = latestToolIntent(session);
	if (intent) return { text: intent, attention: false };
	const silence = now - session.lastFrameAt;
	// A retry the hub reported explains the silence (modelRetry deliberately
	// leaves lastFrameAt alone, model.ts), so it wins, as on the web
	// (liveness.ts's describeLiveness). Past ten minutes the line adds the
	// silence and turns amber, as the web's stalled level does. A first
	// attempt is gated behind the ordinary quiet threshold too (the web's
	// retryKnownEarly = retry.attempt >= 2): a retry that resolves on its
	// next try shouldn't flash across the tray, but from the second attempt
	// on, the retry is known information and always wins.
	if (session.modelRetry && (silence >= QUIET_AFTER_MS || session.modelRetry.attempt >= 2)) {
		const retry = retryText(session.modelRetry);
		return silence >= STUCK_AFTER_MS
			? { text: `${retry} · no updates for ${compactDuration(silence)}`, attention: true }
			: { text: retry, attention: false };
	}
	const running = runningSubagents(session);
	// An agent waiting on subagents is never stuck (Jesse's ruling on S5): a
	// subagent inside one long model call sends nothing for minutes. Quiet
	// below is unreachable while one runs, since every path with a running
	// subagent returns first.
	if (running === 0 && silence >= STUCK_AFTER_MS)
		return { text: `May be stuck · no updates for ${compactDuration(silence)}`, attention: true };
	const step = currentStep(session);
	if (running > 0 && (!step || step.waitsOnSubagents))
		return { text: `Waiting on ${running} ${running === 1 ? "subagent" : "subagents"}`, attention: false };
	if (step)
		return {
			// The hub's startedAt against this phone's clock: a small skew is
			// tolerable in a running clock, and a negative one reads 0s.
			text: step.startedAt === undefined ? step.text : `${step.text} · ${compactDuration(now - step.startedAt)}`,
			attention: false,
		};
	if (silence >= QUIET_AFTER_MS) return { text: `Quiet ${compactDuration(silence)}`, attention: false };
	return { text: "Working", attention: false };
}

function retryText(retry: ModelRetryState): string {
	const cause = retry.errorClass === "rate_limit" ? "rate limited" : "provider error";
	// A hub too old to send attemptCap reports 0; no denominator beats a false one.
	const attempt = retry.attemptCap > 0 ? `attempt ${retry.attempt} of ${retry.attemptCap}` : `attempt ${retry.attempt}`;
	return `Retrying · ${cause} · ${attempt}`;
}

function runningTurn(session: TraySource) {
	return session.turns.find((turn) => turn.id === session.runningTurnId);
}

function latestToolIntent(session: TraySource): string | null {
	const turn = runningTurn(session);
	if (!turn) return null;
	for (let index = turn.items.length - 1; index >= 0; index -= 1) {
		const item = turn.items[index];
		if (item?.type !== "commandExecution") continue;
		const intent = item.description;
		if (intent?.trim()) return intent;
	}
	return null;
}

function currentTurn(session: TraySource) {
	if (session.runningTurnId !== undefined) return runningTurn(session);
	return session.turns.find((turn) => turn.id === session.activeTurnId) ?? session.turns.at(-1);
}

function currentStep(session: TraySource): Step | null {
	const turn = currentTurn(session);
	if (!turn) return null;
	for (let index = turn.items.length - 1; index >= 0; index -= 1) {
		const item = turn.items[index];
		if (!item || !isActiveItem(item, turn.status)) continue;
		const step = stepFor(item, session.cwd);
		if (step) return step;
	}
	return null;
}

function stepFor(item: ItemModel, cwd: string | undefined): Step | null {
	if (item.type === "reasoning") {
		const tokens = thinkingTokens(item);
		return {
			text: tokens > 0 ? `Thinking… · ${formatTokenCount(tokens)} tokens` : "Thinking…",
			waitsOnSubagents: false,
		};
	}
	if (item.type === "agentMessage") return { text: "Writing…", waitsOnSubagents: false };
	if (item.type !== "commandExecution") return null;
	return {
		text: toolStepProgress(item, { cwd }),
		startedAt: timeOf(item.startedAt),
		waitsOnSubagents: WAITING_TOOLS.has(item.toolName ?? ""),
	};
}

// Characters over four, as the web's thinking estimate does (ThinkBlock.tsx):
// a count, never the thought itself. This runs on every render while the
// agent thinks, so it reads lengths through pendingTextJoined, which answers
// a streaming chunk view in O(1) instead of walking every chunk.
function thinkingTokens(item: ItemModel): number {
	const summaries = (item.reasoningSummaries ?? []).reduce(
		(total, chunks) => total + pendingTextJoined(chunks).length,
		0,
	);
	const text = item.text.length + (item.pendingText ? pendingTextJoined(item.pendingText).length : 0);
	return Math.round(Math.max(summaries, text) / 4);
}

function runningSubagents(session: TraySource): number {
	return (session.delegates ?? []).filter((delegate) => subagentState(delegate) === "running").length;
}

function timeOf(value: string | undefined): number | undefined {
	if (!value) return undefined;
	const time = Date.parse(value);
	return Number.isFinite(time) ? time : undefined;
}

/** The frames this phone saw each minute while the session was open: the
 * tray's meter (spec 16.4). A frame is a change to lastFrameAt. Minutes
 * before the screen opened read as empty, which is S5's fallback until the
 * hub reports activity buckets. */
export class FrameCounter {
	private counts = new Map<number, number>();

	record(at: number): void {
		const minute = Math.floor(at / 60_000);
		this.counts.set(minute, (this.counts.get(minute) ?? 0) + 1);
		for (const key of this.counts.keys()) if (key <= minute - PULSE_BARS) this.counts.delete(key);
	}

	hasFrames(): boolean {
		return this.counts.size > 0;
	}

	perMinute(now: number): number[] {
		const current = Math.floor(now / 60_000);
		return Array.from({ length: PULSE_BARS }, (_, index) => this.counts.get(current - (PULSE_BARS - 1 - index)) ?? 0);
	}
}
