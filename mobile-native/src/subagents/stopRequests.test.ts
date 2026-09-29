import { describe, expect, it } from "vitest";
import type { ActivityDelegate } from "@evener/appwire-client";
import { memoryStorage } from "../syncStringStorageTestUtils";
import { forgetStopRequests, StopRequests } from "./stopRequests";
import { flattenSubagents, type SubagentRow } from "./subagentModel";

const session = (ref: string, delegates: ActivityDelegate[]) => ({
	kind: "session" as const,
	sessionId: ref,
	ref,
	label: ref,
	aggregate: "working",
	counts: { active: 0, failed: 0, completed: 0, complete: true },
	branch: {},
	entries: delegates.map((delegate) => ({ kind: "delegate" as const, delegate })),
});
const d = (id: string, over: Partial<ActivityDelegate> = {}): ActivityDelegate => ({
	delegateId: id,
	childSessionId: id,
	childRef: `local:${id}`,
	type: "delegate",
	description: id,
	branch: {},
	...over,
});
const rows = (...delegates: ActivityDelegate[]): SubagentRow[] =>
	flattenSubagents({ revision: 1, root: session("local:coord", delegates) });
const row = (delegate: ActivityDelegate) => rows(delegate)[0] as SubagentRow;

const working = d("fix");
const stopped = d("fix", { terminal: true, outcome: "stopped" });
const finished = d("fix", { terminal: true, outcome: "completed" });
const failedOnItsOwn = d("fix", { terminal: true, outcome: "failed" });

describe("stop requests you sent a coordinator (spec 9, ruling 10)", () => {
	it("says the request is pending while the subagent still works", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		expect(requests.view(row(working))).toBeNull();
		requests.request("local:coord", row(working), 1000);
		expect(requests.view(row(working))).toBe("requested");
	});

	it("says Stopped at your request once it stops, and hands it back once for the toast", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(working), 1000);
		expect(requests.reconcile("local:coord", rows(stopped)).map((stoppedRow) => stoppedRow.id)).toEqual(["fix"]);
		expect(requests.view(row(stopped))).toBe("stopped");
		expect(requests.reconcile("local:coord", rows(stopped))).toEqual([]);
	});

	it("forgets a request whose subagent finished or failed on its own", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(working), 1000);
		expect(requests.reconcile("local:coord", rows(finished))).toEqual([]);
		expect(requests.view(row(finished))).toBeNull();
		requests.request("local:coord", row(working), 2000);
		requests.reconcile("local:coord", rows(failedOnItsOwn));
		expect(requests.view(row(failedOnItsOwn))).toBeNull();
	});

	it("counts a failed subagent whose running work was stopped as stopped at your request", () => {
		const failedWith = (child: ActivityDelegate) =>
			d("fix", { terminal: true, outcome: "failed", child: session("local:fix", [child]) });
		const requests = new StopRequests(memoryStorage(), "hub-1");
		const before = rows(failedWith(d("check")))[0] as SubagentRow;
		requests.request("local:coord", before, 1000);
		expect(requests.view(before)).toBe("requested");
		const after = rows(failedWith(d("check", { terminal: true, outcome: "cancelled" })));
		expect(requests.reconcile("local:coord", after).map((stoppedRow) => stoppedRow.id)).toEqual(["fix"]);
	});

	it("keeps a request whose subagent this read doesn't hold, and ignores another coordinator's read", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(working), 1000);
		expect(requests.reconcile("local:coord", rows(d("other", { terminal: true, outcome: "stopped" })))).toEqual([]);
		expect(requests.reconcile("local:elsewhere", rows(stopped))).toEqual([]);
		expect(requests.view(row(working))).toBe("requested");
	});

	it("survives a relaunch, keeps hubs apart, and forgets a removed hub", () => {
		const storage = memoryStorage();
		new StopRequests(storage, "hub-1").request("local:coord", row(working), 1000);
		expect(new StopRequests(storage, "hub-1").view(row(working))).toBe("requested");
		expect(new StopRequests(storage, "hub-2").view(row(working))).toBeNull();
		forgetStopRequests(storage, "hub-1");
		expect(new StopRequests(storage, "hub-1").view(row(working))).toBeNull();
	});

	it("reads corrupt storage as empty, and keeps the newest 200 requests", () => {
		const storage = memoryStorage(new Map([["evener.native.subagent-stops.hub-1", "{not json"]]));
		const requests = new StopRequests(storage, "hub-1");
		expect(requests.view(row(working))).toBeNull();
		for (let index = 0; index < 205; index += 1) requests.request("local:coord", row(d(`s${index}`)), index);
		const stored = JSON.parse(storage.values.get("evener.native.subagent-stops.hub-1") as string);
		expect(Object.keys(stored)).toHaveLength(200);
		expect(stored.s204).toBeDefined();
		expect(stored.s0).toBeUndefined();
	});

	it("tells subscribers when something changes", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		let calls = 0;
		const stop = requests.subscribe(() => {
			calls += 1;
		});
		const before = requests.getRevision();
		requests.request("local:coord", row(working), 1000);
		expect(calls).toBe(1);
		expect(requests.getRevision()).toBe(before + 1);
		stop();
		requests.request("local:coord", row(d("other")), 2000);
		expect(calls).toBe(1);
	});
});

