import type { TurnModel } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { MobileTimelineItem } from "../projectedRows";
import type { RunStep, TimelineRow } from "../timeline";
import { liveRunId, runSummary, runSummaryText, sessionRows, timeMarkerText } from "./transcriptRows";

type Activity = Extract<MobileTimelineItem, { kind: "activity" }>;
const step = (id: string, label: string, over: Partial<Activity> = {}): Activity => ({
	kind: "activity",
	id,
	label,
	family: "tool",
	state: "completed",
	detail: {},
	transcriptKey: `key-${id}`,
	turnId: "turn_1",
	...over,
});
const user = (id: string, turnId = "turn_1"): TimelineRow => ({ kind: "user", id, text: id, turnId });
const reply = (id: string, turnId = "turn_1"): TimelineRow => ({ kind: "assistant", id, markdown: id, streaming: false, turnId });
const at = (hour: number, minute: number, day = 26) => new Date(Date.UTC(2026, 8, day, hour, minute)).toISOString();
const turn = (id: string, startedAt?: string, completedAt?: string): Pick<TurnModel, "id" | "startedAt" | "completedAt"> => ({
	id,
	startedAt,
	completedAt,
});

describe("runs of steps (spec 8.2)", () => {
	it("folds consecutive steps into one run, failures included", () => {
		const rows = sessionRows(
			[user("u"), step("a", "read_file"), step("b", "shell", { state: "failed" }), reply("r")],
			[turn("turn_1")],
		);
		expect(rows.map((row) => row.kind)).toEqual(["user", "run", "assistant"]);
		const run = rows[1];
		if (run?.kind !== "run") throw new Error("expected a run");
		expect(run.steps.map((s) => s.id)).toEqual(["a", "b"]);
		expect(run.transcriptKey).toBe("key-a");
		expect(run.id).toBe("run:a");
	});

	it("keeps subagents, questions and thoughts out of runs", () => {
		const rows = sessionRows(
			[
				step("a", "read_file"),
				step("d", "delegate"),
				step("b", "grep"),
				step("t", "Reasoning", { family: "reasoning" }),
				step("c", "shell"),
			],
			[turn("turn_1")],
		);
		expect(rows.map((row) => (row.kind === "run" ? `run:${row.steps.length}` : row.id))).toEqual([
			"run:1",
			"d",
			"run:1",
			"t",
			"run:1",
		]);
	});

	it("leaves the step in progress, and a live thought, to the tray (ruling 10)", () => {
		const rows = sessionRows(
			[
				step("a", "read_file"),
				step("live", "shell", { state: "running" }),
				step("think", "Reasoning", { family: "reasoning", state: "running" }),
			],
			[turn("turn_1")],
		);
		expect(rows).toHaveLength(1);
		expect(rows[0]?.kind === "run" && rows[0].steps.map((s) => s.id)).toEqual(["a"]);
		expect(sessionRows([step("live", "shell", { state: "running" })], [turn("turn_1")])).toEqual([]);
	});

	it("rides a step's images with the step", () => {
		const rows = sessionRows(
			[
				step("a", "screenshot"),
				{ kind: "attachments", id: "a:attachments", items: [{ id: "a:out:0", src: "/doc/image?1" }], sourceTranscriptKey: "key-a", turnId: "turn_1" },
				step("b", "read_file"),
			],
			[turn("turn_1")],
		);
		expect(rows).toHaveLength(1);
		const run = rows[0];
		expect(run?.kind === "run" && run.steps[0]?.images).toEqual([{ id: "a:out:0", src: "/doc/image?1" }]);
	});
});

describe("time markers", () => {
	const turns = [
		turn("turn_1", at(12, 0), at(12, 5)),
		turn("turn_2", at(12, 10), at(12, 12)),
		turn("turn_3", at(12, 30), at(12, 31)),
		turn("turn_4", at(9, 0, 27), at(9, 1, 27)),
	];
	it("marks the first turn, a turn after ten quiet minutes, and a new day", () => {
		const rows = sessionRows(
			[user("u1", "turn_1"), user("u2", "turn_2"), user("u3", "turn_3"), user("u4", "turn_4")],
			turns,
			"UTC",
		);
		expect(rows.map((row) => (row.kind === "time" ? `time:${row.turnId}` : row.id))).toEqual([
			"time:turn_1",
			"u1",
			"u2",
			"time:turn_3",
			"u3",
			"time:turn_4",
			"u4",
		]);
	});

	it("splits a run at a marked turn boundary (the first loaded turn is always marked)", () => {
		const rows = sessionRows([step("a", "read_file", { turnId: "turn_2" }), step("b", "grep", { turnId: "turn_3" })], turns, "UTC");
		expect(rows.map((row) => row.kind)).toEqual(["time", "run", "time", "run"]);
	});

	// Coordinator note C: a goal continuation turn can start well inside the
	// ten-minute window, so no marker separates it from the turn before it.
	// The run still has to end at the turn change, or its steps mix two
	// turns' worth of work under one id and duration.
	it("ends a run at a turn change even without a marker (a goal continuation)", () => {
		const rows = sessionRows(
			[step("a", "read_file", { turnId: "turn_1" }), step("b", "grep", { turnId: "turn_2" })],
			[turn("turn_1", at(12, 0), at(12, 5)), turn("turn_2", at(12, 8))],
			"UTC",
		);
		expect(rows.map((row) => row.kind)).toEqual(["time", "run", "run"]);
		expect(rows[1]?.kind === "run" && rows[1].steps.map((s) => s.id)).toEqual(["a"]);
		expect(rows[2]?.kind === "run" && rows[2].steps.map((s) => s.id)).toEqual(["b"]);
	});

	it("reads Today, Yesterday, a weekday, or a date", () => {
		const now = Date.UTC(2026, 8, 26, 18, 0);
		expect(timeMarkerText(Date.UTC(2026, 8, 26, 14, 14), now, "UTC")).toBe("Today 2:14 PM");
		expect(timeMarkerText(Date.UTC(2026, 8, 25, 9, 3), now, "UTC")).toBe("Yesterday 9:03 AM");
		expect(timeMarkerText(Date.UTC(2026, 8, 22, 9, 3), now, "UTC")).toBe("Tue 9:03 AM");
		expect(timeMarkerText(Date.UTC(2026, 8, 12, 9, 3), now, "UTC")).toBe("Sep 12, 9:03 AM");
	});
});

