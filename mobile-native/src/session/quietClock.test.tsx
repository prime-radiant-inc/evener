import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AGENT_QUIET_AFTER_MS } from "../board/attention";
import { renderHook } from "../renderNative.testkit";
import { useNowPastQuiet } from "./quietClock";

vi.mock("react-native", async () => (await import("../renderNative.testkit")).nativeModuleMock());

const START = Date.UTC(2026, 9, 5, 12, 0, 0);
// An agent last heard from at `since`: its silence grows with the clock.
const silentSince = (since: number) => (at: number) => [at - since];

beforeEach(() => {
	vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
	vi.setSystemTime(START);
});
afterEach(() => vi.useRealTimers());

it("moves on once, when the soonest silence crosses into Quiet", () => {
	const hook = renderHook(() => useNowPastQuiet(START, silentSince(START - 15_000)));
	expect(hook.result.current).toBe(START);
	act(() => {
		vi.advanceTimersByTime(4_999);
	});
	expect(hook.result.current).toBe(START);
	act(() => {
		vi.advanceTimersByTime(1);
	});
	expect(hook.result.current).toBe(START + 5_000);
	// Past the threshold there is nothing left to wait for.
	expect(vi.getTimerCount()).toBe(0);
	hook.unmount();
});

it("waits for the next agent's crossing after the first", () => {
	const hook = renderHook(() =>
		useNowPastQuiet(START, (at) => [...silentSince(START - 15_000)(at), ...silentSince(START - 5_000)(at)]),
	);
	act(() => {
		vi.advanceTimersByTime(5_000);
	});
	expect(hook.result.current).toBe(START + 5_000);
	act(() => {
		vi.advanceTimersByTime(10_000);
	});
	expect(hook.result.current).toBe(START + 15_000);
	expect(vi.getTimerCount()).toBe(0);
	hook.unmount();
});

it("times the crossing on the wall clock when the caller's now lags it", () => {
	// The caller's now is 10s behind the wall clock, so the crossing is 5s off.
	vi.setSystemTime(START + 10_000);
	const hook = renderHook(() => useNowPastQuiet(START, silentSince(START - 5_000)));
	act(() => {
		vi.advanceTimersByTime(5_000);
	});
	expect(hook.result.current).toBe(START + AGENT_QUIET_AFTER_MS - 5_000);
	hook.unmount();
});

it("sets no timer when no silence is short of Quiet", () => {
	const hook = renderHook(() => useNowPastQuiet(START, () => [undefined, AGENT_QUIET_AFTER_MS + 1]));
	expect(vi.getTimerCount()).toBe(0);
	expect(hook.result.current).toBe(START);
	hook.unmount();
});

it("stops waiting when it unmounts", () => {
	const hook = renderHook(() => useNowPastQuiet(START, silentSince(START)));
	expect(vi.getTimerCount()).toBe(1);
	hook.unmount();
	expect(vi.getTimerCount()).toBe(0);
});
