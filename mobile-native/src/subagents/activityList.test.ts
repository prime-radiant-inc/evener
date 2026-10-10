import { describe, expect, it } from "vitest";
import { activityNodeID, type ActivityDelegate, type ActivityTree } from "@evener/appwire-client";
import { activityListItems, activityListKey } from "./activityList";
import { flattenActivity, flattenSubagents } from "./subagentModel";

const d = (id: string, over: Partial<ActivityDelegate> = {}): ActivityDelegate => ({
	delegateId: id,
	childSessionId: id,
	childRef: `local:${id}`,
	type: "delegate",
	description: id,
	branch: {},
	...over,
});
const tree: ActivityTree = {
	revision: 1,
	root: {
		kind: "session",
		sessionId: "coord",
		ref: "local:coord",
		label: "Get PR 2138 Test Clean",
		aggregate: "working",
		counts: { active: 0, failed: 0, completed: 0, complete: true },
		branch: {},
		entries: [
			d("Fix race in tree settle", { terminal: true, outcome: "failed" }),
			d("Check drain ordering"),
			d("Run linux -race"),
			d("Tests pass", { terminal: true, outcome: "completed" }),
		].map((delegate) => ({ kind: "delegate" as const, delegate })),
	},
};
const rows = flattenSubagents(tree);
const coordinator = { ref: "local:coord", title: "Get PR 2138 Test Clean" };
// The view each test starts from, naming only what it changes.
const view = {
	filter: "all" as const,
	query: "",
	doneOpen: false,
	completedJobsOpen: false,
	missing: [] as string[],
	coordinator,
};
const shape = (items: ReturnType<typeof activityListItems>) =>
	items.map((item) =>
		item.kind === "row"
			? item.row.title
			: item.kind === "section"
				? `${item.state}:${item.count}`
				: item.kind === "doneFold"
					? `fold:${item.count}:${item.open}`
					: item.kind === "completedJobsFold"
						? `jobs-fold:${item.count}:${item.open}`
						: `missing:${item.title}`,
	);

describe("the list's items", () => {
	it("keeps live work above collapsed terminal delegate history", () => {
		expect(shape(activityListItems(rows, view))).toEqual([
			"running:2",
			"Check drain ordering",
			"Run linux -race",
			"fold:2:false",
		]);
	});

	it("opens every terminal outcome in place, and Done reveals history without another disclosure", () => {
		expect(shape(activityListItems(rows, { ...view, doneOpen: true })).slice(-3)).toEqual([
			"fold:2:true",
			"Fix race in tree settle",
			"Tests pass",
		]);
		expect(shape(activityListItems(rows, { ...view, filter: "done" }))).toEqual([
			"done:2",
			"Fix race in tree settle",
			"Tests pass",
		]);
	});

	it("counts what the search matches, drops empty sections, and ends with what couldn't be listed", () => {
		expect(shape(activityListItems(rows, { ...view, query: "RACE", missing: ["local:coord"] }))).toEqual([
			"running:1",
			"Run linux -race",
			"fold:1:false",
			"missing:Get PR 2138 Test Clean",
		]);
	});

	it("keys each item stably", () => {
		expect(activityListItems(rows, { ...view, missing: ["local:coord"] }).map(activityListKey)).toEqual([
			"section:running",
			activityNodeID({ kind: "delegate", delegate: d("Check drain ordering") }),
			activityNodeID({ kind: "delegate", delegate: d("Run linux -race") }),
			"done-fold",
			"missing:Get PR 2138 Test Clean",
		]);
	});
});

// A branch is named by whose it is: the coordinator by its title, a subagent
// by its row's, never by a raw ref.
describe("what couldn't be listed", () => {
	const missingOf = (missing: string[], listed = rows) =>
		activityListItems(listed, { ...view, missing }).filter((item) => item.kind === "missing");

	it("names the coordinator by its title and a subagent by its row's title", () => {
		expect(missingOf(["local:coord", "local:Check drain ordering"])).toEqual([
			{ kind: "missing", title: "Get PR 2138 Test Clean" },
			{ kind: "missing", title: "Check drain ordering" },
		]);
	});

	it("says so once per title, however many branches share it", () => {
		const twins = flattenSubagents({
			...tree,
			root: {
				...tree.root,
				entries: [d("a", { description: "Run tests" }), d("b", { description: "Run tests" })].map((delegate) => ({
					kind: "delegate" as const,
					delegate,
				})),
			},
		});
		expect(missingOf(["local:a", "local:b"], twins)).toEqual([{ kind: "missing", title: "Run tests" }]);
	});

	// Its subagent's row is on a page not loaded yet, so there's no title to give.
	it("says once, unnamed and keyed apart from named lines, for branches whose subagents aren't loaded", () => {
		const items = missingOf(["local:unloaded", "local:also-unloaded", "local:coord"]);
		expect(items).toEqual([{ kind: "missing" }, { kind: "missing", title: "Get PR 2138 Test Clean" }]);
		expect(items.map(activityListKey)).toEqual(["missing", "missing:Get PR 2138 Test Clean"]);
	});
});

