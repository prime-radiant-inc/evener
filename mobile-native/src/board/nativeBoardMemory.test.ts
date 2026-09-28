import { describe, expect, it, vi } from "vitest";
import { hubSeenMarks } from "./hubSeen";
import { boardSeen, foldedSections, forgetBoardForHub, organizeByPreference, seenMarkers } from "./nativeBoardMemory";

const kv = vi.hoisted(() => new Map<string, string>());
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => kv.set(key, value),
		removeItemSync: (key: string) => kv.delete(key),
	},
}));

// The module keeps its instances for its whole life, so each test uses hubs
// of its own.
describe("the Board's memory per hub", () => {
	it("hands every caller the same instance of each memory for one hub", () => {
		expect(seenMarkers("hub-a")).toBe(seenMarkers("hub-a"));
		expect(foldedSections("hub-a")).toBe(foldedSections("hub-a"));
		expect(organizeByPreference("hub-a")).toBe(organizeByPreference("hub-a"));
		expect(seenMarkers("hub-b")).not.toBe(seenMarkers("hub-a"));
		expect(foldedSections("hub-b")).not.toBe(foldedSections("hub-a"));
		expect(organizeByPreference("hub-b")).not.toBe(organizeByPreference("hub-a"));
	});

	it("forgets a hub's memories in memory and in storage, and keeps other hubs'", () => {
		const markers = seenMarkers("hub-c");
		const sections = foldedSections("hub-c");
		const choice = organizeByPreference("hub-c");
		markers.markUnread("local:a");
		sections.setFolded("idle", false);
		choice.set("host-project");
		const kept = organizeByPreference("hub-d");
		kept.set("host-project");

		forgetBoardForHub("hub-c");

		expect([...kv.keys()].filter((key) => key.endsWith(".hub-c"))).toEqual([]);
		expect(seenMarkers("hub-c")).not.toBe(markers);
		expect(foldedSections("hub-c")).not.toBe(sections);
		expect(organizeByPreference("hub-c")).not.toBe(choice);
		expect(seenMarkers("hub-c").isSeen({ ref: "local:a" })).toBe(true);
		expect(foldedSections("hub-c").isFolded("idle", true)).toBe(true);
		expect(organizeByPreference("hub-c").get()).toBe("project-host");
		expect(organizeByPreference("hub-d")).toBe(kept);
		expect(kept.get()).toBe("host-project");
	});

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
});
