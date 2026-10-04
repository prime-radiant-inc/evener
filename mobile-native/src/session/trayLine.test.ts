import type {
	EvenerDelegateInfo,
	ItemModel,
	ModelRetryState,
	NavigationSessionSummary,
	SessionActivity,
	TurnModel,
} from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { whyLine } from "../board/attention";
import { subagentTally } from "./sessionState";
import { FrameCounter, type TraySource, trayLine } from "./trayLine";

const NOW = Date.UTC(2026, 8, 26, 14, 0, 0);
const ago = (ms: number) => new Date(NOW - ms).toISOString();

const item = (over: Partial<ItemModel>): ItemModel => ({
	id: "item-1",
	turnId: "turn_1",
	type: "commandExecution",
	text: "",
	...over,
});
const turn = (items: ItemModel[], status = "inProgress"): TurnModel => ({ id: "turn_1", status, items });
const delegate = (status: string, n: number): EvenerDelegateInfo => ({
	runGeneration: 1,
	delegateId: `d-${n}`,
	ownerSessionId: "root",
	rootSessionId: "root",
	childSessionId: `child-${n}`,
	transcriptRef: `local:child-${n}`,
	// Every EvenerDelegateInfo the hub sends is the stable "delegate" shape.
	type: "delegate",
	lifecycle: status,
	phase: status,
	status,
	resumable: false,
	needsAttention: false,
	projectionRevision: 1,
});
const session = (over: Partial<TraySource> = {}): TraySource => ({
	status: { type: "active" },
	turns: [],
	runningTurnId: "turn_1",
	delegates: [],
	modelRetry: undefined,
	lastFrameAt: NOW,
	...over,
});

