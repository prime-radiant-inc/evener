import { describe, expect, it } from "vitest";
import type {
	ActivityDelegate,
	ActivityEntry,
	ActivityJob,
	ActivitySessionNode,
	ActivityTree,
} from "@evener/appwire-client";
import {
	countLabel,
	endedInStop,
	flattenActivity,
	flattenSubagents,
	matchesSearch,
	sameModel,
	shellJobMeta,
	type SubagentRow,
	subagentLastLine,
	subagentSections,
	subagentState,
	subagentStateWord,
	subagentTitle,
	subagentWhy,
	stripSegments,
	subtreeStopped,
	subtreeStops,
	tallySubagents,
	timeInState,
} from "./subagentModel";

const NOW = Date.UTC(2026, 8, 26, 12, 0, 0);
const MIN = 60_000;
const ago = (ms: number) => new Date(NOW - ms).toISOString();

let jobs = 0;
function job(terminal: boolean, over: Partial<ActivityJob> = {}): ActivityJob {
	jobs += 1;
	return {
		jobId: `job-${jobs}`,
		ownerSessionId: "child",
		ownerRef: "local:child",
		type: "shell",
		status: terminal ? "completed" : "running",
		terminal,
		background: true,
		hasOutput: true,
		description: "command",
		startedAt: ago(2 * MIN),
		outputBytes: 0,
		...over,
	};
}
function session(ref: string, entries: ActivityEntry[] = []): ActivitySessionNode {
	return {
		kind: "session",
		sessionId: ref.slice(ref.indexOf(":") + 1),
		ref,
		label: ref,
		aggregate: "working",
		counts: { active: 0, failed: 0, completed: 0, complete: true },
		entries,
		branch: {},
	};
}
const entry = (delegate: ActivityDelegate): ActivityEntry => ({ kind: "delegate", delegate });
const delegate = (id: string, over: Partial<ActivityDelegate> = {}): ActivityDelegate => ({
	delegateId: id,
	childSessionId: id,
	childRef: `local:${id}`,
	type: "delegate",
	description: id,
	branch: {},
	...over,
});
const running = (id: string, over: Partial<ActivityDelegate> = {}) =>
	delegate(id, { runStartedAt: ago(4 * MIN), ...over });
const failed = (id: string, over: Partial<ActivityDelegate> = {}) =>
	delegate(id, { terminal: true, outcome: "failed", runStartedAt: ago(20 * MIN), runEndedAt: ago(6 * MIN), ...over });
const done = (id: string, over: Partial<ActivityDelegate> = {}) =>
	delegate(id, {
		terminal: true,
		outcome: "completed",
		runStartedAt: ago(30 * MIN),
		runEndedAt: ago(10 * MIN),
		...over,
	});
const tree = (...delegates: ActivityDelegate[]): ActivityTree => ({
	revision: 1,
	root: session("local:coord", delegates.map(entry)),
});
const rowOf = (d: ActivityDelegate) => flattenSubagents(tree(d))[0] as SubagentRow;

