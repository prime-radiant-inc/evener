import type { ActivityReadParams, ActivityReadResponse } from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { FakeClient, gateSettlements } from "@evener/appwire-client/testing/fakeClient";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ACTIVITY_POLL_MS, ActivityPoll, isFreshRead, STALE_AFTER_MS } from "./activityPoll";

const minutes = [0, 0, 1, 4, 9, 2, 0];
const activityA = { ref: "local:a", minutes, runningSubagents: 0 };

function client(): FakeClient {
	return new FakeClient("ready");
}

/** The recorded evener/activity/read calls' params, in call order. */
function paramsSent(fake: FakeClient): ActivityReadParams[] {
	return fake.calls.filter((call) => call.method === "evener/activity/read").map((call) => call.params as ActivityReadParams);
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe("ActivityPoll (S5)", () => {
	it("does nothing until started", async () => {
		const fake = client();
		fake.on("evener/activity/read", () => ({ sessions: [activityA] }));
		new ActivityPoll(fake);
		await vi.advanceTimersByTimeAsync(ACTIVITY_POLL_MS * 3);
		expect(paramsSent(fake)).toEqual([]);
	});

	it("polls immediately on start, then every ACTIVITY_POLL_MS", async () => {
		const fake = client();
		fake.on("evener/activity/read", () => ({ sessions: [activityA] }));
		const poll = new ActivityPoll(fake);
		poll.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(paramsSent(fake)).toHaveLength(1);
		await vi.advanceTimersByTimeAsync(ACTIVITY_POLL_MS);
		expect(paramsSent(fake)).toHaveLength(2);
		await vi.advanceTimersByTimeAsync(ACTIVITY_POLL_MS);
		expect(paramsSent(fake)).toHaveLength(3);
	});

	it("is idempotent: a second start() while running adds no extra timer", async () => {
		const fake = client();
		fake.on("evener/activity/read", () => ({ sessions: [] }));
		const poll = new ActivityPoll(fake);
		poll.start();
		poll.start();
		await vi.advanceTimersByTimeAsync(ACTIVITY_POLL_MS);
		expect(paramsSent(fake)).toHaveLength(2); // one immediate + one interval tick
	});

	it("stop halts further polling", async () => {
		const fake = client();
		fake.on("evener/activity/read", () => ({ sessions: [] }));
		const poll = new ActivityPoll(fake);
		poll.start();
		await vi.advanceTimersByTimeAsync(0);
		poll.stop();
		await vi.advanceTimersByTimeAsync(ACTIVITY_POLL_MS * 5);
		expect(paramsSent(fake)).toHaveLength(1);
	});

	it("sends no refs to read every session, or the given refs for a scoped screen", async () => {
		const fakeBoard = client();
		fakeBoard.on("evener/activity/read", () => ({ sessions: [] }));
		new ActivityPoll(fakeBoard).start();
		await vi.advanceTimersByTimeAsync(0);
		expect(paramsSent(fakeBoard)).toEqual([{}]);

		const fakeSession = client();
		fakeSession.on("evener/activity/read", () => ({ sessions: [] }));
		new ActivityPoll(fakeSession, ["local:a"]).start();
		await vi.advanceTimersByTimeAsync(0);
		expect(paramsSent(fakeSession)).toEqual([{ refs: ["local:a"] }]);
	});

	it("reads back the decoded activity by ref, notifies subscribers and bumps the revision", async () => {
		const fake = client();
		fake.on("evener/activity/read", () => ({ sessions: [activityA] }));
		const poll = new ActivityPoll(fake);
		const listener = vi.fn();
		poll.subscribe(listener);
		expect(poll.getRevision()).toBe(0);
		poll.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(poll.activity("local:a")).toEqual(activityA);
		expect(poll.activity("local:unknown")).toBeUndefined();
		expect(listener).toHaveBeenCalledTimes(1);
		expect(poll.getRevision()).toBe(1);
	});

	it("keeps the answer to the newest poll only, even if an older one resolves later", async () => {
		const fake = client();
		const settlements = gateSettlements(fake, "evener/activity/read");
		const poll = new ActivityPoll(fake);
		poll.start();
		await vi.advanceTimersByTimeAsync(0); // poll #1 in flight
		await vi.advanceTimersByTimeAsync(ACTIVITY_POLL_MS); // poll #2 in flight
		expect(settlements).toHaveLength(2);
		// The OLDER poll (#1) resolves last, with different data than the newer one.
		settlements[1]?.resolve({ sessions: [{ ...activityA, minutes: [0, 0, 0, 0, 0, 0, 5] }] });
		await vi.advanceTimersByTimeAsync(0);
		settlements[0]?.resolve({ sessions: [{ ...activityA, minutes: [9, 9, 9, 9, 9, 9, 9] }] });
		await vi.advanceTimersByTimeAsync(0);
		expect(poll.activity("local:a")?.minutes).toEqual([0, 0, 0, 0, 0, 0, 5]);
	});

	it("stops for good on a method-not-found answer, and a later start() is a no-op", async () => {
		const fake = client();
		fake.on("evener/activity/read", () => {
			throw new WireError("no such method", -32601);
		});
		const poll = new ActivityPoll(fake);
		poll.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(poll.supported).toBe(false);
		fake.on("evener/activity/read", () => ({ sessions: [activityA] }));
		poll.start();
		await vi.advanceTimersByTimeAsync(ACTIVITY_POLL_MS);
		expect(paramsSent(fake)).toHaveLength(1); // only the failed attempt; start() after is refused
		expect(poll.activity("local:a")).toBeUndefined();
	});

	it("notifies subscribers when a method-not-found answer confirms the hub doesn't support it", async () => {
		const fake = client();
		fake.on("evener/activity/read", () => {
			throw new WireError("no such method", -32601);
		});
		const poll = new ActivityPoll(fake);
		const listener = vi.fn();
		poll.subscribe(listener);
		poll.start();
		await vi.advanceTimersByTimeAsync(0);
		// A caller relying on the revision to know when to stop asking (a
		// force-rerender tick that should give up once .supported turns
		// false, say) needs a notification here too, not only on a landed
		// read: nothing else would ever tell it the poll gave up for good.
		expect(listener).toHaveBeenCalledTimes(1);
		expect(poll.getRevision()).toBe(1);
	});

	it("retries on any other error at the next tick, without giving up", async () => {
		const fake = client();
		let calls = 0;
		fake.on("evener/activity/read", () => {
			calls++;
			if (calls === 1) throw new Error("temporary hiccup");
			return { sessions: [activityA] };
		});
		const poll = new ActivityPoll(fake);
		poll.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(poll.supported).toBe(true);
		expect(poll.activity("local:a")).toBeUndefined();
		await vi.advanceTimersByTimeAsync(ACTIVITY_POLL_MS);
		expect(poll.activity("local:a")).toEqual(activityA);
	});

	it("keeps the previous read rather than blanking it when a later read can't be decoded", async () => {
		const fake = client();
		fake.on("evener/activity/read", () => ({ sessions: [activityA] }));
		const poll = new ActivityPoll(fake);
		poll.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(poll.activity("local:a")).toEqual(activityA);
		fake.on("evener/activity/read", () => ({ sessions: "not a list" }) as unknown as ActivityReadResponse);
		await vi.advanceTimersByTimeAsync(ACTIVITY_POLL_MS);
		expect(poll.activity("local:a")).toEqual(activityA);
	});

	it("reports milliseconds since the last successful read, null before the first", async () => {
		let now = 1_000;
		const fake = client();
		fake.on("evener/activity/read", () => ({ sessions: [activityA] }));
		const poll = new ActivityPoll(fake, undefined, () => now);
		expect(poll.msSinceRead()).toBeNull();
		poll.start();
		await vi.advanceTimersByTimeAsync(0);
		expect(poll.msSinceRead()).toBe(0);
		now += 5_000;
		expect(poll.msSinceRead()).toBe(5_000);
	});

	it("STALE_AFTER_MS is two poll intervals", () => {
		expect(STALE_AFTER_MS).toBe(2 * ACTIVITY_POLL_MS);
	});

	it("isFreshRead trusts a read under two poll intervals old, and never one that's never landed", () => {
		expect(isFreshRead(null)).toBe(false);
		expect(isFreshRead(0)).toBe(true);
		expect(isFreshRead(STALE_AFTER_MS - 1)).toBe(true);
		expect(isFreshRead(STALE_AFTER_MS)).toBe(false);
		expect(isFreshRead(STALE_AFTER_MS + 1)).toBe(false);
	});
});