// Shell jobs join the list by state beside the subagents, and the chips and
// search filter both kinds (Jesse's ruling on shell jobs).
describe("shell jobs in the list", () => {
	const withJobs: ActivityTree = {
		...tree,
		root: {
			...tree.root,
			entries: [
				...tree.root.entries,
				{
					kind: "shell",
					job: {
						jobId: "job-docs",
						ownerSessionId: "coord",
						ownerRef: "local:coord",
						type: "shell",
						status: "running",
						terminal: false,
						background: true,
						hasOutput: true,
						description: "Serving the docs",
						command: "npm run docs",
						startedAt: new Date(0).toISOString(),
						outputBytes: 0,
					},
				},
			],
		},
	};
	const all = flattenActivity(withJobs, coordinator.title);

	it("lists a running job in the running section, and filters and finds it", () => {
		expect(shape(activityListItems(all, { ...view, filter: "running" }))).toEqual([
			"running:3",
			// Newest first by when each started; the subagents carry no start.
			"Serving the docs",
			"Check drain ordering",
			"Run linux -race",
		]);
		expect(shape(activityListItems(all, { ...view, query: "npm" }))).toEqual(["running:1", "Serving the docs"]);
	});

	it("keys a job apart from any subagent", () => {
		const keys = activityListItems(all, { ...view, filter: "running" }).map(activityListKey);
		const jobEntry = withJobs.root.entries.find((entry) => entry.kind === "shell");
		if (!jobEntry) throw new Error("missing shell fixture");
		expect(keys).toContain(activityNodeID(jobEntry));
	});

	const base = withJobs.root.entries.find((entry) => entry.kind === "shell");
	if (!base || base.kind !== "shell") throw new Error("missing shell fixture");
	const mixed = flattenActivity(
		{
			...withJobs,
			root: {
				...withJobs.root,
				entries: [
					...withJobs.root.entries,
					{
						kind: "shell",
						job: {
							...base.job,
							jobId: "Fix race in tree settle",
							description: "Failed command",
							terminal: true,
							status: "command_exited_nonzero",
							outcome: "failure",
							endedAt: "2026-10-03T12:02:00Z",
						},
					},
					{
						kind: "shell",
						job: {
							...base.job,
							jobId: "success",
							description: "Finished command",
							terminal: true,
							status: "completed",
							outcome: "success",
							endedAt: "2026-10-03T12:03:00Z",
						},
					},
					{
						kind: "shell",
						job: {
							...base.job,
							jobId: "stopped",
							description: "Stopped command",
							terminal: true,
							status: "cancelled",
							endedAt: "2026-10-03T12:02:00Z",
						},
					},
				],
			},
		},
		coordinator.title,
	);

	it.each([
		[false, false, []],
		[true, false, ["Fix race in tree settle", "Tests pass"]],
		[false, true, ["Finished command", "Failed command", "Stopped command"]],
		[true, true, ["Fix race in tree settle", "Tests pass", "Finished command", "Failed command", "Stopped command"]],
	])("opens delegate=%s and job=%s histories independently", (doneOpen, completedJobsOpen, titles) => {
		const items = activityListItems(mixed, { ...view, doneOpen, completedJobsOpen });
		expect(items.filter((item) => item.kind === "doneFold" || item.kind === "completedJobsFold")).toEqual([
			{ kind: "doneFold", count: 2, open: doneOpen },
			{ kind: "completedJobsFold", count: 3, open: completedJobsOpen },
		]);
		expect(
			items.flatMap((item) => (item.kind === "row" && item.row.state !== "running" ? [item.row.title] : [])),
		).toEqual(titles);
	});

	it("Done reveals both terminal histories with true outcomes and stable distinct keys", () => {
		const items = activityListItems(mixed, { ...view, filter: "done" });
		expect(shape(items)).toEqual([
			"done:2",
			"Fix race in tree settle",
			"Tests pass",
			"completed:3",
			"Finished command",
			"Failed command",
			"Stopped command",
		]);
		const visible = items.filter((item) => item.kind === "row");
		expect(visible.map((item) => item.row.state)).toEqual(["failed", "done", "done", "failed", "done"]);
		const keys = items.map(activityListKey);
		expect(new Set(keys).size).toBe(keys.length);
		expect(
			activityListItems(mixed, { ...view, doneOpen: true, completedJobsOpen: true }).map(activityListKey),
		).toContain("completed-jobs-fold");
	});

	it("search narrows each loaded history independently and keeps Running live-only", () => {
		expect(shape(activityListItems(mixed, { ...view, query: "command" }))).toEqual(["jobs-fold:3:false"]);
		expect(shape(activityListItems(mixed, { ...view, query: "command", filter: "done" }))).toEqual([
			"completed:3",
			"Finished command",
			"Failed command",
			"Stopped command",
		]);
		expect(
			activityListItems(mixed, { ...view, filter: "running" }).every(
				(item) => item.kind === "section" || (item.kind === "row" && item.row.state === "running"),
			),
		).toBe(true);
	});
});