describe("a subagent's state is its own (spec 9)", () => {
	it.each([
		["running", running("a"), "running"],
		["queued, never started", delegate("q"), "running"],
		["failed", failed("b"), "failed"],
		["exhausted", failed("c", { outcome: "exhausted" }), "failed"],
		["completed", done("d"), "done"],
		["stopped", done("e", { outcome: "stopped" }), "done"],
		["cancelled", done("f", { outcome: "cancelled" }), "done"],
		["resumed after a failure", running("g", { outcome: "failed" }), "running"],
		// The daemon sets an outcome with every terminal run; a record that
		// somehow carries only a failed status still reads as failed.
		["failed, by its status alone", delegate("h", { terminal: true, status: "failed" }), "failed"],
	] as const)("%s", (_name, subject, expected) => {
		expect(subagentState(subject)).toBe(expected);
	});

	it("never takes on a child's failure: each subagent has its own row", () => {
		const parent = running("parent", { child: session("local:parent", [entry(failed("child"))]) });
		expect(flattenSubagents(tree(parent)).map((row) => [row.title, row.state])).toEqual([
			["parent", "running"],
			["child", "failed"],
		]);
	});

	it("reads a turn container by its turns", () => {
		expect(subagentState(delegate("t", { type: "turns", turns: [job(true), job(false)] }))).toBe("running");
		expect(subagentState(delegate("t", { type: "turns", turns: [job(true, { outcome: "failure" }), job(true)] }))).toBe(
			"failed",
		);
		expect(subagentState(delegate("t", { type: "turns", turns: [job(true, { outcome: "success" })] }))).toBe("done");
	});

	it("says the state in the spec's words", () => {
		expect((["running", "failed", "done"] as const).map(subagentStateWord)).toEqual(["Running", "Failed", "Done"]);
	});

	it("knows a subagent a stop ended, by its outcome or else its status", () => {
		expect(endedInStop(done("s", { outcome: "stopped" }))).toBe(true);
		expect(endedInStop(done("c", { outcome: "cancelled" }))).toBe(true);
		expect(endedInStop(delegate("u", { terminal: true, status: "cancelled" }))).toBe(true);
		expect(endedInStop(done("d"))).toBe(false);
		expect(endedInStop(failed("f"))).toBe(false);
		expect(endedInStop(running("r", { outcome: "stopped" }))).toBe(false);
	});

	it("knows a stopped subagent, or one whose subtree was stopped", () => {
		expect(rowOf(done("s", { outcome: "stopped" })).stopped).toBe(true);
		expect(rowOf(done("d")).stopped).toBe(false);
		const failedParent = failed("p", { child: session("local:p", [entry(done("c", { outcome: "cancelled" }))]) });
		expect(subtreeStopped(failedParent)).toBe(true);
		expect(subtreeStopped(failed("p"))).toBe(false);
	});

	it("counts a subagent's own stop by the same rule its row reads", () => {
		const statusOnly = delegate("u", { terminal: true, status: "cancelled" });
		expect(rowOf(statusOnly).stopped).toBe(true);
		expect(subtreeStops(statusOnly)).toBe(1);
		expect(subtreeStops(done("s", { outcome: "stopped" }))).toBe(1);
		expect(subtreeStops(running("r", { outcome: "stopped" }))).toBe(0);
		expect(subtreeStops(done("d"))).toBe(0);
	});
});

describe("one flat list", () => {
	it("walks the tree depth first, names who started a nested subagent, and lists each once", () => {
		const nested = failed("Fix race in tree settle", {
			child: session("local:settle", [entry(running("Check drain ordering in tests"))]),
		});
		const rows = flattenSubagents(tree(nested, running("Run linux -race on agent"), nested));
		expect(rows.map((row) => [row.title, row.parentTitle ?? null, row.order])).toEqual([
			["Fix race in tree settle", null, 0],
			["Check drain ordering in tests", "Fix race in tree settle", 1],
			["Run linux -race on agent", null, 2],
		]);
		expect(rows[0]?.active).toBe(true);
		expect(rows[0]?.ref).toBe("local:Fix race in tree settle");
	});

	it("titles a row with the short description, else the brief's first line, else the session", () => {
		expect(subagentTitle(delegate("x", { description: "  ", mandate: "Fix the settle race.\nThen report." }))).toBe(
			"Fix the settle race.",
		);
		expect(
			subagentTitle(
				delegate("x", { description: undefined, child: { ...session("local:x"), label: "Read the plan" } }),
			),
		).toBe("Read the plan");
		expect(subagentTitle(delegate("x", { description: undefined }))).toBe("local:x");
	});

	it("puts failed, then running, then done, each newest first by when it entered its state", () => {
		const sections = subagentSections(
			flattenSubagents(
				tree(
					failed("old failure", { runEndedAt: ago(30 * MIN) }),
					running("older run", { runStartedAt: ago(10 * MIN) }),
					failed("new failure", { runEndedAt: ago(2 * MIN) }),
					running("newer run", { runStartedAt: ago(1 * MIN) }),
					done("done early", { runEndedAt: ago(50 * MIN) }),
					done("done late", { runEndedAt: ago(5 * MIN) }),
					running("never timed", { runStartedAt: undefined }),
				),
			),
		);
		expect(sections.failed.map((row) => row.title)).toEqual(["new failure", "old failure"]);
		expect(sections.running.map((row) => row.title)).toEqual(["newer run", "older run", "never timed"]);
		expect(sections.done.map((row) => row.title)).toEqual(["done late", "done early"]);
	});
});

describe("tallies and counts (S3's fallback)", () => {
	it("counts the loaded subagents by state, and says when some couldn't be listed", () => {
		const rows = flattenSubagents(tree(running("a"), running("b"), failed("c"), done("d")));
		expect(tallySubagents(rows)).toEqual({ total: 4, running: 2, failed: 1, done: 1 });
		expect(countLabel(55, false)).toBe("55");
		expect(countLabel(55, true)).toBe("55+");
	});
});

