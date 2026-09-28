import type { EvenerDelegateInfo } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import type { TimelineRow } from "../timeline";
import { subagentLine } from "./subagentLine";

type Activity = Extract<TimelineRow, { kind: "activity" }>;

const NOW = Date.UTC(2026, 8, 27, 12, 0);
const ago = (ms: number) => new Date(NOW - ms).toISOString();

const row = (over: Partial<Activity> = {}): Activity => ({
	kind: "activity",
	id: "item-1",
	label: "delegate",
	family: "tool",
	state: "running",
	detail: { callId: "call-1", description: "Delegate: audit the store" },
	...over,
});

const delegate = (over: Partial<EvenerDelegateInfo> = {}): EvenerDelegateInfo => ({
	delegateId: "d1",
	ownerSessionId: "s0",
	rootSessionId: "s0",
	childSessionId: "s1",
	transcriptRef: "local:child-1",
	type: "delegate",
	lifecycle: "running",
	phase: "running",
	status: "running",
	resumable: false,
	needsAttention: false,
	projectionRevision: 1,
	description: "Audit the store",
	task: "Audit the store\nand report back",
	originItemId: "item-1",
	runningForMs: 240_000,
	quietForMs: 1_000,
	...over,
});

describe("a subagent's row (spec 8.2)", () => {
	it("says how long a running subagent has run, and that it's working", () => {
		expect(subagentLine(row(), [delegate()], NOW)).toEqual({
			title: "Audit the store",
			state: "running",
			stateText: "running · 4m",
			activity: "Working",
			ref: "local:child-1",
		});
	});

	it("says it's quiet once no update came for 20 seconds", () => {
		expect(subagentLine(row(), [delegate({ quietForMs: 300_000 })], NOW).activity).toBe("Quiet 5m");
	});

	it("waits on its own running subagents, and is never quiet then (ruling 10)", () => {
		const children = [
			delegate({ delegateId: "c1", parentDelegateId: "d1", originItemId: "other-1" }),
			delegate({ delegateId: "c2", parentDelegateId: "d1", originItemId: "other-2" }),
			delegate({ delegateId: "c3", parentDelegateId: "d1", originItemId: "other-3", status: "completed", terminal: true }),
		];
		expect(subagentLine(row(), [delegate({ quietForMs: 300_000 }), ...children], NOW).activity).toBe("Waiting on 2 subagents");
	});

	it("says how long ago a failed one failed, and why", () => {
		const failed = delegate({ status: "failed", terminal: true, runEndedAt: ago(360_000), reason: "model refused the task" });
		expect(subagentLine(row({ state: "failed" }), [failed], NOW)).toMatchObject({
			state: "failed",
			stateText: "failed · 6m",
			activity: "model refused the task",
		});
	});

	it("has no activity line once done", () => {
		const done = delegate({ status: "completed", terminal: true, runEndedAt: ago(120_000) });
		const line = subagentLine(row({ state: "completed" }), [done], NOW);
		expect(line).toMatchObject({ state: "done", stateText: "done · 2m" });
		expect(line.activity).toBeUndefined();
	});

	it("finds its subagent by the call that started it", () => {
		const byCall = delegate({ originItemId: undefined, originToolCallId: "call-1" });
		expect(subagentLine(row(), [byCall], NOW).ref).toBe("local:child-1");
	});

	it("takes its title and state from the row when no subagent matches", () => {
		expect(subagentLine(row({ state: "completed" }), [], NOW)).toEqual({
			title: "Delegate: audit the store",
			state: "done",
			stateText: "done",
		});
		expect(subagentLine(row({ detail: {} }), undefined, NOW).title).toBe("Subagent");
	});

	it("falls back to the task's first line for a title", () => {
		expect(subagentLine(row(), [delegate({ description: undefined })], NOW).title).toBe("Audit the store");
	});
});
