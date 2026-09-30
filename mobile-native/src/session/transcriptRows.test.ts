import type { TurnModel } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { type MobileTimelineItem, projectedRow } from "../projectedRows";
import type { RunStep, TimelineRow } from "../timeline";
import {
	answerTo,
	hideAnswerMessages,
	latestSettledTurn,
	liveRunId,
	newRowCount,
	runSummary,
	runSummaryText,
	sessionRows,
	timeMarkerText,
} from "./transcriptRows";

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
const reply = (id: string, turnId = "turn_1"): TimelineRow => ({
	kind: "assistant",
	id,
	markdown: id,
	streaming: false,
	turnId,
});
const at = (hour: number, minute: number, day = 26) => new Date(Date.UTC(2026, 8, day, hour, minute)).toISOString();
const turn = (
	id: string,
	startedAt?: string,
	completedAt?: string,
): Pick<TurnModel, "id" | "startedAt" | "completedAt"> => ({
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

	it("leaves a question still waiting to the dock (spec 8.2)", () => {
		const live: TimelineRow = {
			kind: "question",
			id: "ask",
			questions: [
				{ key: "q1", callId: "ask", header: "Choice", question: "Keep them?", options: [], multiSelect: false },
			],
		};
		const rows = sessionRows([user("u"), live, reply("r")], [turn("turn_1")]);
		expect(rows.map((row) => row.id)).toEqual(["u", "r"]);
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
				{
					kind: "attachments",
					id: "a:attachments",
					items: [{ id: "a:out:0", src: "/doc/image?1" }],
					sourceTranscriptKey: "key-a",
					turnId: "turn_1",
				},
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
			{ timeZone: "UTC" },
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

	it("splits a run at a marked turn boundary (the first turn of a whole history is marked)", () => {
		const rows = sessionRows(
			[step("a", "read_file", { turnId: "turn_2" }), step("b", "grep", { turnId: "turn_3" })],
			turns,
			{ timeZone: "UTC" },
		);
		expect(rows.map((row) => row.kind)).toEqual(["time", "run", "time", "run"]);
	});

	it("leaves the first loaded turn unmarked while older history is still to load", () => {
		const rows = sessionRows([user("u2", "turn_2"), user("u3", "turn_3")], turns, {
			timeZone: "UTC",
			olderToLoad: true,
		});
		expect(rows.map((row) => (row.kind === "time" ? `time:${row.turnId}` : row.id))).toEqual([
			"u2",
			"time:turn_3",
			"u3",
		]);
	});

	it("keeps the first row's key across an older page, with steps and a delegate_send among them", () => {
		// Before: the latest page, with more above. After: the page above
		// prepended. The first key before must still be in the list after, so
		// the list can find where its window moved to.
		const latest = [
			user("u2", "turn_2"),
			step("s2", "delegate_send", { turnId: "turn_2" }),
			step("s3", "shell", { turnId: "turn_2" }),
			user("u3", "turn_3"),
		];
		const before = sessionRows(latest, turns, { timeZone: "UTC", olderToLoad: true });
		const after = sessionRows([user("u1", "turn_1"), ...latest], turns, { timeZone: "UTC", olderToLoad: true });
		const first = before[0]?.id;
		expect(first).toBeDefined();
		expect(after.map((row) => row.id)).toContain(first);
		// Once nothing older is left, the first turn gets its marker back.
		const whole = sessionRows([user("u1", "turn_1"), ...latest], turns, { timeZone: "UTC" });
		expect(whole[0]).toMatchObject({ kind: "time", turnId: "turn_1" });
	});

	// The reducer can seat a notice in the display turn that holds its recorded
	// item, and the notice keeps its own turn id, so one turn's rows can have
	// another turn's row inside them. When the outer turn resumes, its start is
	// compared against the notice's turn, which carries no times — read as a
	// gap — so a second marker for the same turn would appear, and the
	// transcript's FlatList keys rows by id, so the two markers collide.
	it("marks a turn once when another turn's row sits inside it", () => {
		const notice: TimelineRow = {
			kind: "notice",
			id: "n",
			origin: "system",
			family: "lifecycle",
			tone: "info",
			text: "note",
			turnId: "turn_2",
		};
		const rows = sessionRows(
			[user("u", "turn_1"), notice, reply("r", "turn_1")],
			[turn("turn_1", at(12, 0), at(12, 5)), turn("turn_2")],
			{ timeZone: "UTC" },
		);
		expect(rows.map((row) => (row.kind === "time" ? `time:${row.turnId}` : row.id))).toEqual([
			"time:turn_1",
			"u",
			"n",
			"r",
		]);
	});

	// A goal continuation turn can start well inside the ten-minute window, so
	// no marker separates it from the turn before it. The run still has to end
	// at the turn change, or its steps mix two turns' worth of work under one
	// id and duration.
	it("ends a run at a turn change even without a marker (a goal continuation)", () => {
		const rows = sessionRows(
			[step("a", "read_file", { turnId: "turn_1" }), step("b", "grep", { turnId: "turn_2" })],
			[turn("turn_1", at(12, 0), at(12, 5)), turn("turn_2", at(12, 8))],
			{ timeZone: "UTC" },
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

	// "Yesterday" has to be the zone's previous calendar date, not "24 hours
	// ago"; those disagree across a DST change.
	it("counts calendar days, so Yesterday survives a DST change", () => {
		// America/New_York's 2026 DST began 2026-03-08 at 2 AM local, so that
		// day has only 23 wall-clock hours: subtracting a flat 24h from "now"
		// (00:30 local on March 9) lands on March 7, not March 8.
		expect(timeMarkerText(Date.UTC(2026, 2, 8, 13, 3), Date.UTC(2026, 2, 9, 4, 30), "America/New_York")).toBe(
			"Yesterday 9:03 AM",
		);
	});

	it("keeps the weekday window to whole calendar days", () => {
		const now = Date.UTC(2026, 8, 26, 18, 0);
		expect(timeMarkerText(Date.UTC(2026, 8, 20, 17, 0), now, "UTC")).toBe("Sun 5:00 PM");
		expect(timeMarkerText(Date.UTC(2026, 8, 19, 20, 0), now, "UTC")).toBe("Sep 19, 8:00 PM");
	});
});

describe("the live run", () => {
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
	// A shell step as projectedRows builds it: its arguments, and its words,
	// whose target is the command with the session's own cd left out.
	const shell = (id: string, command: string | undefined, over: Partial<Activity> = {}): RunStep => {
		const detail =
			command === undefined ? {} : { arguments: JSON.stringify({ command }), words: { verb: "Ran", target: command } };
		return step(id, "shell", { ...over, detail: { ...detail, ...over.detail } });
	};

	it("counts steps, says what they did, and how long the run took", () => {
		const steps: RunStep[] = [
			...Array.from({ length: 6 }, (_, n) =>
				step(`r${n}`, "read_file", { detail: { startedAtMs: 1_000 * n, endedAtMs: 1_000 * n + 500 } }),
			),
			shell("s1", "go test ./agent/...", {
				state: "failed",
				detail: { arguments: JSON.stringify({ command: "go test ./agent/..." }), startedAtMs: 6_000, endedAtMs: 6_500 },
			}),
			shell("s2", "go test ./agent/... -run X", {
				state: "failed",
				detail: {
					arguments: JSON.stringify({ command: "go test ./agent/... -run X" }),
					startedAtMs: 6_500,
					endedAtMs: 7_000,
				},
			}),
			shell("s3", "go test ./agent/", {
				detail: { arguments: JSON.stringify({ command: "go test ./agent/" }), startedAtMs: 10_000, endedAtMs: 480_000 },
			}),
			...Array.from({ length: 3 }, (_, n) =>
				step(`e${n}`, "edit_file", { detail: { startedAtMs: 20_000 + n * 1_000, endedAtMs: 20_500 + n * 1_000 } }),
			),
		];
		const summary = runSummary(steps);
		expect(summary).toEqual({
			steps: 12,
			durationMs: 480_000,
			parts: [
				{ key: "read", family: "read", text: "read 6 files", failed: 0 },
				{ key: "shell", family: "shell", text: "ran go test", failed: 2 },
				{ key: "edit", family: "edit", text: "edited 3 files", failed: 0 },
			],
			failed: 2,
		});
		expect(runSummaryText(summary)).toBe("12 steps · 8m · read 6 files, ran go test (2 failed), edited 3 files");
	});

	it("says one step, and names commands only when it knows them all", () => {
		expect(runSummaryText(runSummary([step("a", "read_file")]))).toBe("1 step · read 1 file");
		expect(runSummary([shell("a", "go test"), shell("b", "npm run check")]).parts).toEqual([
			{ key: "shell", family: "shell", text: "ran 2 commands", failed: 0 },
		]);
		expect(runSummary([shell("a", undefined), shell("b", "go test")]).parts).toEqual([
			{ key: "shell", family: "shell", text: "ran 2 commands", failed: 0 },
		]);
		expect(runSummary([shell("a", "ls -la")]).parts).toEqual([
			{ key: "shell", family: "shell", text: "ran ls", failed: 0 },
		]);
	});

	it("names the program a command ran, not the cd to the session's own directory", () => {
		const cd = shell("a", "go test ./...", {
			detail: { arguments: JSON.stringify({ command: "cd /repo && go test ./..." }) },
		});
		expect(runSummary([cd]).parts).toEqual([{ key: "shell", family: "shell", text: "ran go test", failed: 0 }]);
	});

	// A step whose times the hub didn't send, or that don't parse, carries no
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

	// At Intent, the phone's default level, every settled step shows only its
	// summary. It still keeps its two clock times, so the run it folds into
	// says how long it took, as spec 8.2's run line does (Jesse, 2026-09-27).
	it("says how long a run of summary-only steps took, as at Intent", () => {
		const summarized = (id: string, startedAt: string, completedAt: string) =>
			projectedRow({
				kind: "intent",
				id: `intent:${id}`,
				turnId: "turn_1",
				sourceIndex: 0,
				sourceItemId: id,
				rationale: `Read ${id}`,
				failed: false,
				item: {
					id,
					turnId: "turn_1",
					type: "commandExecution",
					toolName: "read_file",
					text: "",
					startedAt,
					completedAt,
				},
			});
		const steps = [summarized("a", at(12, 0), at(12, 1)), summarized("b", at(12, 2), at(12, 8))];
		expect(steps.every((row) => row?.kind === "activity" && row.summaryOnly === true)).toBe(true);
		const [run] = sessionRows(
			steps.filter((row) => row !== null),
			[turn("turn_1")],
		);
		expect(run?.kind === "run" && runSummaryText(runSummary(run.steps))).toBe("2 steps · 8m · read 2 files");
	});

	it("groups the rest by kind, in the order they first appear", () => {
		expect(
			runSummary([
				step("a", "web_fetch"),
				step("b", "task_list"),
				step("c", "web_search"),
				step("d", "use_skill", { detail: { arguments: JSON.stringify({ skill_name: "brainstorming" }) } }),
			]).parts.map((part) => part.text),
		).toEqual(["fetched 1 page", "checked the task list", "searched the web once", "used skill brainstorming"]);
	});

	// Never "N other steps": MCP tools share one part, which names their
	// server when there is one, and each tool no summary covers gets a part
	// of its own in words.
	it("names the MCP tools and each tool no summary covers", () => {
		expect(
			runSummary([
				step("a", "github__create_issue"),
				step("b", "github__list_issues"),
				step("c", "linear_app__list_issues"),
				step("d", "reindex_workspace"),
				step("e", "reindex_workspace"),
				step("f", "use_skill", { detail: { arguments: JSON.stringify({ skill_name: "a" }) } }),
				step("g", "use_skill", { detail: { arguments: JSON.stringify({ skill_name: "b" }) } }),
			]).parts.map((part) => [part.key, part.text]),
		).toEqual([
			["mcp", "used 3 MCP tools"],
			["tool:reindex workspace", "used reindex workspace 2 times"],
			["skill", "used 2 skills"],
		]);
		expect(
			runSummary([step("a", "github__create_issue"), step("b", "github__list_issues")]).parts.map((part) => part.text),
		).toEqual(["used github 2 times"]);
	});

	// The session's housekeeping tools say what they did in a run's line, in
	// the same words as their own step lines (the package's housekeeping
	// words), each tool a part of its own.
	it("says what each housekeeping tool did", () => {
		expect(
			runSummary([
				step("a", "notes_agent_set"),
				step("b", "urls_add"),
				step("c", "urls_add"),
				step("d", "update_goal"),
				step("e", "communicate"),
			]).parts.map((part) => part.text),
		).toEqual([
			"updated its note once",
			"added a link 2 times",
			"updated the goal once",
			"reported to its parent once",
		]);
	});

	it("says a run updated the task list, or only checked it", () => {
		const tasks = (id: string, args: Record<string, unknown>): RunStep =>
			step(id, "task_list", { detail: { arguments: JSON.stringify(args) } });
		const update = { update: [{ id: 1, status: "done" }] };
		expect(runSummary([tasks("a", update)]).parts.map((part) => part.text)).toEqual(["updated the task list"]);
		expect(runSummary([tasks("a", {}), tasks("b", {})]).parts.map((part) => part.text)).toEqual([
			"checked the task list 2 times",
		]);
		// One change among the reads makes the part an update.
		expect(runSummary([tasks("a", {}), tasks("b", update)]).parts.map((part) => part.text)).toEqual([
			"updated the task list 2 times",
		]);
	});
});

describe("a run's transcript reads and session searches", () => {
	const texts = (steps: RunStep[]) => runSummary(steps).parts.map((part) => part.text);

	it("counts the transcripts a run read", () => {
		expect(texts([step("a", "read_transcript")])).toEqual(["read a transcript"]);
		expect(texts([step("a", "read_transcript"), step("b", "read_session_transcript")])).toEqual(["read 2 transcripts"]);
	});

	it("says how often a run searched sessions", () => {
		expect(texts([step("a", "find_session_transcripts")])).toEqual(["searched sessions"]);
		expect(texts([step("a", "find_session_transcripts"), step("b", "find_session_transcripts")])).toEqual([
			"searched sessions 2 times",
		]);
	});
});

// An ask_user call takes a row of its own (QuestionHistory), so it only
// reaches a run's line when a caller hands one over directly.
describe("a run's questions", () => {
	it("says how many questions a run asked", () => {
		const texts = (steps: RunStep[]) => runSummary(steps).parts.map((part) => part.text);
		const asking = (id: string, headers: string[]) =>
			step(id, "ask_user", {
				detail: {
					arguments: JSON.stringify({
						questions: headers.map((header) => ({ header, question: "?", options: [{ label: "Yes", detail: "." }] })),
					}),
				},
			});
		expect(texts([asking("a", ["Deploy"])])).toEqual(["asked a question"]);
		// Questions, not calls: one call can ask several.
		expect(texts([asking("a", ["Deploy", "Notify"])])).toEqual(["asked 2 questions"]);
		expect(texts([asking("a", ["Deploy"]), asking("b", ["Notify", "Tag"])])).toEqual(["asked 3 questions"]);
		// A call whose questions don't parse, or that lists none, still asked
		// something: parseAskUserQuestions gives no list rather than an empty one.
		expect(texts([step("a", "ask_user")])).toEqual(["asked a question"]);
		expect(texts([asking("a", [])])).toEqual(["asked a question"]);
	});
});

describe("a run's messages to subagents", () => {
	// A send reads the same under the live name and the retired one.
	it("says how many messages a run sent to subagents", () => {
		const texts = (steps: RunStep[]) => runSummary(steps).parts.map((part) => part.text);
		expect(texts([step("a", "delegate_send")])).toEqual(["sent a message"]);
		expect(texts([step("a", "delegate_send"), step("b", "job_send_message")])).toEqual(["sent 2 messages"]);
	});
});

describe("a run's job steps", () => {
	it("says how often a run managed jobs", () => {
		const texts = (steps: RunStep[]) => runSummary(steps).parts.map((part) => part.text);
		expect(texts([step("a", "job_status")])).toEqual(["managed jobs once"]);
		expect(texts([step("a", "job_list"), step("b", "job_stop")])).toEqual(["managed jobs 2 times"]);
	});
});

describe("a run's worktree steps", () => {
	it("says how often a run managed worktrees", () => {
		const texts = (steps: RunStep[]) => runSummary(steps).parts.map((part) => part.text);
		expect(texts([step("a", "manage_worktree")])).toEqual(["managed worktrees once"]);
		expect(texts([step("a", "manage_worktree"), step("b", "manage_worktree")])).toEqual(["managed worktrees 2 times"]);
	});
});

describe("rows that arrived while you read above the end", () => {
	const rows: TimelineRow[] = [user("u1"), reply("a1"), user("u2"), reply("a2")];
	const seen = new Set(["u1", "a1"]);

	it("counts the rows after the last one you had", () => {
		expect(newRowCount(rows, seen)).toBe(2);
	});

	it("never counts older history that loaded above", () => {
		expect(newRowCount([user("u0"), reply("a0"), ...rows], seen)).toBe(2);
	});

	it("leaves time markers out of the count", () => {
		const marked: TimelineRow[] = [
			...rows.slice(0, 2),
			{ kind: "time", id: "time:turn_2", turnId: "turn_2", at: 0 },
			...rows.slice(2),
		];
		expect(newRowCount(marked, seen)).toBe(2);
	});

	it("is zero when nothing new arrived", () => {
		expect(newRowCount(rows, new Set(["u1", "a1", "u2", "a2"]))).toBe(0);
	});
});

describe("the turn you have seen at the end (ruling 31)", () => {
	it("is the latest turn that is not still in progress", () => {
		const turns = [
			{ id: "turn_1", status: "completed" },
			{ id: "turn_2", status: "failed" },
			{ id: "turn_3", status: "inProgress" },
		];
		expect(latestSettledTurn({ turns })).toBe("turn_2");
	});

	it("is nothing without a conversation or a settled turn", () => {
		expect(latestSettledTurn(null)).toBeUndefined();
		expect(latestSettledTurn({ turns: [{ id: "turn_1", status: "inProgress" }] })).toBeUndefined();
	});
});

describe("answers you gave a question", () => {
	const question = step("q", "ask_user", {
		detail: {
			arguments: JSON.stringify({
				questions: [{ header: "Choice", question: "Keep or drop?", options: [{ label: "Drop them", detail: "" }] }],
			}),
		},
	});
	const answer: TimelineRow = {
		kind: "user",
		id: "ans",
		text: '[answers]\n1. [Choice] → "Drop them"',
		turnId: "turn_1",
	};

	it("leave the transcript: the question row shows the answer", () => {
		expect(hideAnswerMessages([question, answer, reply("r")]).map((row) => row.id)).toEqual(["q", "r"]);
	});

	it("stay when no question came before them", () => {
		expect(hideAnswerMessages([user("u"), answer]).map((row) => row.id)).toEqual(["u", "ans"]);
	});

	it("stay when the question row before them can't show its questions", () => {
		const unreadable = step("q", "ask_user", { detail: { arguments: "{not json" } });
		expect(hideAnswerMessages([unreadable, answer]).map((row) => row.id)).toEqual(["q", "ans"]);
	});

	it("stay when they only look like answers", () => {
		const typed: TimelineRow = { kind: "user", id: "typed", text: "[answers]\nsee above", turnId: "turn_1" };
		expect(hideAnswerMessages([question, typed]).map((row) => row.id)).toEqual(["q", "typed"]);
	});

	it("stay when their own question can't show itself, even after an earlier answer", () => {
		// Hiding the first answer clears the flag, so the second answer is judged
		// against its own question row — one that can't show its questions — and
		// stays.
		const unreadable = step("q2", "ask_user", { detail: { arguments: "{not json" } });
		const second: TimelineRow = {
			kind: "user",
			id: "ans2",
			text: '[answers]\n1. [Choice] → "Drop them"',
			turnId: "turn_1",
		};
		expect(hideAnswerMessages([question, answer, unreadable, second]).map((row) => row.id)).toEqual([
			"q",
			"q2",
			"ans2",
		]);
	});

	it("leave every other row alone", () => {
		const rows: TimelineRow[] = [
			question,
			user("u"),
			{ kind: "user", id: "plain", text: "[answers] are here", turnId: "turn_1" },
			reply("r"),
		];
		expect(hideAnswerMessages(rows)).toEqual(rows);
	});
});

describe("the answer you gave a question", () => {
	const asked = {
		id: "ask-1",
		turnId: "turn_1",
		type: "commandExecution",
		toolName: "ask_user",
		text: "",
		argumentsJSON: JSON.stringify({
			questions: [{ header: "Choice", question: "Keep or drop?", options: [{ label: "Drop them", detail: "" }] }],
		}),
	};
	const answered = { id: "ans-1", turnId: "turn_2", type: "userMessage", text: '[answers]\n1. [Choice] → "Drop them"' };
	const model = (items: unknown[]) =>
		({ turns: [{ id: "turn_1", items }] }) as unknown as Parameters<typeof answerTo>[0];

	// The answer keeps the wire's own words, a chosen option quoted as the web
	// shows it: "You answered: "Drop them"".
	it("reads your answer, without the package's own framing", () => {
		expect(answerTo(model([asked, answered]), "ask-1")).toBe('"Drop them"');
	});

	it("is nothing before you answer, or for a row it can't find", () => {
		expect(answerTo(model([asked]), "ask-1")).toBeUndefined();
		expect(answerTo(model([asked, answered]), "gone")).toBeUndefined();
		expect(answerTo(null, "ask-1")).toBeUndefined();
	});
});
