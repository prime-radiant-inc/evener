import type { EvenerDelegateInfo, SandboxEscalationRequested, TurnModel } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { contextChips, sessionStateLine, type StateSource } from "./sessionState";

const counts = (total = 0, failed = 0) => ({ known: true, total, active: total - failed, failed, completed: 0 });
const noFiles = { count: 0, fresh: false };
const NOW = Date.UTC(2026, 8, 26, 15, 0, 0);
const ago = (ms: number) => new Date(NOW - ms).toISOString();
const escalation: SandboxEscalationRequested = {
	threadId: "t",
	ref: "r",
	escalationId: "e",
	mode: "workspace-write",
	tool: "write_file",
	kind: "file_tool",
	deniedPath: "/tmp/x",
};
const session = (type: string, over: Partial<StateSource> = {}): StateSource => ({
	status: { type },
	askPending: false,
	pendingEscalations: [],
	activeTurnStartedAt: undefined,
	turns: [],
	...over,
});
const done = (completedAt: string): TurnModel => ({ id: "turn_1", status: "completed", items: [], completedAt });
const delegate = (
	status: string,
	n: number,
	outcome?: string,
	terminal = outcome !== undefined,
): EvenerDelegateInfo => ({
	delegateId: `d${n}`,
	ownerSessionId: "root",
	rootSessionId: "root",
	childSessionId: `c${n}`,
	transcriptRef: `local:c${n}`,
	type: "delegate",
	lifecycle: status,
	phase: status,
	status,
	resumable: false,
	needsAttention: false,
	projectionRevision: 1,
	...(outcome ? { outcome } : {}),
	...(terminal ? { terminal: true } : {}),
});

describe("the nav bar's state line (spec 8.1, 13.1)", () => {
	it("times a subagent by its run when given one, not its current turn", () => {
		const running = session("active", { activeTurnStartedAt: ago(2 * 60_000) });
		expect(sessionStateLine(running, NOW, 4 * 60_000).text).toBe("Working · 4m");
		expect(sessionStateLine(running, NOW, null).text).toBe("Working · 2m");
	});

	it.each([
		[session("active", { activeTurnStartedAt: ago(38 * 60_000) }), "working", "Working · 38m"],
		[session("active"), "working", "Working"],
		[session("idle", { turns: [done(ago(3_600_000))] }), "idle", "Finished · 1h ago"],
		[session("awaiting", { turns: [done(ago(120_000))] }), "idle", "Finished · 2m ago"],
		[session("idle"), "idle", "Finished"],
		[session("awaiting", { askPending: true }), "question", "Asks a question"],
		[session("active", { pendingEscalations: [escalation] }), "approval", "Asks for approval"],
		[session("awaiting", { askPending: true, pendingEscalations: [escalation] }), "question", "Asks a question"],
		[session("systemError"), "failed", "Failed"],
		[session("restartRequired"), "restartNeeded", "Restart needed"],
		[session("warning"), "warning", "Warning"],
		[session("notLoaded"), "shutDown", "Shut down"],
		[session("closed"), "shutDown", "Shut down"],
		// The Session follows the Board's own attention order (ruling: they
		// can't disagree), which checks warning and "already shut down" before
		// a pending escalation - so a stale escalation on a warning or
		// already-shut-down session never masks its real state.
		[session("warning", { pendingEscalations: [escalation] }), "warning", "Warning"],
		[session("notLoaded", { pendingEscalations: [escalation] }), "shutDown", "Shut down"],
		[session("closed", { pendingEscalations: [escalation] }), "shutDown", "Shut down"],
		[session("ended", { pendingEscalations: [escalation] }), "shutDown", "Shut down"],
	] as const)("%#: %s", (input, state, text) => {
		expect(sessionStateLine(input, NOW)).toEqual({ state, text });
	});
});

