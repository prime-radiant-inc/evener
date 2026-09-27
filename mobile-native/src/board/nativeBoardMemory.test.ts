import { expect, it, vi } from "vitest";
import { forgetBoardForHub, recentSearches } from "./nativeBoardMemory";

const kv = vi.hoisted(() => new Map<string, string>());
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => kv.get(key) ?? null,
		setItemSync: (key: string, value: string) => kv.set(key, value),
		removeItemSync: (key: string) => kv.delete(key),
	},
}));

it("keeps one recent-searches list per hub, and forgetting the hub drops it", () => {
	const recent = recentSearches("hub-a");
	expect(recentSearches("hub-a")).toBe(recent);
	recent.add("fix");
	expect(kv.get("evener.native.recent-searches.hub-a")).toBe('["fix"]');
	forgetBoardForHub("hub-a");
	expect(kv.has("evener.native.recent-searches.hub-a")).toBe(false);
	expect(recentSearches("hub-a")).not.toBe(recent);
	expect(recentSearches("hub-a").list()).toEqual([]);
});
