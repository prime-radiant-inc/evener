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
vi.mock("expo-crypto", () => ({ randomUUID: () => "recipe-id" }));

it("forgets a removed hub's cached memory even when the phone can't delete its keys", async () => {
	const { forgetLaunchMemoryForHub, launchMemory } = await import("./nativeLaunchMemory");
	const before = launchMemory("hub-a");
	expect(launchMemory("hub-a")).toBe(before);
	failure.remove = true;
	expect(() => forgetLaunchMemoryForHub("hub-a")).toThrow();
	failure.remove = false;
	expect(launchMemory("hub-a")).not.toBe(before);
});
