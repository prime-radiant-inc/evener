import { describe, expect, it } from "vitest";
import { parseActivityTree } from "@evener/appwire-client";
import {
	flattenActivity,
	flattenSubagents,
	shellJobMeta,
	subagentWhy,
	tallySubagents,
	subagentLastLine,
} from "../subagents/subagentModel";
import {
	createDemoDocuments,
	type DemoCoordinator,
	demoActivityTree,
	demoTokens,
	SETTLE_RACE_PLAN,
	SETTLE_RACE_PLAN_REVISED,
} from "./demoSubagents";

const NOW = Date.UTC(2026, 8, 26, 12, 0, 0);
const MIN = 60;
// The spec's example swarm (spec 9; data.js's s-pr2138): two failures, one of
// them with a running child, 31 more running, and 21 done.
const coordinator: DemoCoordinator = {
	ref: "local:s-pr2138",
	title: "Get PR 2138 Test Clean",
	model: "glm-5.3-vision",
	subagentRef: (id) => `local:${id}`,
	jobs: [{ id: "pr-build", command: "go build ./...", ago: 12 * MIN, elapsed: 40 }],
	subagents: [
		{
			id: "g-settle",
			title: "Fix race in tree settle",
			state: "failed",
			model: "glm-5.3-vision",
			lane: "fix-settle-race",
			ago: 6 * MIN,
			elapsed: 21 * MIN,
			tokens: "1.2M",
			line: "Failed: go test exited 1 (3 times)",
			children: [
				{
					id: "g-settle-1",
					title: "Check drain ordering in tests",
					state: "running",
					model: "deepseek-4.1-flash",
					ago: 20,
					elapsed: 4 * MIN,
					tokens: "210K",
					line: "Reading agent/retirement_test.go",
				},
			],
		},
		{
			id: "g-repro",
			title: "Reproduce TestRetirementTreeSettleDrains",
			state: "failed",
			ago: 9 * MIN,
			line: "Failed: could not reproduce in 200 runs",
		},
		...Array.from({ length: 31 }, (_, index) => ({
			id: `g-run-${index}`,
			title: `Running subagent ${index}`,
			state: "running" as const,
			ago: 5,
			line: index % 6 === 0 ? "Running go test ./agent/..." : "Thinking",
		})),
		...Array.from({ length: 21 }, (_, index) => ({
			id: `g-done-${index}`,
			title: `Done subagent ${index}`,
			state: "done" as const,
			ago: 10 * MIN,
			line: "Tests pass",
		})),
	],
};

describe("the demo fleet's subagents", () => {
	it("serves the spec's example tree, which the phone reads as 55 subagents", () => {
		const tree = parseActivityTree(demoActivityTree(coordinator, NOW).data);
		expect(tree).not.toBeNull();
		const rows = flattenSubagents(tree as NonNullable<typeof tree>);
		expect(rows).toHaveLength(55);
		expect(tallySubagents(rows)).toEqual({ total: 55, failed: 2, running: 32, done: 21 });
		const settle = rows.find((row) => row.id === "g-settle");
		expect(subagentWhy(settle as NonNullable<typeof settle>, NOW)).toEqual({
			word: "Failed",
			text: "go test exited 1 (3 times)",
		});
		expect(subagentLastLine(settle as NonNullable<typeof settle>, coordinator.model, (model) => model)).toEqual({
			branch: "fix-settle-race",
			tokens: "1.2M tokens",
		});
		expect(rows.find((row) => row.id === "g-settle-1")?.parentTitle).toBe("Fix race in tree settle");
		expect(subagentWhy(rows.find((row) => row.id === "g-run-0") as NonNullable<(typeof rows)[number]>, NOW)).toEqual({
			text: "Running go test ./agent/...",
		});
	});

	// A transcript's subagent row finds its outcome in this tree by the id the
	// session's roster gives it (demoSessions.ts delegatesOf), as on a hub,
	// where both name the one delegate (audit G13).
	it("names each subagent by its roster id, and a finished one's report", () => {
		const tree = parseActivityTree(demoActivityTree(coordinator, NOW).data);
		const rows = flattenSubagents(tree as NonNullable<typeof tree>);
		const done = rows.find((row) => row.id === "g-done-0");
		expect(subagentWhy(done as NonNullable<typeof done>, NOW)).toEqual({ text: "Tests pass." });
	});

	// The Activity list's shell jobs: the running command a subagent's line
	// names, the command a failed one's line says exited, and the
	// coordinator's own finished ones.
	it("serves a shell job for each running command, each failed one, and the coordinator's own", () => {
		const tree = parseActivityTree(demoActivityTree(coordinator, NOW).data);
		const { jobs } = flattenActivity(tree as NonNullable<typeof tree>);
		const shown = jobs.map((row) => [row.title, row.state, row.owner, shellJobMeta(row, NOW)]);
		expect(shown).toContainEqual(["go build ./...", "done", "Get PR 2138 Test Clean", "40s"]);
		expect(shown).toContainEqual(["go test", "failed", "Fix race in tree settle", "1m"]);
		expect(shown).toContainEqual(["go test ./agent/...", "running", "Running subagent 0", "running · 42s"]);
		expect(jobs.find((row) => row.state === "failed")?.job.exitCode).toBe(1);
	});

	it("reads data.js's token labels", () => {
		expect(["1.2M", "210K", "95K", "7", "", undefined].map(demoTokens)).toEqual([1_200_000, 210_000, 95_000, 7, 0, 0]);
	});
});

describe("the demo fleet's documents", () => {
	const documents = createDemoDocuments(
		[
			{
				sessionRef: "local:s-pr2138",
				path: "docs/superpowers/plans/2026-09-25-settle-race.md",
				text: SETTLE_RACE_PLAN,
			},
		],
		"/home/jesse/git/evener",
	);
	const read = (session: string, path: string, format = "raw") =>
		documents.answerDocFile(
			new URL(
				`http://demo/doc/file?format=${format}&session=${encodeURIComponent(session)}&path=${encodeURIComponent(path)}`,
			),
		);

	it("serves a plan by a relative or an absolute path, the same on every read", () => {
		for (const path of [
			"docs/superpowers/plans/2026-09-25-settle-race.md",
			"/home/jesse/git/evener/docs/superpowers/plans/2026-09-25-settle-race.md",
			"docs/superpowers/plans/2026-09-25-settle-race.md",
		])
			expect(read("local:s-pr2138", path)).toEqual({ status: 200, body: SETTLE_RACE_PLAN });
		expect(SETTLE_RACE_PLAN_REVISED).not.toBe(SETTLE_RACE_PLAN);
	});

	it("answers as the hub does for anything else", () => {
		expect(read("local:s-pr2138", "docs/missing.md").status).toBe(404);
		expect(read("local:other", "docs/superpowers/plans/2026-09-25-settle-race.md").status).toBe(404);
		expect(read("local:s-pr2138", "docs/superpowers/plans/2026-09-25-settle-race.md", "html").status).toBe(400);
	});
});