describe("the live run (for Task 24)", () => {
	it("finds the last run of the active turn among runs of two turns", () => {
		const rows = sessionRows(
			[
				step("a", "read_file", { turnId: "turn_1" }),
				step("b", "grep", { turnId: "turn_2" }),
				reply("mid", "turn_2"),
				step("c", "shell", { turnId: "turn_2" }),
			],
			[turn("turn_1"), turn("turn_2")],
		);
		expect(liveRunId(rows, "turn_2")).toBe("run:c");
	});

	it("finds nothing with no active turn", () => {
		const rows = sessionRows([step("a", "read_file", { turnId: "turn_1" })], [turn("turn_1")]);
		expect(liveRunId(rows, undefined)).toBeUndefined();
	});

	it("finds nothing when the active turn has no run yet", () => {
		const rows = sessionRows([user("u", "turn_1")], [turn("turn_1")]);
		expect(liveRunId(rows, "turn_1")).toBeUndefined();
	});
});

describe("a run's one line", () => {
	const shell = (id: string, command: string | undefined, over: Partial<Activity> = {}): RunStep =>
		step(id, "shell", { detail: command === undefined ? {} : { arguments: JSON.stringify({ command }) }, ...over });

	it("counts steps, says what they did, and how long the run took", () => {
		const steps: RunStep[] = [
			...Array.from({ length: 6 }, (_, n) => step(`r${n}`, "read_file", { detail: { startedAtMs: 1_000 * n, endedAtMs: 1_000 * n + 500 } })),
			shell("s1", "go test ./agent/...", {
				state: "failed",
				detail: { arguments: JSON.stringify({ command: "go test ./agent/..." }), startedAtMs: 6_000, endedAtMs: 6_500 },
			}),
			shell("s2", "go test ./agent/... -run X", {
				state: "failed",
				detail: { arguments: JSON.stringify({ command: "go test ./agent/... -run X" }), startedAtMs: 6_500, endedAtMs: 7_000 },
			}),
			shell("s3", "go test ./agent/", { detail: { arguments: JSON.stringify({ command: "go test ./agent/" }), startedAtMs: 10_000, endedAtMs: 480_000 } }),
			...Array.from({ length: 3 }, (_, n) =>
				step(`e${n}`, "edit_file", { detail: { startedAtMs: 20_000 + n * 1_000, endedAtMs: 20_500 + n * 1_000 } }),
			),
		];
		const summary = runSummary(steps);
		expect(summary).toEqual({
			steps: 12,
			durationMs: 480_000,
			parts: [
				{ text: "read 6 files", failed: 0 },
				{ text: "ran go test", failed: 2 },
				{ text: "edited 3 files", failed: 0 },
			],
			failed: 2,
		});
		expect(runSummaryText(summary)).toBe("12 steps · 8m · read 6 files, ran go test (2 failed), edited 3 files");
	});

	it("says one step, and names commands only when it knows them all", () => {
		expect(runSummaryText(runSummary([step("a", "read_file")]))).toBe("1 step · read 1 file");
		expect(runSummary([shell("a", "go test"), shell("b", "npm run check")]).parts).toEqual([{ text: "ran 2 commands", failed: 0 }]);
		expect(runSummary([shell("a", undefined), shell("b", "go test")]).parts).toEqual([{ text: "ran 2 commands", failed: 0 }]);
		expect(runSummary([shell("a", "ls -la")]).parts).toEqual([{ text: "ran ls", failed: 0 }]);
	});

	// Coordinator note B: a settled step at a compact detail level carries no
	// clock times, so a duration read from only some steps would understate
	// the run. It is said only when every step in the run carries both.
	it("says how long only when every step carries its clock times", () => {
		expect(
			runSummary([
				step("a", "grep", { detail: { startedAtMs: 1_000, endedAtMs: 2_000 } }),
				step("b", "glob", { detail: { startedAtMs: 4_000, endedAtMs: 6_000 } }),
			]),
		).toMatchObject({
			durationMs: 5_000,
			parts: [{ text: "searched 2 times", failed: 0 }],
		});
		expect(
			runSummary([
				step("a", "grep", { detail: { startedAtMs: 1_000, endedAtMs: 2_000 } }),
				step("b", "glob", { detail: { durationMs: 3_000 } }),
			]).durationMs,
		).toBeUndefined();
		expect(runSummary([step("a", "grep")]).durationMs).toBeUndefined();
		expect(runSummaryText(runSummary([step("a", "grep")]))).toBe("1 step · searched once");
	});

	it("groups the rest by kind, in the order they first appear", () => {
		expect(
			runSummary([step("a", "web_fetch"), step("b", "task_list"), step("c", "web_search"), step("d", "use_skill")]).parts.map(
				(part) => part.text,
			),
		).toEqual(["fetched 1 page", "2 other steps", "searched the web once"]);
	});
});
