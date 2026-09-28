import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ConversationStatus } from "../../../mobile/src/state/conversation";
import { renderHook } from "../renderNative.testkit";
import { useReadRetry } from "./useReadRetry";

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

async function advance(ms: number) {
	await act(async () => {
		await vi.advanceTimersByTimeAsync(ms);
	});
}

function harness(resume: () => Promise<unknown>) {
	const state = { status: "error" as ConversationStatus };
	const hook = renderHook(() =>
		useReadRetry({ status: state.status, active: true, resetKey: "ref-1", readStatus: () => state.status, resume }),
	);
	return { state, hook };
}

it("keeps backing off when a retried read rejects instead of settling on an error", async () => {
	const resume = vi.fn(async () => {
		throw new Error("subscribe failed");
	});
	const { hook } = harness(resume);
	await advance(0);
	expect(resume).toHaveBeenCalledTimes(1);
	expect(hook.result.current).toBe(2);
	await advance(999);
	expect(resume).toHaveBeenCalledTimes(1);
	await advance(1);
	expect(resume).toHaveBeenCalledTimes(2);
	expect(hook.result.current).toBe(3);
	hook.unmount();
});

it("counts a retry that settles on an error, and resets once a read succeeds", async () => {
	const { state, hook } = harness(vi.fn(async () => {}));
	await advance(0);
	expect(hook.result.current).toBe(2);
	state.status = "open";
	hook.rerender();
	expect(hook.result.current).toBe(0);
	hook.unmount();
});