describe("the tray's line (spec 8.3)", () => {
	it("is absent unless a turn is running", () => {
		expect(trayLine(session({ status: { type: "idle" } }), NOW)).toBeNull();
		// #2514: a session resting on a failed turn reports systemError until its
		// next turn. The tray shows only while a turn runs, so this reads null too.
		expect(trayLine(session({ status: { type: "systemError" } }), NOW)).toBeNull();
	});

	it("names the command that is running and how long it has run", () => {
		const running = item({
			toolName: "shell",
			argumentsJSON: JSON.stringify({ command: "go test ./agent/...\necho done" }),
			status: "inProgress",
			startedAt: ago(42_000),
		});
		expect(trayLine(session({ turns: [turn([running])] }), NOW)).toEqual({
			text: "Running go test ./agent/... · 42s",
			attention: false,
		});
	});

	it("uses a step's own intent when it isn't a shell command", () => {
		const reading = item({
			toolName: "read_file",
			description: "Reading agent/retirement_test.go",
			status: "inProgress",
			startedAt: ago(5_000),
		});
		expect(trayLine(session({ turns: [turn([reading])] }), NOW)?.text).toBe("Reading agent/retirement_test.go");
	});

	// A step with no intent says what it is doing in the step's own words'
	// live form (the package's toolStepProgress), never its raw tool name.
	it("says a running step with no intent in words, never its tool's name", () => {
		const step = (toolName: string, args: Record<string, unknown>) =>
			item({ toolName, argumentsJSON: JSON.stringify(args), status: "inProgress", startedAt: ago(5_000) });
		const text = (running: ReturnType<typeof item>) => trayLine(session({ turns: [turn([running])] }), NOW)?.text;
		expect(text(step("read_file", { file_path: "agent/tree.go" }))).toBe("Reading agent/tree.go · 5s");
		expect(text(step("github__create_issue", { title: "x" }))).toBe("Using github: create issue · 5s");
		expect(text(step("reindex_workspace", {}))).toBe("Using reindex workspace · 5s");
		// A housekeeping tool says what it's doing in its own words.
		expect(text(step("compact_context", { note_to_self: "Next: run the race detector." }))).toBe(
			"Asking for a context compaction · 5s",
		);
		expect(text(step("shell", {}))).toBe("Running a command · 5s");
	});

	// A shell call whose command is missing or named otherwise still says its
	// intent, as the web's row does, rather than a bare "Running a command".
	it("keeps a command-less shell step's intent", () => {
		const running = item({
			toolName: "shell",
			argumentsJSON: JSON.stringify({ description: "run the audit" }),
			description: "Run the audit",
			status: "inProgress",
			startedAt: ago(5_000),
		});
		expect(trayLine(session({ turns: [turn([running])] }), NOW)?.text).toBe("Run the audit");
	});

	it("leaves out a running command's cd to the session's own directory", () => {
		const running = item({
			toolName: "shell",
			argumentsJSON: JSON.stringify({ command: "cd /repo && make test" }),
			status: "inProgress",
			startedAt: ago(5_000),
		});
		expect(trayLine({ ...session({ turns: [turn([running])] }), cwd: "/repo" }, NOW)?.text).toBe(
			"Running make test · 5s",
		);
	});

	it("says Thinking with a token estimate and no clock", () => {
		const thought = item({ type: "reasoning", text: "x".repeat(4_800), status: "inProgress" });
		expect(trayLine(session({ turns: [turn([thought])] }), NOW)?.text).toBe("Thinking… · 1.2K tokens");
	});

	it("estimates a streaming thought from its summaries or its text, whichever is longer", () => {
		const summarized = item({
			type: "reasoning",
			status: "inProgress",
			reasoningSummaries: [["x".repeat(2_000), "x".repeat(2_000)], ["x".repeat(800)]],
		});
		expect(trayLine(session({ turns: [turn([summarized])] }), NOW)?.text).toBe("Thinking… · 1.2K tokens");
		const streamed = item({
			type: "reasoning",
			status: "inProgress",
			text: "x".repeat(800),
			pendingText: ["x".repeat(2_000), "x".repeat(6_000)],
			reasoningSummaries: [["x".repeat(400)]],
		});
		expect(trayLine(session({ turns: [turn([streamed])] }), NOW)?.text).toBe("Thinking… · 2.2K tokens");
	});

	it("says Writing while the reply streams", () => {
		const reply = item({ type: "agentMessage", status: "inProgress" });
		expect(trayLine(session({ turns: [turn([reply])] }), NOW)?.text).toBe("Writing…");
	});

	// Without a hub read, the tray counts what the Subagents list, its chip and
	// the transcript row call running (subagentState) among the session's own
	// delegates.
	it("without a hub read, counts the delegates the list counts as running", () => {
		// Resumable with no ended run, so not terminal: running. An idle
		// subagent whose run ended is terminal on the wire.
		const idle = { ...delegate("idle", 2), resumable: true };
		const stopped = { ...delegate("stopped", 3), terminal: true, outcome: "stopped" };
		const failed = { ...delegate("failed", 4), terminal: true, outcome: "failed" };
		const delegates = [delegate("running", 1), idle, stopped, failed];
		expect(subagentTally(delegates).running).toBe(2);
		expect(trayLine(session({ delegates }), NOW)?.text).toBe("Waiting on 2 subagents");
	});

	it("falls back to the subagent count until a tool intent arrives", () => {
		const running = Array.from({ length: 12 }, (_, n) => delegate("running", n));
		const finished = { ...delegate("completed", 99), terminal: true, outcome: "completed" };
		expect(trayLine(session({ delegates: [...running, finished] }), NOW)?.text).toBe("Waiting on 12 subagents");
		expect(trayLine(session({ delegates: [delegate("running", 1)] }), NOW)?.text).toBe("Waiting on 1 subagent");
		const watching = item({ toolName: "job_watch", description: "Watching the jobs", status: "inProgress" });
		expect(trayLine(session({ turns: [turn([watching])], delegates: running }), NOW)?.text).toBe("Watching the jobs");
	});

	// Spec 13.1: one line, the same on the Board and in the tray. The root's
	// thread carries only the subagents the coordinator started itself
	// (agent/status.go lists the delegates the session owns), while the hub
	// counts running subagents at every depth: its activity read
	// (app_activity.go's runningSubagents), and without one the session's
	// navigation row tally (S3). The tray takes the same count by the same
	// precedence the Board does, so a running grandchild, or a running child of
	// a failed subagent, counts on both, and a fresh read wins over the tally.
	const failed = { ...delegate("failed", 2), terminal: true, outcome: "failed" };
	it.each([
		{
			tree: "a running subagent with its own running subagent, and a failed one whose subagent still runs",
			delegates: [delegate("running", 1), failed],
			read: 3,
			tally: 2,
			text: "Waiting on 3 subagents",
		},
		{
			tree: "only a failed subagent whose own subagent still runs, long silent",
			delegates: [failed],
			read: 1,
			tally: 0,
			text: "Waiting on 1 subagent",
		},
		{
			tree: "the same nested tree with no activity read",
			delegates: [delegate("running", 1), failed],
			read: undefined,
			tally: 3,
			text: "Waiting on 3 subagents",
		},
	])("names the hub's running-subagent count, as the Board does: $tree", ({ delegates, read, tally, text }) => {
		const activity: SessionActivity | undefined =
			read === undefined ? undefined : { ref: "local:root", minutes: [0, 0, 0, 0, 0, 0, 0], runningSubagents: read };
		const row: NavigationSessionSummary = {
			ref: "local:root",
			host_id: "local",
			session_id: "root",
			title: "Get PR 2138 Test Clean",
			project: "evener",
			state: "active",
			kind: "session",
			live: true,
			subagents: { running: tally, failed: 1, done: 0 },
			children: [],
		};
		const board = whyLine({ row, state: "working" }, activity);
		const tray = trayLine(session({ delegates, lastFrameAt: NOW - 15 * 60_000 }), NOW, activity, row);
		expect(board?.text).toBe(text);
		expect(tray).toEqual({ text, attention: false });
	});

	it("keeps the newest completed tool intent above status fallbacks", () => {
		const reading = item({
			toolName: "read_file",
			description: "Reading the review",
			status: "completed",
		});
		const reviewing = item({
			id: "item-2",
			toolName: "shell",
			description: "Checking the latest review findings",
			status: "completed",
		});
		const turns = [turn([reading, reviewing])];
		const intent = "Checking the latest review findings";
		expect(trayLine(session({ turns, delegates: [delegate("running", 1)] }), NOW)?.text).toBe(intent);
		expect(trayLine(session({ turns, lastFrameAt: NOW - 12 * 60_000 }), NOW)?.text).toBe(intent);
		expect(
			trayLine(
				session({
					turns,
					modelRetry: {
						attempt: 2,
						maxAttempts: 11,
						attemptCap: 4,
						delayMs: 30_000,
						errorClass: "rate_limit",
						groupElapsedMs: 30_000,
						receivedAt: NOW,
					},
				}),
				NOW,
			)?.text,
		).toBe(intent);
	});

	it("skips blank descriptions and keeps the newest nonblank intent verbatim", () => {
		const described = item({ description: "  Reading the exact request  ", status: "completed" });
		const blank = item({ id: "item-2", description: " \n ", status: "completed" });

		expect(trayLine(session({ turns: [turn([described, blank])] }), NOW)?.text).toBe("  Reading the exact request  ");
	});

	it("reads the tool intent from the live running turn", () => {
		const previous = item({ description: "Reading the previous turn", status: "completed" });
		const current = item({
			id: "item-2",
			turnId: "turn_2",
			description: "Checking the current turn",
			status: "completed",
		});
		const turns = [turn([previous], "completed"), { id: "turn_2", status: "inProgress", items: [current] }];
		const liveSession = session({
			turns,
			activeTurnId: "turn_1",
			runningTurnId: "turn_2",
		});

		expect(trayLine(liveSession, NOW)?.text).toBe("Checking the current turn");
	});

	it("reads current-step progress from the live running turn", () => {
		const previous = item({ type: "agentMessage", status: "inProgress" });
		const current = item({ id: "item-2", turnId: "turn_2", type: "reasoning", status: "inProgress" });
		const turns = [turn([previous]), { id: "turn_2", status: "inProgress", items: [current] }];
		const liveSession = session({
			turns,
			activeTurnId: "turn_1",
			runningTurnId: "turn_2",
		});

		expect(trayLine(liveSession, NOW)?.text).toBe("Thinking…");
	});

	it("ignores a completed turn still named by the live running turn id", () => {
		const previous = item({ description: "Reading the previous turn", status: "completed" });
		const current = item({ id: "item-2", turnId: "turn_2", type: "reasoning", status: "inProgress" });
		const turns = [turn([previous], "completed"), { id: "turn_2", status: "inProgress", items: [current] }];
		const betweenFrames = session({
			turns,
			activeTurnId: "turn_2",
			runningTurnId: "turn_1",
		});

		expect(trayLine(betweenFrames, NOW)?.text).toBe("Thinking…");
	});

	it("falls back to snapshot progress until the live running turn arrives", () => {
		const running = item({
			toolName: "shell",
			argumentsJSON: JSON.stringify({ command: "make test" }),
			status: "inProgress",
			startedAt: ago(5_000),
		});
		const betweenFrames = session({
			turns: [turn([running])],
			activeTurnId: "turn_1",
			runningTurnId: "turn_2",
		});

		expect(trayLine(betweenFrames, NOW)?.text).toBe("Running make test · 5s");
	});

	it("keeps snapshot-only command progress without leaking its tool intent", () => {
		const described = item({ description: "Reading the snapshot", status: "completed" });
		const running = item({
			id: "item-2",
			toolName: "shell",
			argumentsJSON: JSON.stringify({ command: "make test" }),
			status: "inProgress",
			startedAt: ago(5_000),
		});
		const snapshotOnly = session({
			turns: [turn([described, running])],
			activeTurnId: "turn_1",
			runningTurnId: undefined,
		});

		expect(trayLine(snapshotOnly, NOW)?.text).toBe("Running make test · 5s");
	});

	it("keeps a snapshot-only command-less shell step's intent", () => {
		const running = item({
			toolName: "shell",
			argumentsJSON: JSON.stringify({ description: "run the audit" }),
			description: "Run the audit",
			status: "inProgress",
			startedAt: ago(5_000),
		});
		const snapshotOnly = session({
			turns: [turn([running])],
			activeTurnId: "turn_1",
			runningTurnId: undefined,
		});

		expect(trayLine(snapshotOnly, NOW)?.text).toBe("Run the audit · 5s");
	});

	it("goes Quiet after twenty seconds without a frame", () => {
		expect(trayLine(session({ lastFrameAt: NOW - 19_000 }), NOW)?.text).toBe("Working");
		expect(trayLine(session({ lastFrameAt: NOW - 40_000 }), NOW)?.text).toBe("Quiet 40s");
	});

	it("says it may be stuck, in amber, after ten minutes, whatever step is running", () => {
		const running = item({ toolName: "shell", argumentsJSON: '{"command":"sleep 900"}', status: "inProgress" });
		expect(trayLine(session({ turns: [turn([running])], lastFrameAt: NOW - 12 * 60_000 }), NOW)).toEqual({
			text: "May be stuck · no updates for 12m",
			attention: true,
		});
	});

	it("never says Quiet or May be stuck while a subagent runs (Jesse's S5 ruling)", () => {
		const silent = { delegates: [delegate("running", 1)], lastFrameAt: NOW - 15 * 60_000 };
		expect(trayLine(session(silent), NOW)).toEqual({ text: "Waiting on 1 subagent", attention: false });
		const running = item({ toolName: "shell", argumentsJSON: '{"command":"sleep 900"}', status: "inProgress" });
		expect(trayLine(session({ ...silent, turns: [turn([running])] }), NOW)?.attention).toBe(false);
	});

	it("waits for the quiet threshold before explaining a first retry", () => {
		const retry: ModelRetryState = {
			attempt: 1,
			maxAttempts: 11,
			attemptCap: 4,
			delayMs: 30_000,
			errorClass: "rate_limit",
			groupElapsedMs: 5_000,
			receivedAt: NOW,
		};
		const reply = item({ type: "agentMessage", status: "inProgress" });
		const withRetry = (lastFrameAt: number) => session({ modelRetry: retry, turns: [turn([reply])], lastFrameAt });
		expect(trayLine(withRetry(NOW - 5_000), NOW)?.text).toBe("Writing…");
		expect(trayLine(withRetry(NOW - 25_000), NOW)?.text).toBe("Retrying · rate limited · attempt 1 of 4");
	});

	it("explains a retry with its cause and place in the budget", () => {
		const retry: ModelRetryState = {
			attempt: 3,
			maxAttempts: 11,
			attemptCap: 4,
			delayMs: 30_000,
			errorClass: "rate_limit",
			groupElapsedMs: 90_000,
			receivedAt: NOW,
		};
		expect(trayLine(session({ modelRetry: retry }), NOW)?.text).toBe("Retrying · rate limited · attempt 3 of 4");
		expect(trayLine(session({ modelRetry: { ...retry, errorClass: "server", attemptCap: 0 } }), NOW)?.text).toBe(
			"Retrying · provider error · attempt 3",
		);
	});

	it("keeps a long retry's explanation past ten minutes, adding the silence in amber", () => {
		const retry: ModelRetryState = {
			attempt: 9,
			maxAttempts: 11,
			attemptCap: 11,
			delayMs: 60_000,
			errorClass: "rate_limit",
			groupElapsedMs: 700_000,
			receivedAt: NOW - 60_000,
		};
		expect(trayLine(session({ modelRetry: retry, lastFrameAt: NOW - 12 * 60_000 }), NOW)).toEqual({
			text: "Retrying · rate limited · attempt 9 of 11 · no updates for 12m",
			attention: true,
		});
	});
});

describe("the tray's pulse counts (spec 16.4)", () => {
	it("counts frames per minute, newest on the right, and forgets past seven minutes", () => {
		const counter = new FrameCounter();
		const start = Date.UTC(2026, 8, 26, 12, 0, 0);
		expect(counter.hasFrames()).toBe(false);
		counter.record(start + 1_000);
		counter.record(start + 2_000);
		counter.record(start + 3 * 60_000);
		expect(counter.hasFrames()).toBe(true);
		expect(counter.perMinute(start + 3 * 60_000 + 5_000)).toEqual([0, 0, 0, 2, 0, 0, 1]);
		counter.record(start + 11 * 60_000);
		expect(counter.perMinute(start + 11 * 60_000)).toEqual([0, 0, 0, 0, 0, 0, 1]);
	});
});
