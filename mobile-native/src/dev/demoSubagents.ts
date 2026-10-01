// The demo fleet's subagents and documents, as the hub serves them: the
// activity tree the typed activity reads project (demoSessionActivity.ts) for
// the Activity list, and the documents the Reader opens (/doc/file). Built
// from the same raw swarm the Board's navigation rows come from
// (demoFleet.ts, after the prototype's data.js), so the Board, the list and
// the transcript agree (spec Appendix B). Each subagent's own session
// (thread/read) is demoSessions.ts's.
export interface DemoSubagent {
	id: string;
	title: string;
	state: "running" | "failed" | "done";
	/** Seconds since its last activity (running), or since it ended (data.js "ago"). */
	ago: number;
	/** Seconds it ran (data.js "elapsed"). */
	elapsed?: number;
	model?: string;
	/** Its own worktree's branch (data.js "lane"). */
	lane?: string;
	/** data.js's token label: "1.2M", "210K". */
	tokens?: string;
	/** data.js's why line: "Running go test ./agent/...", "Failed: …", "Tests pass". */
	line?: string;
	children?: DemoSubagent[];
}

/** A shell job a coordinator ran itself that has finished. */
export interface DemoShellJob {
	id: string;
	command: string;
	/** Seconds since it ended. */
	ago: number;
	/** Seconds it ran. */
	elapsed: number;
}

export interface DemoCoordinator {
	/** Its ref, "local:s-pr2138". */
	ref: string;
	title: string;
	model: string;
	subagents: readonly DemoSubagent[];
	/** Its own finished shell jobs, listed beside its subagents. */
	jobs?: readonly DemoShellJob[];
	/** Its own running command, also summarized by navigation. */
	runningJob?: { id: string; command: string };
	/** How the fleet names a subagent's own session (demoFleet.ts hostSessionRef). */
	subagentRef: (id: string) => string;
}

const DEFAULT_ELAPSED_SECONDS = 300;

/** When a subagent's run started, from data.js's elapsed: a running one's
 * counts back from now, an ended one's from when it ended. The Subagents list,
 * the coordinator's transcript and the subagent's own screen all read this
 * one fact (demoSessions.ts), so their times agree. */
export function demoRunStartedAt(sub: Pick<DemoSubagent, "state" | "ago" | "elapsed">, startupMs: number): number {
	const elapsed = (sub.elapsed ?? DEFAULT_ELAPSED_SECONDS) * 1000;
	const until = sub.state === "running" ? startupMs : startupMs - sub.ago * 1000;
	return until - elapsed;
}

export function demoTokens(label: string | undefined): number {
	const match = /^(\d+(?:\.\d+)?)([KM]?)$/.exec(label ?? "");
	if (!match) return 0;
	const scale = match[2] === "M" ? 1_000_000 : match[2] === "K" ? 1_000 : 1;
	return Math.round(Number(match[1]) * scale);
}

const idOf = (ref: string) => ref.slice(ref.indexOf(":") + 1);
const iso = (ms: number) => new Date(ms).toISOString();
const BRIEF = (title: string) => `${title}. Report what you find; don't change unrelated code.`;

interface Counts {
	active: number;
	failed: number;
	completed: number;
	complete: true;
}

const noCounts = (): Counts => ({ active: 0, failed: 0, completed: 0, complete: true });

function addCounts(into: Counts, from: Counts): void {
	into.active += from.active;
	into.failed += from.failed;
	into.completed += from.completed;
}

function session(sessionId: string, ref: string, label: string, entries: unknown[], counts: Counts) {
	const aggregate = counts.active > 0 ? "working" : counts.failed > 0 ? "failed" : "ended";
	return { kind: "session", sessionId, ref, label, aggregate, counts, entries, branch: {} };
}

/** A coordinator's activity tree: its subagents, nested as they were started,
 * and their shell jobs. The typed activity reads project it. */
