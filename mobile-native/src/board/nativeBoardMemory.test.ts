import { expect, it, vi } from "vitest";
import { hubSeenMarks } from "./hubSeen";
import { boardSeen, forgetBoardForHub, seenMarkers } from "./nativeBoardMemory";

const kv = vi.hoisted(() => new Map<string, string>());
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => kv.set(key, value),
		removeItemSync: (key: string) => kv.delete(key),
	},
}));

it("forgets a hub's seen marks, on the device and pending for the hub", () => {
	const markers = seenMarkers("hub-forget");
	const hub = hubSeenMarks("hub-forget");
	hub.markUnread(null, ["local:a"]);
	const row = { ref: "local:a", turn_ended_at: new Date(0).toISOString(), unseen: false };
	expect(hub.isSeenOnHub(row)).toBe(false);
	forgetBoardForHub("hub-forget");
	expect(seenMarkers("hub-forget")).not.toBe(markers);
	expect(hubSeenMarks("hub-forget")).not.toBe(hub);
	expect(hubSeenMarks("hub-forget").isSeenOnHub(row)).toBe(true);
});

it("gives the Board both paths for its hub", () => {
	seenMarkers("hub-both").adoptEpoch([]);
	hubSeenMarks("hub-both").markUnread(null, ["local:hub"]);
	const seen = boardSeen("hub-both");
	const base = {
		host_id: "local",
		session_id: "s",
		title: "t",
		project: "p",
		state: "idle",
		kind: "session",
		live: true,
		children: [],
	};
	expect(seen.isSeen({ ...base, ref: "local:hub", turn_ended_at: new Date(1).toISOString() })).toBe(false);
	seenMarkers("hub-both").markUnread("local:device");
	expect(seen.isSeen({ ...base, ref: "local:device" })).toBe(false);
});
