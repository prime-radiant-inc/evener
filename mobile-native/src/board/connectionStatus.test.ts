import type { ConnectionState } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, unmountMountedTrees } from "../renderNative.testkit";

const connection = vi.hoisted(() => ({
	value: {
		state: "ready" as string,
		fatal: false,
		error: null as string | null,
		downSince: null as number | null,
		lastLiveAt: null as number | null,
	},
}));
vi.mock("../ConnectionProvider", () => ({ useConnection: () => connection.value }));

import { connectionStatus, nextStatusChange, offlineAge, useConnectionStatusText } from "./connectionStatus";

const NOW = Date.UTC(2026, 8, 26, 12, 0);
const secondsAgo = (seconds: number) => NOW - seconds * 1000;
const minutesAgo = (minutes: number) => secondsAgo(minutes * 60);

const cases: Array<{
	name: string;
	state: ConnectionState;
	fatal?: boolean;
	error?: string | null;
	downSince: number | null;
	lastLiveAt: number | null;
	expected: string | null;
}> = [
	{ name: "live", state: "ready", downSince: null, lastLiveAt: minutesAgo(3), expected: null },
	{ name: "down 1s", state: "reconnecting", downSince: secondsAgo(1), lastLiveAt: secondsAgo(1), expected: null },
	{ name: "down just under 2s", state: "reconnecting", downSince: NOW - 1999, lastLiveAt: NOW - 1999, expected: null },
	{
		name: "down 2s",
		state: "reconnecting",
		downSince: secondsAgo(2),
		lastLiveAt: secondsAgo(2),
		expected: "Reconnecting…",
	},
	{
		name: "down just under 30s",
		state: "reconnecting",
		downSince: NOW - 29_999,
		lastLiveAt: minutesAgo(3),
		expected: "Reconnecting…",
	},
	{
		name: "down 30s",
		state: "reconnecting",
		downSince: secondsAgo(30),
		lastLiveAt: minutesAgo(3),
		expected: "Offline · updated 3m ago",
	},
	{
		name: "down 31s, live 3 minutes ago",
		state: "reconnecting",
		downSince: secondsAgo(31),
		lastLiveAt: minutesAgo(3),
		expected: "Offline · updated 3m ago",
	},
	{
		name: "down 31s, live under a minute ago, says at least 1m",
		state: "reconnecting",
		downSince: secondsAgo(31),
		lastLiveAt: secondsAgo(31),
		expected: "Offline · updated 1m ago",
	},
	{
		name: "down 31s, never live this launch",
		state: "connecting",
		downSince: secondsAgo(31),
		lastLiveAt: null,
		expected: "Offline",
	},
	{ name: "closed 1s", state: "closed", downSince: secondsAgo(1), lastLiveAt: secondsAgo(1), expected: null },
	{
		name: "closed 2s",
		state: "closed",
		downSince: secondsAgo(2),
		lastLiveAt: secondsAgo(2),
		expected: "Reconnecting…",
	},
	{
		name: "closed 31s",
		state: "closed",
		downSince: secondsAgo(31),
		lastLiveAt: minutesAgo(3),
		expected: "Offline · updated 3m ago",
	},
	{
		name: "fatal just now",
		state: "closed",
		fatal: true,
		downSince: NOW,
		lastLiveAt: NOW,
		expected: "Update needed",
	},
	{
		name: "fatal long ago",
		state: "closed",
		fatal: true,
		downSince: minutesAgo(10),
		lastLiveAt: null,
		expected: "Update needed",
	},
	{
		name: "fatal while ready",
		state: "ready",
		fatal: true,
		downSince: null,
		lastLiveAt: NOW,
		expected: "Update needed",
	},
];

it.each(cases)("$name → $expected", ({ state, fatal = false, error = null, downSince, lastLiveAt, expected }) => {
	expect(connectionStatus(state, fatal, error, downSince, lastLiveAt, NOW)).toBe(expected);
});

const CLOSED_ERROR = "connection closed for the test's own reasons";