export function demoActivityTree(coordinator: DemoCoordinator, startupMs: number): { data: unknown } {
	const toEntry = (sub: DemoSubagent, ownerSessionId: string): { entry: unknown; counts: Counts } => {
		const ref = coordinator.subagentRef(sub.id);
		const sessionId = idOf(ref);
		const running = sub.state === "running";
		const lastEvent = startupMs - sub.ago * 1000;
		const counts: Counts = {
			active: running ? 1 : 0,
			failed: sub.state === "failed" ? 1 : 0,
			completed: sub.state === "done" ? 1 : 0,
			complete: true,
		};
		const childEntries: unknown[] = [];
		const childCounts = noCounts();
		if (running && sub.line?.startsWith("Running ")) {
			const command = sub.line.slice("Running ".length);
			childEntries.push(
				shellEntry(sessionId, ref, {
					jobId: `job-${sub.id}`,
					status: "running",
					terminal: false,
					description: command,
					command,
					startedAt: iso(startupMs - 42_000),
				}),
			);
			childCounts.active += 1;
		}
		// A failure its line blames on a command ("Failed: go test exited 1")
		// ran that command as a job that failed as the subagent ended.
		const exited = sub.state === "failed" ? /^Failed: (.+?) exited (\d+)/.exec(sub.line ?? "") : null;
		if (exited) {
			const [, command = "", code = ""] = exited;
			childEntries.push(
				shellEntry(sessionId, ref, {
					jobId: `job-${sub.id}`,
					status: "command_exited_nonzero",
					terminal: true,
					outcome: "failure",
					exitCode: Number(code),
					description: command,
					command,
					startedAt: iso(lastEvent - 60_000),
					endedAt: iso(lastEvent),
				}),
			);
			childCounts.failed += 1;
		}
		for (const child of sub.children ?? []) {
			const nested = toEntry(child, sessionId);
			childEntries.push(nested.entry);
			addCounts(childCounts, nested.counts);
		}
		addCounts(counts, childCounts);
		const tokens = demoTokens(sub.tokens);
		const reason = sub.state === "failed" ? (sub.line ?? "").replace(/^Failed:\s*/, "") : "";
		const delegate = {
			delegateId: sub.id,
			ownerSessionId,
			childSessionId: sessionId,
			childRef: ref,
			type: "delegate",
			description: sub.title,
			task: BRIEF(sub.title),
			...(sub.model ? { model: sub.model } : {}),
			runStartedAt: iso(demoRunStartedAt(sub, startupMs)),
			...(running
				? { latestActivityAt: iso(lastEvent) }
				: {
						terminal: true,
						outcome: sub.state === "failed" ? "failed" : "completed",
						runEndedAt: iso(lastEvent),
						...(reason ? { reason } : {}),
						...(sub.state === "done" && sub.line ? { message: `${sub.line}.` } : {}),
					}),
			...(tokens > 0 ? { usage: { inputTokens: tokens, outputTokens: 0, totalTokens: tokens } } : {}),
			...(sub.lane
				? {
						worktree: {
							path: `/home/jesse/git/evener/.worktrees/${sub.lane}`,
							branch: sub.lane,
							headSha: "0000000",
							ahead: 1,
							dirty: false,
						},
					}
				: {}),
			branch: {},
			...(childEntries.length > 0 ? { child: session(sessionId, ref, sub.title, childEntries, childCounts) } : {}),
		};
		return { entry: { kind: "delegate", delegate }, counts };
	};
	const counts = noCounts();
	const entries: unknown[] = (coordinator.jobs ?? []).map((job) => {
		counts.completed += 1;
		const ended = startupMs - job.ago * 1000;
		return shellEntry(idOf(coordinator.ref), coordinator.ref, {
			jobId: `job-${job.id}`,
			status: "completed",
			terminal: true,
			outcome: "success",
			exitCode: 0,
			description: job.command,
			command: job.command,
			startedAt: iso(ended - job.elapsed * 1000),
			endedAt: iso(ended),
		});
	});
	if (coordinator.runningJob) {
		counts.active += 1;
		entries.push(
			shellEntry(idOf(coordinator.ref), coordinator.ref, {
				jobId: `job-${coordinator.runningJob.id}`,
				status: "running",
				terminal: false,
				description: coordinator.runningJob.command,
				command: coordinator.runningJob.command,
				startedAt: iso(startupMs - 42_000),
			}),
		);
	}
	for (const sub of coordinator.subagents) {
		const built = toEntry(sub, idOf(coordinator.ref));
		addCounts(counts, built.counts);
		entries.push(built.entry);
	}
	return {
		data: { revision: 1, root: session(idOf(coordinator.ref), coordinator.ref, coordinator.title, entries, counts) },
	};
}