describe("the strip", () => {
	it("sizes segments by count in the list's order, with failures never thinner than 3pt", () => {
		const segments = stripSegments({ failed: 2, running: 32, done: 21 }, 361);
		expect(segments.map((segment) => segment.state)).toEqual(["failed", "running", "done"]);
		expect(segments.reduce((sum, segment) => sum + segment.width, 0) + 2).toBeCloseTo(361);
		expect(segments[0]?.width).toBeCloseTo(13.0545, 3);
		expect(stripSegments({ failed: 1, running: 0, done: 499 }, 300)).toEqual([
			{ state: "failed", width: 3 },
			{ state: "done", width: 296 },
		]);
		expect(stripSegments({ failed: 0, running: 1, done: 999 }, 200)).toEqual([
			{ state: "running", width: 1 },
			{ state: "done", width: 198 },
		]);
	});

	it("draws no strip once nothing is running or failed", () => {
		expect(stripSegments({ failed: 0, running: 0, done: 5 }, 200)).toEqual([]);
		expect(stripSegments({ failed: 0, running: 0, done: 0 }, 200)).toEqual([]);
	});
});

describe("why lines on the fallbacks (ruling 6)", () => {
	it("names a failure's reason, with only the word taking the hue", () => {
		expect(subagentWhy(rowOf(failed("f", { reason: "go test exited 1 (3 times)\nsee the log" })), NOW)).toEqual({
			word: "Failed",
			text: "go test exited 1 (3 times)",
		});
		expect(subagentWhy(rowOf(failed("f")), NOW)).toEqual({ word: "Failed", text: "" });
	});

	// The hub's reason is a code; a failed run's cause rides beside it (#3327).
	it("names a failure by its cause, else its reason code in words", () => {
		expect(subagentWhy(rowOf(failed("f", { reason: "failed", error: "provider returned 500" })), NOW)).toEqual({
			word: "Failed",
			text: "provider returned 500",
		});
		expect(subagentWhy(rowOf(failed("f", { reason: "runtime_lost" })), NOW)).toEqual({
			word: "Failed",
			text: "runtime lost",
		});
	});

	it("opens a finished report, and says Stopped or Finished when there's nothing to quote", () => {
		expect(subagentWhy(rowOf(done("d", { message: "## Report\n**Tests pass** on both platforms." })), NOW)).toEqual({
			text: "Tests pass on both platforms.",
		});
		expect(subagentWhy(rowOf(done("d", { message: { ok: true } })), NOW)).toEqual({ text: "Finished" });
		expect(subagentWhy(rowOf(done("s", { outcome: "stopped" })), NOW)).toEqual({ text: "Stopped" });
	});

	it("says what a running subagent is doing with what the tree carries", () => {
		const commanding = running("r", {
			child: session("local:r", [{ kind: "shell", job: job(false, { command: "go test ./agent/..." }) }]),
		});
		expect(subagentWhy(rowOf(commanding), NOW)).toEqual({ text: "Running go test ./agent/..." });
		const waiting = running("w", {
			child: session("local:w", [entry(running("a")), entry(running("b")), entry(done("c"))]),
		});
		expect(subagentWhy(rowOf(waiting), NOW)).toEqual({ text: "Waiting on 2 subagents" });
		expect(subagentWhy(rowOf(running("q", { latestActivityAt: ago(4 * MIN) })), NOW)).toEqual({ text: "Quiet 4m" });
		expect(subagentWhy(rowOf(running("n", { latestActivityAt: ago(1 * MIN) })), NOW)).toEqual({ text: "Working" });
	});
});

describe("time in its state (ruling 7)", () => {
	it("is how long a running one has run, and how long since one failed or finished", () => {
		expect(timeInState(rowOf(running("r")), NOW)).toBe(4 * MIN);
		expect(timeInState(rowOf(failed("f")), NOW)).toBe(6 * MIN);
		expect(timeInState(rowOf(done("d", { runEndedAt: undefined })), NOW)).toBeNull();
	});
});