it("says why the connection closed while it stays closed, in place of Reconnecting and Offline", () => {
	// A close a retry can fix carries its own sentence: the status says that
	// once the drop is old enough to speak, instead of counting up to Offline.
	expect(connectionStatus("closed", false, CLOSED_ERROR, secondsAgo(2), secondsAgo(2), NOW)).toBe(CLOSED_ERROR);
	expect(connectionStatus("closed", false, CLOSED_ERROR, secondsAgo(31), minutesAgo(3), NOW)).toBe(CLOSED_ERROR);
	expect(connectionStatus("reconnecting", false, CLOSED_ERROR, secondsAgo(45), null, NOW)).toBe(CLOSED_ERROR);
	// Fresh enough that a blip would stay quiet: quiet here too.
	expect(connectionStatus("closed", false, CLOSED_ERROR, secondsAgo(1), secondsAgo(1), NOW)).toBeNull();
});

it("prefers the fatal state over the close's reason, whatever the error says", () => {
	// A fatal close never becomes the error sentence a retryable close shows:
	// the two branches are mutually exclusive in the presenter's decision.
	const fatal = connectionStatus("closed", true, CLOSED_ERROR, NOW, NOW, NOW);
	const retryable = connectionStatus("closed", false, CLOSED_ERROR, NOW, NOW, NOW);
	expect(fatal).not.toBe(retryable);
	expect(fatal).not.toBe(CLOSED_ERROR);
});

it("says nothing about an error while the connection is live", () => {
	expect(connectionStatus("ready", false, CLOSED_ERROR, null, NOW, NOW)).toBeNull();
});

it("says the data's age in whole minutes, hours or days, never under 1m", () => {
	expect([0, 59_999, 60_000, 119_999, 3_599_999, 3_600_000, 86_399_999, 86_400_000 * 2].map(offlineAge)).toEqual([
		"1m",
		"1m",
		"1m",
		"1m",
		"59m",
		"1h",
		"23h",
		"2d",
	]);
});

it("counts the data's age from when it was last live, background included", () => {
	expect(connectionStatus("connecting", false, null, secondsAgo(31), minutesAgo(60), NOW)).toBe(
		"Offline · updated 1h ago",
	);
});

it("changes on its own only at 2 seconds, 30 seconds, then when the age it says changes", () => {
	expect(nextStatusChange("reconnecting", false, null, NOW, NOW, NOW + 500)).toBe(NOW + 2_000);
	expect(nextStatusChange("reconnecting", false, null, NOW, NOW, NOW + 2_000)).toBe(NOW + 30_000);
	// Under 2 minutes old the age already reads "1m", so the next change is 2m.
	expect(nextStatusChange("reconnecting", false, null, NOW, NOW - 10_000, NOW + 30_000)).toBe(NOW + 110_000);
	expect(nextStatusChange("reconnecting", false, null, NOW, NOW, NOW + 150_000)).toBe(NOW + 180_000);
	// An hour or more old, it reads in hours, then in days.
	expect(nextStatusChange("reconnecting", false, null, NOW - 60_000, NOW - 5_400_000, NOW)).toBe(NOW + 1_800_000);
	expect(nextStatusChange("reconnecting", false, null, NOW - 60_000, NOW - 90_000_000, NOW)).toBe(NOW + 82_800_000);
	expect(nextStatusChange("reconnecting", false, null, NOW, null, NOW + 30_000)).toBeNull();
	expect(nextStatusChange("ready", false, null, null, null, NOW)).toBeNull();
	expect(nextStatusChange("closed", true, null, NOW, NOW, NOW)).toBeNull();
});

