import { describe, expect, it } from "vitest";
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { QUIET_AFTER_MS, STUCK_AFTER_MS } from "@evener/appwire-client";
import {
	AGENT_QUIET_AFTER_MS,
	approvalRefs,
	bandOf,
	boardState,
	hostLabeler,
	hubTime,
	lastLine,
	liveBands,
	liveSummary,
	quietOrWorking,
	stateWord,
	subagentChipText,
	summaryText,
	taskLine,
	usualPlace,
	whyLine,
	workingActivity,
} from "./attention";

const row = (ref: string, over: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary => ({
	ref,
	host_id: "local",
	session_id: ref,
	title: ref,
	project: "evener",
	state: "idle",
	kind: "session",
	live: true,
	children: [],
	...over,
});
const at = (minutes: number) => new Date(Date.UTC(2026, 8, 26, 12, minutes)).toISOString();
const never = () => false;

describe("a row's Board state (spec 13.1)", () => {
	it.each([
		[{ state: "errored" }, false, false, "failed"],
		[{ state: "restartRequired" }, false, false, "restartNeeded"],
		[{ state: "warning" }, false, false, "warning"],
		[{ state: "awaiting", ask_pending: true }, false, false, "question"],
		[{ state: "awaiting", ask_pending: true }, true, false, "question"],
		[{ state: "active" }, true, false, "approval"],
		[{ state: "active" }, false, false, "working"],
		// Awaiting without a question is a turn that ended on needs_response:
		// it needs you, seen or not (#4093).
		[{ state: "awaiting" }, false, false, "needsYou"],
		[{ state: "awaiting" }, false, true, "needsYou"],
		[{ state: "idle" }, false, false, "finished"],
		[{ state: "idle", dormant: true }, false, false, "idle"],
		[{ state: "ended" }, false, false, "shutDown"],
		[{ state: "notLoaded" }, false, false, "shutDown"],
		[{ state: "ended", offline: true }, false, false, "shutDown"],
		[{ state: "active", offline: true }, false, false, "shutDown"],
		[{ state: "active", offline: true }, true, false, "shutDown"],
		[{ state: "awaiting", ask_pending: true, offline: true }, false, false, "shutDown"],
		[{ state: "active", approval_pending: true }, false, false, "approval"],
		[{ state: "active", offline: true, approval_pending: true }, false, false, "shutDown"],
		[{ state: "awaiting", ask_pending: true, approval_pending: true }, false, false, "question"],
		[{ state: "errored", approval_pending: true }, false, false, "failed"],
	] as const)("%o, approval %s, seen %s → %s", (over, approval, seen, expected) => {
		expect(boardState(row("s", over), approval, seen)).toBe(expected);
	});

	it.each([
		[{ state: "errored", ask_pending: true, approval_pending: true }, "failed"],
		[{ state: "awaiting", ask_pending: true }, "question"],
		[{ state: "active", approval_pending: true }, "approval"],
		[{ state: "errored", offline: true, ask_pending: true, approval_pending: true }, "shutDown"],
		[{ state: "active", ask_pending: true }, "working"],
	] as const)("keeps own-session attention %o with failed delegates", (over, expected) => {
		expect(boardState(row("s", { ...over, subagents: { running: 0, failed: 3, done: 0 } }), false, false)).toBe(
			expected,
		);
	});

	it("keeps own errors ahead of questions and approvals while delegate failures stay outside Needs you", () => {
		const settled = { running: 0, failed: 3, done: 0 };
		const bands = liveBands(
			[
				row("question", { state: "awaiting", ask_pending: true, updated_at: at(1), subagents: settled }),
				row("error", { state: "errored", ask_pending: true, updated_at: at(8), subagents: settled }),
				row("approval", { state: "active", approval_pending: true, updated_at: at(2), subagents: settled }),
				row("working", { state: "active", ask_pending: true, subagents: settled }),
				row("offline", { state: "errored", offline: true, ask_pending: true, subagents: settled }),
			],
			[],
			never,
		);
		expect(bands.needsYou.map((item) => [item.row.ref, item.state])).toEqual([
			["error", "failed"],
			["question", "question"],
			["approval", "approval"],
		]);
		expect(bands.working.map((item) => item.row.ref)).toEqual(["working"]);
	});

	it.each([
		[{ state: "idle" }, false, false, "working"],
		[{ state: "awaiting" }, false, true, "needsYou"],
		[{ state: "idle", dormant: true }, false, false, "working"],
		[{ state: "warning" }, false, false, "working"],
		[{ state: "warning", ask_pending: true }, false, false, "warning"],
		[{ state: "warning", approval_pending: true }, false, false, "warning"],
		[{ state: "warning" }, true, false, "warning"],
		[{ state: "awaiting", ask_pending: true }, false, false, "question"],
		[{ state: "idle", approval_pending: true }, false, false, "approval"],
		[{ state: "idle" }, true, false, "approval"],
		[{ state: "errored" }, false, false, "failed"],
		[{ state: "restartRequired" }, false, false, "restartNeeded"],
		[{ state: "idle", offline: true }, false, false, "shutDown"],
		[{ state: "ended" }, false, false, "shutDown"],
		[{ state: "notLoaded" }, false, false, "shutDown"],
		[{ state: "idle", live: false }, false, false, "finished"],
		[{ state: "idle", kind: "subagent" }, false, false, "finished"],
		[{ state: "idle", kind: "fork" }, false, false, "finished"],
		[{ state: "idle", kind: "cluster" }, false, false, "finished"],
		[{ state: "active", kind: "subagent" }, false, true, "working"],
	] as const)("mixed running/failed children, %o, approval %s, seen %s → %s", (over, approval, seen, expected) => {
		const parent = row("s", { subagents: { running: 1, failed: 1, done: 0 }, ...over });
		expect(boardState(parent, approval, seen)).toBe(expected);
	});

	it.each([
		[{ subagents: { running: 1, failed: 0, done: 0 } }, false, "working"],
		[{ subagents: { running: 0, failed: 1, done: 1 } }, false, "finished"],
		[{ subagents: { running: 0, failed: 1, done: 1 } }, true, "idle"],
		[{ state: "errored", subagents: { running: 0, failed: 1, done: 1 } }, false, "failed"],
		[{}, false, "finished"],
		[{}, true, "idle"],
		[{ children: Array.of(row("child", { state: "active", kind: "subagent" })) }, false, "finished"],
	] as const)("only the compact live tally contributes work, %o, seen %s → %s", (over, seen, expected) => {
		expect(boardState(row("s", over), false, seen)).toBe(expected);
	});

	it.each([
		["failed", "Failed"],
		["question", "Question"],
		["approval", "Approval"],
		["warning", "Warning"],
		["restartNeeded", "Restart needed"],
		["working", "Working"],
		["finished", "Finished"],
		["idle", "Idle"],
		["shutDown", "Shut down"],
	] as const)("names %s as %s", (state, word) => {
		expect(stateWord(state)).toBe(word);
	});
});

describe("hub timestamps", () => {
	it("reads the hub's ISO time and nothing else", () => {
		expect(hubTime(at(5))).toBe(Date.UTC(2026, 8, 26, 12, 5));
		expect(hubTime(undefined)).toBeNull();
		expect(hubTime(null)).toBeNull();
		expect(hubTime("")).toBeNull();
		expect(hubTime("not a time")).toBeNull();
	});
});

describe("approvals inferred from the needs_you section", () => {
	it("is every row there for no reason its state gives", () => {
		const section = [
			row("a", { state: "awaiting" }),
			row("b", { state: "warning" }),
			row("c", { state: "restartRequired" }),
			row("d", { state: "errored" }),
			row("e", { state: "active" }),
			row("f", { state: "idle" }),
		];
		expect([...approvalRefs(section)].sort()).toEqual(["e", "f"]);
	});
});

describe("Live bands (spec 7.1)", () => {
	it.each([
		["failed", "needsYou"],
		["question", "needsYou"],
		["approval", "needsYou"],
		["warning", "needsYou"],
		["restartNeeded", "needsYou"],
		["working", "working"],
		["finished", "finished"],
		["idle", "idle"],
		["shutDown", null],
	] as const)("%s → %s", (state, band) => {
		expect(bandOf(state)).toBe(band);
	});

	it("sorts Needs you by failed, then question or approval, then warning or restart-needed, then oldest first", () => {
		// Every row is newer than each row in the bands below it, ages interleave
		// within a band, and the rows arrive newest first: age order, arrival
		// order and any merged or split band would each give a different sequence.
		const live = [
			row("f-new", { state: "errored", updated_at: at(8) }),
			row("f-old", { state: "errored", updated_at: at(7) }),
			row("q-new", { state: "awaiting", ask_pending: true, updated_at: at(6) }),
			row("a", { state: "active", approval_pending: true, updated_at: at(5) }),
			row("q-old", { state: "awaiting", ask_pending: true, updated_at: at(4) }),
			row("w-new", { state: "warning", updated_at: at(3) }),
			row("r", { state: "restartRequired", updated_at: at(2) }),
			row("w-old", { state: "warning", updated_at: at(1) }),
		];
		const needsYou = liveBands(live, [], never).needsYou.map((item) => item.row.ref);
		expect(needsYou).toEqual(["f-old", "f-new", "q-old", "a", "q-new", "w-old", "r", "w-new"]);
	});

	// boardState's mark precedence returns "warning"/"restartNeeded" for these
	// rows before it ever looks at ask_pending/approval_pending, so ranking by
	// mark alone would bury a row the hub still counts as blocked on you under
	// every plain warning or restart-needed row, however old. The band must
	// read the flags directly instead.
	it("a warning that also carries ask_pending sorts in the middle band, above an older plain warning", () => {
		const bands = liveBands(
			[
				row("w-plain", { state: "warning", updated_at: at(5) }),
				row("w-asking", { state: "warning", ask_pending: true, updated_at: at(30) }),
			],
			[],
			never,
		);
		expect(bands.needsYou.map((item) => item.row.ref)).toEqual(["w-asking", "w-plain"]);
		// The mark itself is untouched: it still reads Warning, not Question.
		expect(bands.needsYou.find((item) => item.row.ref === "w-asking")?.state).toBe("warning");
	});

	it("a restart-needed row that also carries approval_pending sorts in the middle band, above an older plain restart-needed", () => {
		const bands = liveBands(
			[
				row("r-plain", { state: "restartRequired", updated_at: at(5) }),
				row("r-approving", { state: "restartRequired", approval_pending: true, updated_at: at(30) }),
			],
			[],
			never,
		);
		expect(bands.needsYou.map((item) => item.row.ref)).toEqual(["r-approving", "r-plain"]);
		expect(bands.needsYou.find((item) => item.row.ref === "r-approving")?.state).toBe("restartNeeded");
	});

	// A hub that predates S2a never sends approval_pending; approvalRefs infers
	// the approval from needs_you section membership instead (the row's raw
	// approval_pending is absent). That inferred approval must still land in
	// the middle band, not fall to "everything else" for lack of the raw flag.
	it("an approval inferred from the needs_you section (no raw approval_pending) still sorts in the middle band", () => {
		const bands = liveBands(
			[row("w", { state: "warning", updated_at: at(30) }), row("inferred", { state: "active", updated_at: at(1) })],
			[row("inferred", { state: "active" })],
			never,
		);
		expect(bands.needsYou.map((item) => item.row.ref)).toEqual(["inferred", "w"]);
	});

	it("orders Finished and Idle newest first and keeps the hub's order for Working", () => {
		const live = [
			row("work-b", { state: "active", updated_at: at(1) }),
			row("done-old", { state: "idle", updated_at: at(2) }),
			row("work-a", { state: "active", updated_at: at(40) }),
			row("done-new", { state: "idle", updated_at: at(50) }),
			row("seen-old", { state: "idle", updated_at: at(3) }),
			row("seen-new", { state: "idle", updated_at: at(4) }),
		];
		const seen = (r: NavigationSessionSummary) => r.ref.startsWith("seen");
		const bands = liveBands(live, [], seen);
		expect(bands.working.map((item) => item.row.ref)).toEqual(["work-b", "work-a"]);
		expect(bands.finished.map((item) => item.row.ref)).toEqual(["done-new", "done-old"]);
		expect(bands.idle.map((item) => item.row.ref)).toEqual(["seen-new", "seen-old"]);
	});

	it("floats a may-be-stuck session to the top of Working when isStuck is given (spec 7.1, S5)", () => {
		const live = [
			row("work-a", { state: "active", updated_at: at(1) }),
			row("work-b-stuck", { state: "active", updated_at: at(2) }),
			row("work-c", { state: "active", updated_at: at(3) }),
			row("work-d-stuck", { state: "active", updated_at: at(4) }),
		];
		const isStuck = (r: NavigationSessionSummary) => r.ref.endsWith("-stuck");
		const bands = liveBands(live, [], () => false, isStuck);
		// Both stuck rows float up, keeping their own relative (hub) order;
		// the rest keep theirs too.
		expect(bands.working.map((item) => item.row.ref)).toEqual(["work-b-stuck", "work-d-stuck", "work-a", "work-c"]);
	});

	it("keeps the hub's order for Working when isStuck is omitted, same as before S5", () => {
		const live = [
			row("work-a", { state: "active", updated_at: at(1) }),
			row("work-b", { state: "active", updated_at: at(2) }),
		];
		expect(liveBands(live, [], () => false).working.map((item) => item.row.ref)).toEqual(["work-a", "work-b"]);
	});

	it("orders Finished and Idle by when the turn ended, falling back to updated_at, and leaves Needs you alone", () => {
		const live = [
			// Renamed lately, but its turn ended long ago.
			row("done-renamed", { state: "idle", updated_at: at(59), turn_ended_at: at(10), unseen: true }),
			row("done-ended", { state: "idle", updated_at: at(20), turn_ended_at: at(30), unseen: true }),
			row("done-older-hub", { state: "idle", updated_at: at(20) }),
			row("seen-renamed", { state: "idle", updated_at: at(58), turn_ended_at: at(1) }),
			row("seen-ended", { state: "idle", updated_at: at(2), turn_ended_at: at(40) }),
			row("seen-garbled", { state: "idle", updated_at: at(5), turn_ended_at: "not a time" }),
			row("f-ended-late", { state: "errored", updated_at: at(5), turn_ended_at: at(50) }),
			row("f-ended-early", { state: "errored", updated_at: at(6), turn_ended_at: at(1) }),
		];
		const seen = (r: NavigationSessionSummary) => r.ref.startsWith("seen");
		const bands = liveBands(live, [], seen);
		expect(bands.finished.map((item) => item.row.ref)).toEqual(["done-ended", "done-older-hub", "done-renamed"]);
		expect(bands.idle.map((item) => item.row.ref)).toEqual(["seen-ended", "seen-garbled", "seen-renamed"]);
		expect(bands.needsYou.map((item) => item.row.ref)).toEqual(["f-ended-late", "f-ended-early"]);
	});

	it("sorts a row with a missing or unreadable updated_at as the oldest", () => {
		const live = [
			row("done-dated", { state: "idle", updated_at: at(10) }),
			row("done-missing", { state: "idle" }),
			row("done-garbled", { state: "idle", updated_at: "not a time" }),
			row("failed-dated", { state: "errored", updated_at: at(10) }),
			row("failed-missing", { state: "errored" }),
		];
		const bands = liveBands(live, [], never);
		expect(bands.finished.map((item) => item.row.ref)).toEqual(["done-dated", "done-garbled", "done-missing"]);
		expect(bands.needsYou.map((item) => item.row.ref)).toEqual(["failed-missing", "failed-dated"]);
	});

	it("shows a session that needs you even when it sits past the Live pages loaded so far", () => {
		const bands = liveBands(
			[row("loaded", { state: "active" })],
			[row("later", { state: "awaiting", ask_pending: true })],
			never,
		);
		expect(bands.needsYou.map((item) => item.row.ref)).toEqual(["later"]);
	});

	it("marks an approval from the section on the Live copy of the row", () => {
		const bands = liveBands(
			[row("x", { state: "active", children: [row("child", { state: "active" })] })],
			[row("x", { state: "active" })],
			never,
		);
		expect(bands.needsYou).toHaveLength(1);
		expect(bands.needsYou[0]?.state).toBe("approval");
		expect(bands.needsYou[0]?.row.children).toHaveLength(1);
	});

	it("leaves shut-down rows to Projects", () => {
		const bands = liveBands([row("gone", { state: "ended" })], [], never);
		expect(Object.values(bands).flat()).toEqual([]);
	});
});

describe("the Live summary line", () => {
	it("shows only when at least two bands have sessions", () => {
		expect(liveSummary(liveBands([row("a", { state: "active" })], [], never))).toBeNull();
		expect(liveSummary(liveBands([row("a", { state: "active" }), row("b", { state: "errored" })], [], never))).toEqual({
			needsYou: 1,
			finished: 0,
			working: 1,
			idle: 0,
		});
	});

	it("says each count the spec's way", () => {
		expect(summaryText("needsYou", 1)).toBe("1 needs you");
		expect(summaryText("needsYou", 4)).toBe("4 need you");
		expect(summaryText("finished", 4)).toBe("4 finished");
		expect(summaryText("working", 9)).toBe("9 working");
		expect(summaryText("idle", 3)).toBe("3 idle");
	});
});

describe("a running agent's last words", () => {
	it("reads Working until 20 seconds pass without an update, then Quiet", () => {
		expect(AGENT_QUIET_AFTER_MS).toBe(20_000);
		expect(quietOrWorking(19_999)).toBe("Working");
		expect(quietOrWorking(20_000)).toBe("Quiet 20s");
	});
});

describe("why lines on the fallbacks (spec 7.2, 18)", () => {
	it.each([
		["failed", { word: "Failed", hue: "danger", text: "open the session to see what went wrong" }],
		["question", { word: "Question", hue: "attention", text: "waiting for your answer" }],
		["approval", { word: "Approval", hue: "attention", text: "waiting for your permission" }],
		["warning", { word: "Warning", hue: "attention", text: "open the session to see it" }],
		[
			"restartNeeded",
			{ word: "Restart needed", hue: "attention", text: "restart this session to pick up the hub's update" },
		],
	] as const)("%s", (state, expected) => {
		expect(whyLine({ row: row("s"), state })).toEqual(expected);
	});

	it("has none for Finished (the excerpt is S1) or Idle", () => {
		expect(whyLine({ row: row("s"), state: "finished" })).toBeNull();
		expect(whyLine({ row: row("s"), state: "idle" })).toBeNull();
	});

	it("says what a working session is doing with what the row carries", () => {
		const one = row("s", { state: "active", subagents: { running: 1, failed: 0, done: 0 } });
		expect(workingActivity(one)).toBe("Waiting on 1 subagent");
		const three = row("s", { state: "active", subagents: { running: 3, failed: 0, done: 0 } });
		expect(workingActivity(three)).toBe("Waiting on 3 subagents");
		const running = row("s", {
			state: "active",
			running_job_count: 1,
			running_job_command: "go test ./agent/...",
		});
		expect(workingActivity(running)).toBe("Running go test ./agent/...");
		// A settled tally (no running) leaves the command or Working to say it.
		expect(workingActivity(row("s", { state: "active", subagents: { running: 0, failed: 1, done: 11 } }))).toBe(
			"Working",
		);
		expect(workingActivity(row("s", { state: "active" }))).toBe("Working");
		expect(whyLine({ row: running, state: "working" })).toEqual({ text: "Running go test ./agent/..." });
	});

	it("words only running subagents in the native chip", () => {
		const cases: Array<[{ running: number; failed: number }, string]> = [
			[{ running: 2, failed: 0 }, "2 running"],
			[{ running: 0, failed: 3 }, ""],
			[{ running: 2, failed: 3 }, "2 running"],
		];
		for (const [tally, expected] of cases) expect(subagentChipText(tally)).toBe(expected);
	});
});

describe("the working why line reads S5's activity (spec 7.1, 13.1)", () => {
	const minutes = [0, 0, 0, 0, 0, 0, 0];
	const working = row("s", { state: "active" });

	it("trusts the activity read's subagent tally over the row's own tally", () => {
		// The row's own tally is a fallback the read replaces: the read (every
		// depth, S3) says what is really running, and activity wins.
		const stale = row("s", { state: "active", subagents: { running: 1, failed: 0, done: 0 } });
		const activity = { ref: "s", minutes, runningSubagents: 3 };
		expect(whyLine({ row: stale, state: "working" }, activity, 0)).toEqual({
			text: "Waiting on 3 subagents",
		});
		expect(whyLine({ row: working, state: "working" }, { ...activity, runningSubagents: 1 }, 0)).toEqual({
			text: "Waiting on 1 subagent",
		});
	});

	it("reads Quiet after three minutes and May be stuck (in the attention tone) after ten", () => {
		const quiet = { ref: "s", minutes, runningSubagents: 0, quietForMs: QUIET_AFTER_MS };
		expect(whyLine({ row: working, state: "working" }, quiet, 0)).toEqual({ text: "Quiet 3m" });
		const stuck = { ref: "s", minutes, runningSubagents: 0, quietForMs: STUCK_AFTER_MS };
		expect(whyLine({ row: working, state: "working" }, stuck, 0)).toEqual({
			text: "May be stuck · no updates for 10m",
			stuck: true,
		});
	});

	it("counts time since the read toward the quiet duration shown", () => {
		const activity = { ref: "s", minutes, runningSubagents: 0, quietForMs: 2 * 60_000 };
		expect(whyLine({ row: working, state: "working" }, activity, 60_000)).toEqual({ text: "Quiet 3m" });
	});

	it("never shows a stuck or quiet label while subagents are running (Jesse's ruling)", () => {
		const activity = { ref: "s", minutes, runningSubagents: 1, quietForMs: 15 * 60_000 };
		expect(whyLine({ row: working, state: "working" }, activity, 0)).toEqual({ text: "Waiting on 1 subagent" });
	});

	it("falls back to the command, or Working, once activity says nothing is running and it isn't quiet yet", () => {
		const activity = { ref: "s", minutes, runningSubagents: 0 };
		expect(whyLine({ row: working, state: "working" }, activity, 0)).toEqual({ text: "Working" });
		const running = row("s", {
			state: "active",
			running_job_count: 1,
			running_job_command: "go test ./agent/...",
		});
		expect(whyLine({ row: running, state: "working" }, activity, 0)).toEqual({ text: "Running go test ./agent/..." });
	});

	it("never lets the row's own subagent tally override an activity read of zero", () => {
		// Without S5 data, this row would read "Waiting on 1 subagent" (its own
		// tally) - once a real read says zero are running, that must win.
		const stale = row("s", { state: "active", subagents: { running: 1, failed: 0, done: 0 } });
		const activity = { ref: "s", minutes, runningSubagents: 0 };
		expect(whyLine({ row: stale, state: "working" }, activity, 0)).toEqual({ text: "Working" });
	});

	it("says what the session last set out to do, over the job it is running", () => {
		const intent = { ref: "s", minutes, runningSubagents: 0, latestIntent: "Reading the board's row tests." };
		expect(whyLine({ row: working, state: "working" }, intent, 0)).toEqual({
			text: "Reading the board's row tests.",
		});
		// The session's own words lead the job it is running: they say what the
		// job is for. A read that states none leaves the job to say itself, and
		// a session waiting on subagents, quiet or stuck says so first.
		const running = row("s", { state: "active", running_job_count: 1, running_job_command: "go test ./agent/..." });
		expect(whyLine({ row: running, state: "working" }, intent, 0)).toEqual({ text: "Reading the board's row tests." });
		expect(whyLine({ row: running, state: "working" }, { ref: "s", minutes, runningSubagents: 0 }, 0)).toEqual({
			text: "Running go test ./agent/...",
		});
		expect(whyLine({ row: working, state: "working" }, { ...intent, runningSubagents: 1 }, 0)).toEqual({
			text: "Waiting on 1 subagent",
		});
		expect(whyLine({ row: working, state: "working" }, { ...intent, quietForMs: QUIET_AFTER_MS }, 0)).toEqual({
			text: "Quiet 3m",
		});
		// No read, no intent: a row before the first poll keeps its own facts.
		expect(whyLine({ row: working, state: "working" })).toEqual({ text: "Working" });
	});

	it("keeps the pre-S5 fallback (the row's own tally and jobs) when there is no activity read at all", () => {
		const stale = row("s", { state: "active", subagents: { running: 1, failed: 0, done: 0 } });
		expect(whyLine({ row: stale, state: "working" })).toEqual({ text: "Waiting on 1 subagent" });
	});
});

describe("the task line (spec 7.2, S13)", () => {
	it("names the task now in progress by the hub's own position for it", () => {
		const unfinished = row("s", {
			tasks: { total: 7, done: 3, current_id: 4, current: "Fix the settle/drain race" },
		});
		expect(taskLine(unfinished)).toBe("Task 4 of 7 · Fix the settle/drain race");
	});

	it("trusts current_id over done/cancelled when a later task already settled out of order", () => {
		// 3 tasks are settled (done or cancelled), but the one still in progress
		// is task 2, not task 4: dependency-driven completion can settle a
		// later task before an earlier one. done + cancelled + 1 would say 4.
		const outOfOrder = row("s", {
			tasks: { total: 7, done: 2, cancelled: 1, current_id: 2, current: "Fix the settle/drain race" },
		});
		expect(taskLine(outOfOrder)).toBe("Task 2 of 7 · Fix the settle/drain race");
	});

	it("falls back to counting done and cancelled tasks when the hub omits current_id", () => {
		const withCancellation = row("s", {
			tasks: { total: 4, done: 2, cancelled: 1, current: "Cap retries per host" },
		});
		expect(taskLine(withCancellation)).toBe("Task 4 of 4 · Cap retries per host");
	});

	it("has no line once every task is done or cancelled (current absent)", () => {
		const finished = row("s", { tasks: { total: 3, done: 2, cancelled: 1 } });
		expect(taskLine(finished)).toBeNull();
	});

	it("has no line before any task starts (current absent, nothing done yet)", () => {
		const notStarted = row("s", { tasks: { total: 5, done: 0 } });
		expect(taskLine(notStarted)).toBeNull();
	});

	it("has no line for a session with no task list", () => {
		expect(taskLine(row("s"))).toBeNull();
	});
});

describe("the last line prints project and host only when unusual", () => {
	const fleet = [
		row("a", { project: "evener", host_id: "local" }),
		row("b", { project: "evener", host_id: "local" }),
		row("c", { project: "docs", host_id: "paradise-park" }),
	];
	const label = (id: string) => (id === "paradise-park" ? "paradise-park" : "this host");

	it("finds the fleet's usual project and host", () => {
		expect(usualPlace(fleet)).toEqual({ project: "evener", host: "local" });
	});

	it("prints what differs and nothing when nothing does", () => {
		const usual = usualPlace(fleet);
		expect(lastLine(fleet[0] as NavigationSessionSummary, usual, label)).toBeNull();
		expect(lastLine(fleet[2] as NavigationSessionSummary, usual, label)).toEqual({
			project: "docs",
			host: "paradise-park",
		});
	});

	it("carries task progress alongside an unusual project and host", () => {
		const usual = usualPlace(fleet);
		const withTask = row("d", {
			project: "docs",
			host_id: "paradise-park",
			tasks: { total: 7, done: 3, current: "Fix the settle/drain race" },
		});
		expect(lastLine(withTask, usual, label)).toEqual({
			task: "Task 4 of 7 · Fix the settle/drain race",
			project: "docs",
			host: "paradise-park",
		});
	});

	it("has no last line when the task list is finished and the project and host are usual", () => {
		const usual = usualPlace(fleet);
		const finished = row("a", { tasks: { total: 2, done: 2 } });
		expect(lastLine(finished, usual, label)).toBeNull();
	});
});

// "Show model on Board rows" (spec 7.2, 12): the model's display name ends
// the last line when the setting is on and the row carries one (S17).
describe("the last line's model", () => {
	const fleet = [row("a", { model_name: "GLM 5.3 Vision" }), row("b"), row("c")];
	const label = (id: string) => id;

	it("ends the line with the model when the setting is on", () => {
		const withTask = row("d", { model_name: "GPT-5", tasks: { total: 7, done: 3, current: "Fix it" } });
		expect(lastLine(withTask, usualPlace(fleet), label, true)).toEqual({
			task: "Task 4 of 7 · Fix it",
			model: "GPT-5",
		});
		expect(lastLine(fleet[0] as NavigationSessionSummary, usualPlace(fleet), label, true)).toEqual({
			model: "GLM 5.3 Vision",
		});
	});

	it("leaves the model out when the setting is off or the row has none", () => {
		expect(lastLine(fleet[0] as NavigationSessionSummary, usualPlace(fleet), label, false)).toBeNull();
		expect(lastLine(fleet[1] as NavigationSessionSummary, usualPlace(fleet), label, true)).toBeNull();
	});
});

describe("host labels from the manifest's sources", () => {
	const sources = [{ id: "local", label: "Laptop", kind: "local", online: true }];

	it("names a host by its source's label, and any other by its id", () => {
		const label = hostLabeler(sources);
		expect(label("local")).toBe("Laptop");
		expect(label("paradise-park")).toBe("paradise-park");
		expect(hostLabeler(undefined)("local")).toBe("local");
	});

	it("hands a host the manifest doesn't name to the fallback", () => {
		const label = hostLabeler(sources, (id) => `host ${id}`);
		expect(label("local")).toBe("Laptop");
		expect(label("paradise-park")).toBe("host paradise-park");
	});
});