describe("the context chips (spec 8.1)", () => {
	it("appear only with content", () => {
		expect(contextChips({ delegates: [], tasks: null, goal: null, queue: null }, true, noFiles, counts())).toEqual([]);
	});

	it("count subagents the way the Subagents list does, with failures in their own part", () => {
		// A stopped or cancelled delegate finished, not failed; a delegate still
		// retrying after exhaustion (not terminal) is running. The list says so
		// (subagentState), and the chip must show the same numbers (issue #2684).
		const [chip] = contextChips(
			{
				delegates: [delegate("running", 1), delegate("idle", 2, "failed")],
				tasks: null,
				goal: null,
				queue: null,
			},
			true,
			noFiles,
			counts(2, 1),
		);
		expect(chip).toEqual({
			kind: "subagents",
			label: "Subagents 2",
			failed: "1 failed",
			attention: false,
			accessibilityLabel: "Subagents, 2, 1 failed",
		});
	});

	it("show tasks done of total, the goal (amber when blocked), and the queue", () => {
		const chips = contextChips(
			{
				delegates: undefined,
				tasks: { total: 7, done: 3 },
				goal: { objective: "Ship it", status: "blocked", iterations: 2 },
				queue: { revision: 1, depth: 1 },
			},
			true,
			noFiles,
			counts(),
		);
		expect(chips.map((chip) => [chip.kind, chip.label, chip.attention])).toEqual([
			["tasks", "Tasks 3/7", false],
			["goal", "Goal", true],
			["queue", "Queue 1", false],
		]);
		expect(chips.map((chip) => chip.accessibilityLabel)).toEqual([
			"Tasks, 3 of 7 done",
			"Goal, blocked",
			"1 queued message",
		]);
	});

	it("follow Subagents with Files and its count, dotted when a document is new or changed", () => {
		const session = { delegates: [delegate("running", 1)], tasks: { total: 7, done: 3 }, goal: null, queue: null };
		const chips = contextChips(session, true, { count: 4, fresh: false }, counts(1));
		expect(chips.map((chip) => chip.kind)).toEqual(["subagents", "files", "tasks"]);
		expect(chips[1]).toEqual({
			kind: "files",
			label: "Files 4",
			attention: false,
			dot: false,
			accessibilityLabel: "Files, 4",
		});
		expect(contextChips(session, true, { count: 4, fresh: true }, counts(1))[1]).toMatchObject({
			dot: true,
			accessibilityLabel: "Files, 4, new or changed",
		});
		expect(contextChips(session, true, { count: 0, fresh: false }, counts(1)).map((chip) => chip.kind)).not.toContain(
			"files",
		);
		expect(contextChips(session, true, noFiles, counts(1)).map((chip) => chip.kind)).not.toContain("files");
	});

	it("hides Subagents and Tasks while disconnected, since tapping either can't act (Calm); Goal and Queue don't need a connection", () => {
		const chips = contextChips(
			{
				delegates: [delegate("running", 1)],
				tasks: { total: 7, done: 3 },
				goal: { objective: "Ship it", status: "on_track", iterations: 1 },
				queue: { revision: 1, depth: 1 },
			},
			false,
		);
		expect(chips.map((chip) => chip.kind)).toEqual(["goal", "queue"]);
	});
});

it("uses complete summary counts instead of a partial transcript roster, and shows unknown honestly", () => {
	const s = { delegates: [delegate("running", 1)], tasks: null, goal: null, queue: null };
	expect(
		contextChips(
			s,
			true,
			{ count: 0, fresh: false },
			{ known: true, total: 501, active: 400, failed: 20, completed: 81 },
		)[0],
	).toMatchObject({ label: "Subagents 501", failed: "20 failed" });
	expect(
		contextChips(
			s,
			true,
			{ count: 0, fresh: false },
			{ known: false, total: 0, active: 0, failed: 0, completed: 0 },
		)[0],
	).toMatchObject({ label: "Subagents …", accessibilityLabel: "Subagents, count unknown" });
	expect(
		contextChips(s, true, { count: 0, fresh: false }, { known: true, total: 0, active: 0, failed: 0, completed: 0 }),
	).toEqual([]);
});