/** evener/jobs/output's answer for one of the demo tree's shell jobs: a
 * short tail that reads like its command's output, the whole of it kept. */
export function demoJobOutput(job: { status: string; terminal: boolean; exitCode?: number }): { data: unknown } {
	const tail =
		job.status === "command_exited_nonzero"
			? [
					"=== RUN   TestRetirementTreeSettleDrains",
					"--- FAIL: TestRetirementTreeSettleDrains (0.42s)",
					"    retirement_test.go:88: drained 3 of 4 delegates",
					"\u001b[31mFAIL\u001b[0m",
					`exit status ${job.exitCode ?? 1}`,
					"",
				].join("\n")
			: job.terminal
				? ""
				: [
						"=== RUN   TestRetirement",
						"--- PASS: TestRetirement (0.08s)",
						"=== RUN   TestRetirementTreeSettle",
						"",
					].join("\n");
	const totalBytes = new TextEncoder().encode(tail).length;
	return { data: { tail, totalBytes, retainedStart: 0 } };
}

/** A shell job in the tree, owned by the session it names. */
function shellEntry(ownerSessionId: string, ownerRef: string, fields: Record<string, unknown>) {
	return {
		kind: "shell",
		job: { ownerSessionId, ownerRef, type: "shell", background: true, hasOutput: true, outputBytes: 2048, ...fields },
	};
}

// data.js's settle-race plan (data.js:505-531), the text the demo hub serves
// unless EVENER_DEMO_FLEET_PLAN_REVISED is set.
export const SETTLE_RACE_PLAN = `# Fix the settle/drain race

## Problem

The retirement drain and the tree settle pass both take the tree lock. When settle runs first, it can mark the tree idle before the drain has seen pending work, so the root's attention is never delivered.

## Fix

1. Settle waits for the drain to finish before it takes the tree lock.
2. The drain signals completion through a channel, not a shared flag.
3. Add a regression test that forces settle to run first.

## Proof

- Run every affected package under \`-race\` on macOS **and** Linux.
- Run the three flaky tests 200 times each with \`-count=200\`.
- No skipped or quarantined tests.

## Subagents

| Work | Subagents |
|---|---|
| Fix the race | 1 |
| -race runs, macOS | 14 |
| -race runs, Linux | 14 |
| Flake loops | 3 |
`;

// The revision a restarted hub serves with EVENER_DEMO_FLEET_PLAN_REVISED:
// three changed blocks, so reading the plan before and after shows "3 changes
// since you read it earlier today" (frame 17).
export const SETTLE_RACE_PLAN_REVISED = SETTLE_RACE_PLAN.replace(
	"so the root's attention is never delivered.",
	"so the root's attention is never delivered. It shows up as three flaky tests.",
)
	.replace(
		"2. The drain signals completion through a channel, not a shared flag.",
		"2. The drain closes a channel when it finishes, and settle waits on it.",
	)
	.replace(
		"- No skipped or quarantined tests.",
		"- No skipped or quarantined tests.\n- Keep the -race runs in CI for a week.",
	);

export interface DemoDocument {
	sessionRef: string;
	/** Relative to the demo sessions' folder (createDemoDocuments' `folder`). */
	path: string;
	text: string;
}

/** /doc/file for the demo hub, as the hub answers it (doc_serve.go): a known
 * document's text by session and path (relative, or absolute under the demo
 * folder, as a file link names it), 404 for anything else, 400 without
 * format=raw. A document reads the same every time: chips and Files rows
 * read it too, so the Reader can't be told apart by when it reads. A demo
 * shows a revision by restarting the hub with the other text
 * (EVENER_DEMO_FLEET_PLAN_REVISED). */
export function createDemoDocuments(documents: readonly DemoDocument[], folder: string) {
	const root = `${folder}/`;
	return {
		answerDocFile(url: URL): { status: number; body: string } {
			const session = url.searchParams.get("session") ?? "";
			const raw = url.searchParams.get("path") ?? "";
			const path = raw.startsWith(root) ? raw.slice(root.length) : raw;
			const document = documents.find((candidate) => candidate.sessionRef === session && candidate.path === path);
			if (!document) return { status: 404, body: "not found" };
			if (url.searchParams.get("format") !== "raw") return { status: 400, body: "format=raw required" };
			return { status: 200, body: document.text };
		},
	};
}
