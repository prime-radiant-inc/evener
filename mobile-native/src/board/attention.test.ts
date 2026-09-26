import { describe, expect, it } from "vitest";
import type { NavigationSessionSummary } from "@evener/appwire-client";
import {
	approvalRefs,
	bandOf,
	boardState,
	hubTime,
	lastLine,
	liveBands,
	liveSummary,
	stateWord,
	summaryText,
	usualPlace,
	whyLine,
	workingActivity,
} from "./attention";

const row = (
	ref: string,
	over: Partial<NavigationSessionSummary> = {},
): NavigationSessionSummary => ({
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
		[{ state: "awaiting" }, false, false, "finished"],
		[{ state: "awaiting" }, false, true, "idle"],
		[{ state: "idle" }, false, false, "finished"],
		[{ state: "idle", dormant: true }, false, false, "idle"],
		[{ state: "ended" }, false, false, "shutDown"],
		[{ state: "notLoaded" }, false, false, "shutDown"],
		[{ state: "ended", offline: true }, false, false, "shutDown"],
		[{ state: "active", offline: true }, false, false, "shutDown"],
		[{ state: "active", offline: true }, true, false, "shutDown"],
		[{ state: "awaiting", ask_pending: true, offline: true }, false, false, "shutDown"],
	] as const)("%o, approval %s, seen %s → %s", (over, approval, seen, expected) => {
		expect(boardState(row("s", over), approval, seen)).toBe(expected);
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

describe("approvals inferred from the needs_you section (until S2)", () => {
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

	it("puts failures first, oldest first, then the rest of Needs you oldest waiting first", () => {
		const live = [
			row("q-new", { state: "awaiting", ask_pending: true, updated_at: at(30) }),
			row("f-new", { state: "errored", updated_at: at(20) }),
			row("w-old", { state: "warning", updated_at: at(5) }),
			row("f-old", { state: "errored", updated_at: at(10) }),
		];
		const bands = liveBands(live, [], never);
		expect(bands.needsYou.map((item) => item.row.ref)).toEqual(["f-old", "f-new", "w-old", "q-new"]);
	});

	it("orders Finished and Idle newest first and keeps the hub's order for Working", () => {
		const live = [
			row("work-b", { state: "active", updated_at: at(1) }),
			row("done-old", { state: "awaiting", updated_at: at(2) }),
			row("work-a", { state: "active", updated_at: at(40) }),
			row("done-new", { state: "awaiting", updated_at: at(50) }),
			row("seen-old", { state: "idle", updated_at: at(3) }),
			row("seen-new", { state: "idle", updated_at: at(4) }),
		];
		const seen = (r: NavigationSessionSummary) => r.ref.startsWith("seen");
		const bands = liveBands(live, [], seen);
		expect(bands.working.map((item) => item.row.ref)).toEqual(["work-b", "work-a"]);
		expect(bands.finished.map((item) => item.row.ref)).toEqual(["done-new", "done-old"]);
		expect(bands.idle.map((item) => item.row.ref)).toEqual(["seen-new", "seen-old"]);
	});

	it("sorts a row with a missing or unreadable updated_at as the oldest", () => {
		const live = [
			row("done-dated", { state: "awaiting", updated_at: at(10) }),
			row("done-missing", { state: "awaiting" }),
			row("done-garbled", { state: "awaiting", updated_at: "not a time" }),
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
		expect(
			liveSummary(liveBands([row("a", { state: "active" }), row("b", { state: "errored" })], [], never)),
		).toEqual({ needsYou: 1, finished: 0, working: 1, idle: 0 });
	});

	it("says each count the spec's way", () => {
		expect(summaryText("needsYou", 1)).toBe("1 needs you");
		expect(summaryText("needsYou", 4)).toBe("4 need you");
		expect(summaryText("finished", 4)).toBe("4 finished");
		expect(summaryText("working", 9)).toBe("9 working");
		expect(summaryText("idle", 3)).toBe("3 idle");
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
		const one = row("s", { state: "active", children: [row("c", { state: "active" })] });
		expect(workingActivity(one)).toBe("Waiting on 1 subagent");
		const three = row("s", {
			state: "active",
			children: [
				row("c1", { state: "active" }),
				row("c2", { state: "active" }),
				row("c3", { state: "active" }),
				row("c4", { state: "idle" }),
			],
		});
		expect(workingActivity(three)).toBe("Waiting on 3 subagents");
		expect(workingActivity({ ...three, more_subagents: 12 })).toBe("Waiting on 3 subagents (+12 more)");
		const running = row("s", {
			state: "active",
			running_jobs: [{ job_id: "j", job_type: "shell", status: "running", command: "go test ./agent/..." }],
		});
		expect(workingActivity(running)).toBe("Running go test ./agent/...");
		expect(workingActivity(row("s", { state: "active" }))).toBe("Working");
		expect(whyLine({ row: running, state: "working" })).toEqual({ text: "Running go test ./agent/..." });
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
});
