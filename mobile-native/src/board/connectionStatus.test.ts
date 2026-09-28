import type { ConnectionState } from "@evener/appwire-client";
import { expect, it } from "vitest";
import { connectionStatus } from "./connectionStatus";

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