describe("a request of the coordinator over work that was already stopped", () => {
	it("isn't stopped at your request when the only stop under it came before you asked", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		const stoppedChild = { kind: "delegate" as const, delegate: d("child", { terminal: true, outcome: "stopped" }) };
		const workingOverStopped = d("fix", {
			runStartedAt: "2026-09-28T10:00:00.000Z",
			child: { ...session("local:fix", []), entries: [stoppedChild] },
		});
		const finishedOverStopped = d("fix", {
			terminal: true,
			outcome: "completed",
			child: { ...session("local:fix", []), entries: [stoppedChild] },
		});
		requests.request("local:coord", row(workingOverStopped), 1000);
		expect(requests.reconcile("local:coord", rows(finishedOverStopped))).toEqual([]);
		expect(requests.view(row(finishedOverStopped))).toBeNull();
	});
});

describe("a request of the coordinator over a failed subagent's running command", () => {
	const shell = (status: string, terminal: boolean) => ({
		kind: "shell" as const,
		job: {
			jobId: "job-1",
			ownerSessionId: "fix",
			ownerRef: "local:fix",
			type: "shell",
			status,
			terminal,
			background: true,
			hasOutput: false,
			description: "go test",
			startedAt: "2026-09-28T10:00:00.000Z",
			outputBytes: 0,
		},
	});
	const failedOver = (entry: ReturnType<typeof shell>) =>
		d("fix", { terminal: true, outcome: "failed", child: { ...session("local:fix", []), entries: [entry] } });

	it("is stopped at your request once the command under it is stopped", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(failedOver(shell("running", false))), 1000);
		expect(
			requests.reconcile("local:coord", rows(failedOver(shell("stopped", true)))).map((stopped) => stopped.id),
		).toEqual(["fix"]);
	});

	it("is forgotten when the command finishes on its own", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(failedOver(shell("running", false))), 1000);
		expect(requests.reconcile("local:coord", rows(failedOver(shell("completed", true))))).toEqual([]);
	});
});

// S6: a direct stop ends the subagent's own run and leaves its subagents
// running (the S6 plan's ruling 1), so it settles on the subagent's own run.
describe("a stop you sent directly (S6)", () => {
	const runningWithChild = d("fix", {
		runStartedAt: "2026-09-28T10:00:00.000Z",
		child: session("local:fix", [d("child", { runStartedAt: "2026-09-28T10:01:00.000Z" })]),
	});
	const cancelledWithChild = d("fix", {
		terminal: true,
		outcome: "cancelled",
		child: session("local:fix", [d("child", { runStartedAt: "2026-09-28T10:01:00.000Z" })]),
	});

	it("is pending while the subagent's own run goes on, and says it's direct", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(runningWithChild), 1000, { direct: true });
		expect(requests.view(row(runningWithChild))).toBe("requested");
		expect(requests.direct(row(runningWithChild))).toBe(true);
	});

	it("settles once its own run is cancelled, though its subagents still work", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(runningWithChild), 1000, { direct: true });
		expect(requests.reconcile("local:coord", rows(cancelledWithChild)).map((stoppedRow) => stoppedRow.id)).toEqual([
			"fix",
		]);
		expect(requests.view(row(cancelledWithChild))).toBe("stopped");
	});

	it("forgets a direct stop whose subagent finished on its own, though a child of it was stopped", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(runningWithChild), 1000, { direct: true });
		const finishedOverStoppedChild = d("fix", {
			terminal: true,
			outcome: "completed",
			child: session("local:fix", [d("child", { terminal: true, outcome: "cancelled" })]),
		});
		expect(requests.reconcile("local:coord", rows(finishedOverStoppedChild))).toEqual([]);
		expect(requests.view(row(finishedOverStoppedChild))).toBeNull();
	});

	it("keeps that it was direct through a relaunch", () => {
		const storage = memoryStorage();
		new StopRequests(storage, "hub-1").request("local:coord", row(runningWithChild), 1000, { direct: true });
		expect(new StopRequests(storage, "hub-1").direct(row(runningWithChild))).toBe(true);
	});

	it("isn't direct when you asked the coordinator", () => {
		const requests = new StopRequests(memoryStorage(), "hub-1");
		requests.request("local:coord", row(working), 1000);
		expect(requests.direct(row(working))).toBe(false);
	});
});