describe("useConnectionStatusText: the status on the provider's clock", () => {
	/** What connectionStatus says of the provider's facts on the clock now:
	 * what a line in step with the clock shows. */
	const due = () => {
		const { state, fatal, error, downSince, lastLiveAt } = connection.value;
		return connectionStatus(state as ConnectionState, fatal, error, downSince, lastLiveAt, Date.now());
	};
	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(NOW);
	});
	afterEach(() => {
		unmountMountedTrees();
		vi.useRealTimers();
	});

	it("re-renders exactly when its words change, and runs no clock once live", () => {
		connection.value = { state: "reconnecting", fatal: false, error: null, downSince: NOW, lastLiveAt: NOW };
		const hook = renderHook(() => useConnectionStatusText());
		const advance = (ms: number) => {
			act(() => {
				vi.advanceTimersByTime(ms);
			});
			return hook.result.current;
		};
		expect(advance(1_000)).toBeNull();
		const reconnecting = advance(1_000);
		expect(reconnecting).not.toBeNull();
		expect(reconnecting).toBe(due());
		expect(advance(27_999)).toBe(reconnecting);
		const offline = advance(1);
		expect(offline).not.toBe(reconnecting);
		expect(offline).toBe(due());
		// One tick per act: React re-runs the effect that schedules the next
		// tick only when act flushes.
		expect(advance(30_000)).toBe(offline);
		const older = advance(60_000);
		expect(older).not.toBe(offline);
		expect(older).toBe(due());
		connection.value = { state: "ready", fatal: false, error: null, downSince: null, lastLiveAt: NOW };
		hook.rerender();
		expect(hook.result.current).toBeNull();
		expect(vi.getTimerCount()).toBe(0);
		hook.unmount();
	});

	it("starts from the provider's clock, so a screen that mounts mid-drop says what the rest say", () => {
		connection.value = {
			state: "reconnecting",
			fatal: false,
			error: null,
			downSince: NOW - 45_000,
			lastLiveAt: minutesAgo(3),
		};
		const hook = renderHook(() => useConnectionStatusText());
		// Already past 30 seconds down: the age, not Reconnecting.
		expect(hook.result.current).toBe(due());
		expect(hook.result.current).not.toBe(connectionStatus("reconnecting", false, null, NOW, NOW, NOW + 2_000));
		hook.unmount();
	});

	it("reads the provider's clock when a line already on screen hears of a drop that began earlier", () => {
		connection.value = { state: "ready", fatal: false, error: null, downSince: null, lastLiveAt: NOW };
		const hook = renderHook(() => useConnectionStatusText());
		expect(hook.result.current).toBeNull();
		act(() => {
			vi.advanceTimersByTime(60_000);
		});
		// The provider reports a drop that began 2 seconds ago, as it does when a
		// ready connection's close reaches the screens a moment late.
		connection.value = {
			state: "reconnecting",
			fatal: false,
			error: null,
			downSince: Date.now() - 2_000,
			lastLiveAt: Date.now() - 2_000,
		};
		hook.rerender();
		act(() => {
			vi.advanceTimersByTime(0);
		});
		expect(hook.result.current).not.toBeNull();
		expect(hook.result.current).toBe(due());
		hook.unmount();
	});

	it("says Offline with no age when the hub was never reached this launch", () => {
		connection.value = { state: "connecting", fatal: false, error: null, downSince: NOW, lastLiveAt: null };
		const hook = renderHook(() => useConnectionStatusText());
		for (const ms of [2_000, 28_000])
			act(() => {
				vi.advanceTimersByTime(ms);
			});
		expect(hook.result.current).toBe(due());
		expect(hook.result.current).not.toBe(connectionStatus("reconnecting", false, null, NOW, NOW, NOW + 2_000));
		expect(vi.getTimerCount()).toBe(0);
		hook.unmount();
	});

	it("says Update needed at once for a close no retry can fix, and runs no clock", () => {
		connection.value = { state: "closed", fatal: true, error: null, downSince: NOW, lastLiveAt: NOW };
		const hook = renderHook(() => useConnectionStatusText());
		// At once: a drop this fresh would say nothing yet.
		expect(hook.result.current).not.toBeNull();
		expect(hook.result.current).toBe(due());
		expect(vi.getTimerCount()).toBe(0);
		hook.unmount();
	});

	it("shows the connection's own error sentence while the close lasts, and drops it when the connection returns", () => {
		const marker = "the status line must carry this sentence";
		connection.value = {
			state: "closed",
			fatal: false,
			error: marker,
			downSince: NOW,
			lastLiveAt: NOW,
		};
		const hook = renderHook(() => useConnectionStatusText());
		act(() => {
			vi.advanceTimersByTime(2_000);
		});
		expect(hook.result.current).toBe(marker);
		// The sentence stays until the connection's own facts change: no
		// clock ticks for an age it is not counting.
		expect(vi.getTimerCount()).toBe(0);
		act(() => {
			vi.advanceTimersByTime(60_000);
		});
		expect(hook.result.current).toBe(marker);
		connection.value = { state: "ready", fatal: false, error: null, downSince: null, lastLiveAt: NOW };
		hook.rerender();
		expect(hook.result.current).toBeNull();
		hook.unmount();
	});
});
