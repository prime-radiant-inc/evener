import { expect, it, vi } from "vitest";

const values = new Map<string, string>();
const failure = { remove: false };
vi.mock("expo-sqlite/kv-store", () => ({
	Storage: {
		getItemSync: (key: string) => values.get(key) ?? null,
		setItemSync: (key: string, value: string) => {
			values.set(key, value);
		},
		removeItemSync: (key: string) => {
			if (failure.remove) throw new Error("disk busy");
			values.delete(key);
		},
	},
}));

// The module keeps its one cache for its whole life, so this counts the shared
// helper's makers rather than the instances.
const perHub = vi.hoisted(() => ({ makers: 0 }));
vi.mock("../board/perHub", async (importOriginal) => {
	const actual = await importOriginal<typeof import("../board/perHub")>();
	return {
		perHub: <T, A extends unknown[]>(make: (hubId: string, ...args: A) => T) => {
			perHub.makers += 1;
			return actual.perHub(make);
		},
	};
});

it("keeps its per-hub memory through the shared perHub helper", async () => {
	const { launchMemory } = await import("./nativeLaunchMemory");
	expect(launchMemory("hub-a")).toBe(launchMemory("hub-a"));
	expect(launchMemory("hub-b")).not.toBe(launchMemory("hub-a"));
	expect(perHub.makers).toBe(1);
});

it("forgets a removed hub's cached memory even when the phone can't delete its keys", async () => {
	const { forgetLaunchMemoryForHub, launchMemory } = await import("./nativeLaunchMemory");
	const before = launchMemory("hub-a");
	expect(launchMemory("hub-a")).toBe(before);
	failure.remove = true;
	expect(() => forgetLaunchMemoryForHub("hub-a")).toThrow();
	failure.remove = false;
	expect(launchMemory("hub-a")).not.toBe(before);
});
