import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { ConversationStatus } from "../../../mobile/src/state/conversation";
import { renderHook, unmountMountedTrees } from "../renderNative.testkit";
import { useReadRetry } from "./useReadRetry";

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
	unmountMountedTrees();
	vi.useRealTimers();
});

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

it("lets an old session's late retry count for nothing, and never block the new one", async () => {
	const state = { status: "error" as ConversationStatus, key: "ref-1" };
	const pending: (() => void)[] = [];
	const resume = vi.fn(() => new Promise<void>((resolve) => pending.push(resolve)));
	const hook = renderHook(() =>
		useReadRetry({ status: state.status, active: true, resetKey: state.key, readStatus: () => state.status, resume }),
	);
	await advance(0);
	expect(resume).toHaveBeenCalledTimes(1);
	// A new session replaces the store: its status starts over.
	state.key = "ref-2";
	state.status = "opening";
	hook.rerender();
	expect(hook.result.current).toBe(0);
	// The old session's read answers late, while the new one is still opening.
	await act(async () => {
		pending[0]?.();
	});
	expect(hook.result.current).toBe(0);
	// The new session's own failure retries at once: nothing is left in flight.
	state.status = "error";
	hook.rerender();
	await advance(0);
	expect(resume).toHaveBeenCalledTimes(2);
	hook.unmount();
	await act(async () => {
		pending[1]?.();
	});
});

it("doesn't count a retry that finished while a newer read was still opening", async () => {
	const state = { status: "error" as ConversationStatus };
	// Another read (the screen's own resume on focus, say) takes over while
	// this retry is out: the store reads "opening" when the retry settles.
	const resume = vi.fn(async () => {
		state.status = "opening";
	});
	const hook = renderHook(() =>
		useReadRetry({ status: state.status, active: true, resetKey: "ref-1", readStatus: () => state.status, resume }),
	);
	await advance(0);
	expect(resume).toHaveBeenCalledTimes(1);
	expect(hook.result.current).toBe(1);
	// That newer read fails: the retry re-arms at the same step.
	state.status = "error";
	hook.rerender();
	await advance(0);
	expect(resume).toHaveBeenCalledTimes(2);
	hook.unmount();
});

it("retries a failed read that leaves readable rows open, then clears on a later success", async () => {
	const state = { status: "open" as ConversationStatus, failed: true };
	const resume = vi.fn(async () => {});
	const hook = renderHook(() =>
		useReadRetry({
			status: state.status,
			active: true,
			resetKey: "ref-1",
			readStatus: () => state.status,
			readFailed: state.failed,
			readFailedNow: () => state.failed,
			resume,
		}),
	);
	// A rehydrate failure keeps status "open"; the read-failure fact still arms
	// the one retry, and its own failed outcome advances the count.
	await advance(0);
	expect(resume).toHaveBeenCalledTimes(1);
	expect(hook.result.current).toBe(2);

	// An accepted later publication clears the demand and its pending backoff.
	state.failed = false;
	hook.rerender();
	expect(hook.result.current).toBe(0);
	await advance(30_000);
	expect(resume).toHaveBeenCalledTimes(1);
	hook.unmount();
});
