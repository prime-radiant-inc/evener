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
		expect(shape(activityListItems(rows, { filter: "all", query: "", doneOpen: false, missing: [] }))).toEqual([
			"failed:1",
			"Fix race in tree settle",
			"running:2",
			"Check drain ordering",
			"Run linux -race",
			"fold:1:false",
		]);
	});

	it("opens done in place, and shows a filtered state as its own section", () => {
		expect(shape(activityListItems(rows, { filter: "all", query: "", doneOpen: true, missing: [] })).slice(-2)).toEqual(
			["fold:1:true", "Tests pass"],
		);
		expect(shape(activityListItems(rows, { filter: "done", query: "", doneOpen: false, missing: [] }))).toEqual([
			"done:1",
			"Tests pass",
		]);
	});

	it("counts what the search matches, drops empty sections, and ends with what couldn't be listed", () => {
		expect(
			shape(
				activityListItems(rows, { filter: "all", query: "RACE", doneOpen: false, missing: ["Get PR 2138 Test Clean"] }),
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
			activityListItems(rows, { filter: "all", query: "", doneOpen: false, missing: ["x"] }).map(activityListKey),
		).toEqual([
			"section:failed",
			activityNodeID({ kind: "delegate", delegate: d("Fix race in tree settle") }),
			"section:running",
			activityNodeID({ kind: "delegate", delegate: d("Check drain ordering") }),
			activityNodeID({ kind: "delegate", delegate: d("Run linux -race") }),
			"done-fold",
			"missing:x",
		]);
	});
});

describe("what couldn't be listed", () => {
	it("says so once per title, however many branches share it", () => {
		const items = activityListItems([], {
			filter: "all",
			query: "",
			doneOpen: false,
			missing: ["Run tests", "Run tests"],
		});
		expect(items).toEqual([{ kind: "missing", title: "Run tests" }]);
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
	const all = flattenActivity(withJobs);

	it("lists a running job in the running section, and filters and finds it", () => {
		const view = { query: "", doneOpen: false, missing: [] as string[] };
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
		const keys = activityListItems(all, { filter: "running", query: "", doneOpen: false, missing: [] }).map(
			activityListKey,
		);
		const jobEntry = withJobs.root.entries.find((entry) => entry.kind === "shell");
		if (!jobEntry) throw new Error("missing shell fixture");
		expect(keys).toContain(activityNodeID(jobEntry));
	});
});
