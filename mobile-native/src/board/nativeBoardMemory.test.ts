import { describe, expect, it, vi } from "vitest";
import { foldedSections, forgetBoardForHub, organizeByPreference, recentSearches, seenMarkers } from "./nativeBoardMemory";

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
		markers.markUnread("local:a");
		sections.setFolded("idle", false);
		choice.set("host-project");
		searches.add("fix");
		expect(kv.get("evener.native.recent-searches.hub-c")).toBe('["fix"]');
		const kept = organizeByPreference("hub-d");
		kept.set("host-project");

		forgetBoardForHub("hub-c");

		expect([...kv.keys()].filter((key) => key.endsWith(".hub-c"))).toEqual([]);
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
});
