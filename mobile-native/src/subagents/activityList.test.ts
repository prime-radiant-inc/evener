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
const shape = (items: ReturnType<typeof activityListItems>) =>
	items.map((item) =>
		item.kind === "row"
			? item.row.title
			: item.kind === "section"
				? `${item.state}:${item.count}`
				: item.kind === "doneFold"
					? `fold:${item.count}:${item.open}`
					: `missing:${item.title}`,
	);

describe("the list's items", () => {
	it("lists failed, then running, with done folded under All", () => {
		expect(shape(activityListItems(rows, { filter: "all", query: "", doneOpen: false, missing: [], coordinator }))).toEqual([
			"failed:1",
			"Fix race in tree settle",
			"running:2",
			"Check drain ordering",
			"Run linux -race",
			"fold:1:false",
		]);
	});

	it("opens done in place, and shows a filtered state as its own section", () => {
		expect(shape(activityListItems(rows, { filter: "all", query: "", doneOpen: true, missing: [], coordinator })).slice(-2)).toEqual(
			["fold:1:true", "Tests pass"],
		);
		expect(shape(activityListItems(rows, { filter: "done", query: "", doneOpen: false, missing: [], coordinator }))).toEqual([
			"done:1",
			"Tests pass",
		]);
	});

	it("counts what the search matches, drops empty sections, and ends with what couldn't be listed", () => {
		expect(
			shape(
				activityListItems(rows, { filter: "all", query: "RACE", doneOpen: false, missing: ["local:coord"], coordinator }),
			),
		).toEqual([
			"failed:1",
			"Fix race in tree settle",
			"running:1",
			"Run linux -race",
			"missing:Get PR 2138 Test Clean",
		]);
	});

	it("keys each item stably", () => {
		expect(
			activityListItems(rows, { filter: "all", query: "", doneOpen: false, missing: ["local:coord"], coordinator }).map(
				activityListKey,
			),
		).toEqual([
			"section:failed",
			activityNodeID({ kind: "delegate", delegate: d("Fix race in tree settle") }),
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
	const view = { filter: "all" as const, query: "", doneOpen: false, coordinator };
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
		const view = { query: "", doneOpen: false, missing: [] as string[], coordinator };
		expect(shape(activityListItems(all, { ...view, filter: "running" }))).toEqual([
			"running:3",
			// Newest first by when each started; the subagents carry no start.
			"Serving the docs",
			"Check drain ordering",
			"Run linux -race",
		]);
		expect(shape(activityListItems(all, { ...view, filter: "all", query: "npm" }))).toEqual([
			"running:1",
			"Serving the docs",
		]);
	});

	it("keys a job apart from any subagent", () => {
		const keys = activityListItems(all, { filter: "running", query: "", doneOpen: false, missing: [], coordinator }).map(
			activityListKey,
		);
		const jobEntry = withJobs.root.entries.find((entry) => entry.kind === "shell");
		if (!jobEntry) throw new Error("missing shell fixture");
		expect(keys).toContain(activityNodeID(jobEntry));
	});
});
