import type { ConnectionState } from "@evener/appwire-client";
import { act } from "react-test-renderer";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import { connectionStatus, useConnectionStatusText } from "./connectionStatus";

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
	{ name: "closed 2s", state: "closed", downSince: secondsAgo(2), lastLiveAt: secondsAgo(2), expected: "Reconnecting…" },
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
	{ name: "fatal while ready", state: "ready", fatal: true, downSince: null, lastLiveAt: NOW, expected: "Update needed" },
];

it.each(cases)("$name → $expected", ({ state, fatal = false, downSince, lastLiveAt, expected }) => {
	expect(connectionStatus(state, fatal, downSince, lastLiveAt, NOW)).toBe(expected);
});

describe("useConnectionStatusText: the clock behind the status", () => {
	beforeEach(() => {
		vi.useFakeTimers();
		vi.setSystemTime(NOW);
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	/** Mounts the hook over a connection the test drives: `set` moves it to a
	 * new state and re-renders, the way the provider's value would. */
	function statusText(initial: ConnectionState, fatal = false) {
		let state = initial;
		const hook = renderHook(() => useConnectionStatusText(state, fatal));
		return {
			text: () => hook.result.current,
			set(next: ConnectionState) {
				state = next;
				hook.rerender();
			},
			advance(ms: number) {
				act(() => {
					vi.advanceTimersByTime(ms);
				});
				return hook.result.current;
			},
		};
	}

	it("says nothing at 1 second down, Reconnecting… at 2 and Offline at 30, then keeps the age current", () => {
		const status = statusText("ready");
		expect(status.text()).toBeNull();
		status.set("reconnecting");
		expect(status.text()).toBeNull();
		expect(status.advance(1000)).toBeNull();
		expect(status.advance(1000)).toBe("Reconnecting…");
		expect(status.advance(27_999)).toBe("Reconnecting…");
		expect(status.advance(1)).toBe("Offline · updated 1m ago");
		// The minute timer (ticking from the 30-second mark) moves the age on with
		// nothing else changing: 3m30s after the drop it says 3m.
		expect(status.advance(180_000)).toBe("Offline · updated 3m ago");
	});

	it("says Offline with no age when the hub was never reached this launch", () => {
		const status = statusText("connecting");
		expect(status.advance(30_000)).toBe("Offline");
	});

	it("runs no clock while live, and stops its clock when the connection is live again", () => {
		const status = statusText("ready");
		expect(vi.getTimerCount()).toBe(0);
		status.set("reconnecting");
		expect(vi.getTimerCount()).toBeGreaterThan(0);
		expect(status.advance(2000)).toBe("Reconnecting…");
		status.set("ready");
		expect(vi.getTimerCount()).toBe(0);
		expect(status.text()).toBeNull();
	});

	it("says Update needed at once when no retry can fix the close", () => {
		expect(statusText("closed", true).text()).toBe("Update needed");
	});
});