describe("the last line", () => {
	const coordinatorModel = "lunaroute/glm-5.3-vision";
	const name = (model: string) => (model === "deepseek-4.1-flash" ? "DeepSeek 4.1 Flash" : model);

	it("prints who started it, a model that differs from the coordinator's, its own branch and its tokens", () => {
		const rows = flattenSubagents(
			tree(
				failed("Fix race in tree settle", {
					resolvedModel: "glm-5.3-vision",
					worktree: { path: "/w/fix", branch: "fix-settle-race", headSha: "abc", ahead: 2, dirty: false },
					usage: { inputTokens: 1_000_000, outputTokens: 200_000, totalTokens: 1_200_000 },
					child: session("local:settle", [
						entry(
							running("Check drain ordering in tests", {
								resolvedModel: "deepseek-4.1-flash",
								usage: { inputTokens: 200_000, outputTokens: 10_000 },
							}),
						),
					]),
				}),
			),
		);
		expect(subagentLastLine(rows[0] as SubagentRow, coordinatorModel, name)).toEqual({
			branch: "fix-settle-race",
			tokens: "1.2M tokens",
		});
		expect(subagentLastLine(rows[1] as SubagentRow, coordinatorModel, name)).toEqual({
			parent: "Fix race in tree settle",
			model: "DeepSeek 4.1 Flash",
			tokens: "210K tokens",
		});
	});

	it("has no last line when nothing applies, and hides the model while the coordinator's is unknown", () => {
		expect(subagentLastLine(rowOf(running("r")), coordinatorModel, name)).toBeNull();
		expect(subagentLastLine(rowOf(running("r", { resolvedModel: "deepseek-4.1-flash" })), null, name)).toBeNull();
	});

	it("knows one model under two spellings", () => {
		expect(sameModel("lunaroute/glm-5.3-vision", "glm-5.3-vision")).toBe(true);
		expect(sameModel("GLM-5.3-Vision", "glm-5.3-vision")).toBe(true);
		expect(sameModel("gpt-5.6", "glm-5.3-vision")).toBe(false);
	});
});

it("filters by title, ignoring case and surrounding space", () => {
	const row = rowOf(running("Check drain ordering in tests"));
	expect(matchesSearch(row, "  DRAIN ")).toBe(true);
	expect(matchesSearch(row, "settle")).toBe(false);
	expect(matchesSearch(row, "")).toBe(true);
});

// Shell jobs join the Activity list (Jesse's ruling on shell jobs): each is a
// row with its state, its description, and the session or subagent that
// started it, in the same flat list by state as the subagents.
describe("shell jobs in the Activity list", () => {
	const shell = (j: ActivityJob): ActivityEntry => ({ kind: "shell", job: j });
	const activityTree = (): ActivityTree => ({
		revision: 1,
		root: session("local:coord", [
			shell(job(false, { jobId: "j-root", description: "Serving the docs", command: "npm run docs" })),
			entry(
				done("Fix race in tree settle", {
					child: session("local:fix", [
						shell(
							job(true, {
								jobId: "j-child",
								description: "",
								command: "go test ./agent/...\n# second line",
								status: "command_exited_nonzero",
								outcome: "failure",
								exitCode: 1,
								endedAt: ago(MIN),
							}),
						),
					]),
				}),
			),
		]),
	});

	it("lists every shell job with its state, its title and who started it", () => {
		const { jobs, subagents } = flattenActivity(activityTree());
		expect(subagents.map((row) => row.title)).toEqual(["Fix race in tree settle"]);
		expect(jobs.map((row) => [row.id, row.state, row.title, row.owner])).toEqual([
			["j-root", "running", "Serving the docs", "local:coord"],
			["j-child", "failed", "go test ./agent/...", "Fix race in tree settle"],
		]);
		// The subagent-only views are unchanged.
		expect(flattenSubagents(activityTree())).toEqual(subagents);
	});

	it("sorts jobs among subagents by state, and finds them by title, command or owner", () => {
		const { jobs, subagents } = flattenActivity(activityTree());
		const all = [...subagents, ...jobs];
		const sections = subagentSections(all);
		expect(sections.failed.map((row) => row.id)).toEqual(["j-child"]);
		expect(sections.running.map((row) => row.id)).toEqual(["j-root"]);
		expect(sections.done.map((row) => row.id)).toEqual(["Fix race in tree settle"]);
		expect(all.filter((row) => matchesSearch(row, "npm run")).map((row) => row.id)).toEqual(["j-root"]);
		expect(all.filter((row) => matchesSearch(row, "tree settle")).map((row) => row.id)).toEqual([
			"Fix race in tree settle",
			"j-child",
		]);
	});

	it("says a running job's status and quiet age, and a finished one's duration, in words", () => {
		const { jobs } = flattenActivity(activityTree());
		const [running, finished] = jobs;
		if (!running || !finished) throw new Error("no jobs");
		expect(shellJobMeta(running, NOW)).toBe("running · 2m");
		expect(shellJobMeta(finished, NOW)).toBe("Command failed · 1m");
	});
});
