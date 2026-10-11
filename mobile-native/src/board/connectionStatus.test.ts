import type { ConnectionState } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, unmountMountedTrees } from "../renderNative.testkit";

const connection = vi.hoisted(() => ({
	value: {
		state: "ready" as string,
		fatal: false,
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

it.each(cases)("$name → $expected", ({ state, fatal = false, downSince, lastLiveAt, expected }) => {
	expect(connectionStatus(state, fatal, downSince, lastLiveAt, NOW)).toBe(expected);
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
	expect(connectionStatus("connecting", false, secondsAgo(31), minutesAgo(60), NOW)).toBe("Offline · updated 1h ago");
});

it("changes on its own only at 2 seconds, 30 seconds, then when the age it says changes", () => {
	expect(nextStatusChange("reconnecting", false, NOW, NOW, NOW + 500)).toBe(NOW + 2_000);
	expect(nextStatusChange("reconnecting", false, NOW, NOW, NOW + 2_000)).toBe(NOW + 30_000);
	// Under 2 minutes old the age already reads "1m", so the next change is 2m.
	expect(nextStatusChange("reconnecting", false, NOW, NOW - 10_000, NOW + 30_000)).toBe(NOW + 110_000);
	expect(nextStatusChange("reconnecting", false, NOW, NOW, NOW + 150_000)).toBe(NOW + 180_000);
	// An hour or more old, it reads in hours, then in days.
	expect(nextStatusChange("reconnecting", false, NOW - 60_000, NOW - 5_400_000, NOW)).toBe(NOW + 1_800_000);
	expect(nextStatusChange("reconnecting", false, NOW - 60_000, NOW - 90_000_000, NOW)).toBe(NOW + 82_800_000);
	expect(nextStatusChange("reconnecting", false, NOW, null, NOW + 30_000)).toBeNull();
	expect(nextStatusChange("ready", false, null, null, NOW)).toBeNull();
	expect(nextStatusChange("closed", true, NOW, NOW, NOW)).toBeNull();
});

describe("useConnectionStatusText: the status on the provider's clock", () => {
	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(NOW);
	});
	afterEach(() => {
		unmountMountedTrees();
		vi.useRealTimers();
	});

	it("re-renders exactly when its words change, and runs no clock once live", () => {
		connection.value = { state: "reconnecting", fatal: false, downSince: NOW, lastLiveAt: NOW };
		const hook = renderHook(() => useConnectionStatusText());
		const advance = (ms: number) => {
			act(() => {
				vi.advanceTimersByTime(ms);
			});
			return hook.result.current;
		};
		expect(advance(1_000)).toBeNull();
		expect(advance(1_000)).toBe("Reconnecting…");
		expect(advance(27_999)).toBe("Reconnecting…");
		expect(advance(1)).toBe("Offline · updated 1m ago");
		// One tick per act: React re-runs the effect that schedules the next
		// tick only when act flushes.
		expect(advance(30_000)).toBe("Offline · updated 1m ago");
		expect(advance(60_000)).toBe("Offline · updated 2m ago");
		connection.value = { state: "ready", fatal: false, downSince: null, lastLiveAt: NOW };
		hook.rerender();
		expect(hook.result.current).toBeNull();
		expect(vi.getTimerCount()).toBe(0);
		hook.unmount();
	});

	it("starts from the provider's clock, so a screen that mounts mid-drop says what the rest say", () => {
		connection.value = { state: "reconnecting", fatal: false, downSince: NOW - 45_000, lastLiveAt: minutesAgo(3) };
		const hook = renderHook(() => useConnectionStatusText());
		expect(hook.result.current).toBe("Offline · updated 3m ago");
		hook.unmount();
	});

	it("reads the provider's clock when a line already on screen hears of a drop that began earlier", () => {
		connection.value = { state: "ready", fatal: false, downSince: null, lastLiveAt: NOW };
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
			downSince: Date.now() - 2_000,
			lastLiveAt: Date.now() - 2_000,
		};
		hook.rerender();
		act(() => {
			vi.advanceTimersByTime(0);
		});
		expect(hook.result.current).toBe("Reconnecting…");
		hook.unmount();
	});

	it("says Offline with no age when the hub was never reached this launch", () => {
		connection.value = { state: "connecting", fatal: false, downSince: NOW, lastLiveAt: null };
		const hook = renderHook(() => useConnectionStatusText());
		for (const ms of [2_000, 28_000])
			act(() => {
				vi.advanceTimersByTime(ms);
			});
		expect(hook.result.current).toBe("Offline");
		expect(vi.getTimerCount()).toBe(0);
		hook.unmount();
	});

	it("says Update needed at once for a close no retry can fix, and runs no clock", () => {
		connection.value = { state: "closed", fatal: true, downSince: NOW, lastLiveAt: NOW };
		const hook = renderHook(() => useConnectionStatusText());
		expect(hook.result.current).toBe("Update needed");
		expect(vi.getTimerCount()).toBe(0);
		hook.unmount();
	});
});
