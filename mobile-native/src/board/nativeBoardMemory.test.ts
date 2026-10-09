import { act } from "react-test-renderer";
import { describe, expect, it, vi } from "vitest";
import { renderHook } from "../renderNative.testkit";
import { hubSeenMarks } from "./hubSeen";
import {
	boardHold,
	boardSeen,
	foldedSections,
	forgetBoardForHub,
	organizeByPreference,
	recentSearches,
	seenMarkers,
	useBoardSeen,
} from "./nativeBoardMemory";

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
		expect(recentSearches("hub-a")).toBe(recentSearches("hub-a"));
		expect(seenMarkers("hub-b")).not.toBe(seenMarkers("hub-a"));
		expect(foldedSections("hub-b")).not.toBe(foldedSections("hub-a"));
		expect(organizeByPreference("hub-b")).not.toBe(organizeByPreference("hub-a"));
		expect(recentSearches("hub-b")).not.toBe(recentSearches("hub-a"));
	});

	it("forgets a hub's memories in memory and in storage, and keeps other hubs'", () => {
		const markers = seenMarkers("hub-c");
		const sections = foldedSections("hub-c");
		const choice = organizeByPreference("hub-c");
		const searches = recentSearches("hub-c");
		const hold = boardHold("hub-c");
		markers.markSeen({ ref: "local:a", updated_at: new Date(0).toISOString() });
		hold.hold({ kind: "stop", ref: "local:a", title: "A", seen: { turnEndedAt: null, running: true } }, 1);
		sections.setFolded("idle", false);
		choice.set("host-project");
		searches.add("fix");
		expect(kv.get("evener.native.recent-searches.hub-c")).toBe('["fix"]');
		const kept = organizeByPreference("hub-d");
		kept.set("host-project");

		forgetBoardForHub("hub-c");

		expect([...kv.keys()].filter((key) => key.endsWith(".hub-c"))).toEqual([]);
		// A replay still answering finds the old hold empty and inert.
		expect(hold.getSnapshot()).toEqual([]);
		hold.hold({ kind: "stop", ref: "local:b", title: "B", seen: { turnEndedAt: null, running: true } }, 2);
		expect(kv.has("evener.native.board-hold.hub-c")).toBe(false);
		expect(boardHold("hub-c")).not.toBe(hold);
		expect(boardHold("hub-c").getSnapshot()).toEqual([]);
		expect(seenMarkers("hub-c")).not.toBe(markers);
		expect(foldedSections("hub-c")).not.toBe(sections);
		expect(organizeByPreference("hub-c")).not.toBe(choice);
		expect(recentSearches("hub-c")).not.toBe(searches);
		expect(seenMarkers("hub-c").isSeen({ ref: "local:a" })).toBe(true);
		expect(foldedSections("hub-c").isFolded("idle", true)).toBe(true);
		expect(organizeByPreference("hub-c").get()).toBe("project-host");
		expect(recentSearches("hub-c").list()).toEqual([]);
		expect(organizeByPreference("hub-d")).toBe(kept);
		expect(kept.get()).toBe("host-project");
	});

	it("forgets a hub's seen marks, on the device and pending for the hub", () => {
		const markers = seenMarkers("hub-forget");
		const hub = hubSeenMarks("hub-forget");
		hub.markSeen(null, [{ ref: "local:a", seenThrough: 1 }]);
		const row = { ref: "local:a", turn_ended_at: new Date(1).toISOString(), unseen: true };
		expect(hub.isSeenOnHub(row)).toBe(true);
		forgetBoardForHub("hub-forget");
		expect(seenMarkers("hub-forget")).not.toBe(markers);
		expect(hubSeenMarks("hub-forget")).not.toBe(hub);
		expect(hubSeenMarks("hub-forget").isSeenOnHub(row)).toBe(false);
	});

	it("gives the Board both paths for its hub", () => {
		seenMarkers("hub-both").adoptEpoch([]);
		hubSeenMarks("hub-both").markSeen(null, [{ ref: "local:hub", seenThrough: 1 }]);
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
		expect(seen.isSeen({ ...base, ref: "local:hub", turn_ended_at: new Date(1).toISOString(), unseen: true })).toBe(
			true,
		);
		const device = { ...base, ref: "local:device", updated_at: new Date(1).toISOString() };
		expect(seen.isSeen(device)).toBe(false);
		seenMarkers("hub-both").markSeen(device);
		expect(seen.isSeen(device)).toBe(true);
	});

	it("hands a screen a new BoardSeen after each mark on either path, so its memos re-classify", () => {
		seenMarkers("hub-hook").adoptEpoch([]);
		const hook = renderHook(() => useBoardSeen("hub-hook"));
		const first = hook.result.current;
		const row = {
			ref: "local:device",
			host_id: "local",
			session_id: "s",
			title: "t",
			project: "p",
			state: "idle",
			kind: "session",
			live: true,
			children: [],
			updated_at: new Date(1).toISOString(),
		};
		expect(first.isSeen(row)).toBe(false);
		act(() => seenMarkers("hub-hook").markSeen(row));
		const second = hook.result.current;
		expect(second).not.toBe(first);
		expect(second.isSeen(row)).toBe(true);
		act(() => hubSeenMarks("hub-hook").markSeen(null, [{ ref: "local:hub", seenThrough: 1 }]));
		expect(hook.result.current).not.toBe(second);
		hook.unmount();
	});
});
