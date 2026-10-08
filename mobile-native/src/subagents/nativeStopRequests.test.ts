import { expect, it, vi } from "vitest";
// Static, not `await import()` in a test body: the first import transforms
// the module's graph, which timed out a test under parallel load (#4031).
import { forgetStopRequestsForHub, stopRequests } from "./nativeStopRequests";

// Hoisted with the mocks that read them: the static import above evaluates
// the module before this file's plain declarations would exist.
const values = vi.hoisted(() => new Map<string, string>());
const failure = vi.hoisted(() => ({ remove: false }));
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

it("keeps its per-hub requests through the shared perHub helper", () => {
	expect(stopRequests("hub-a")).toBe(stopRequests("hub-a"));
	expect(stopRequests("hub-b")).not.toBe(stopRequests("hub-a"));
	expect(perHub.makers).toBe(1);
});

it("forgets a removed hub's cached requests even when the phone can't delete its keys", () => {
	const before = stopRequests("hub-c");
	failure.remove = true;
	expect(() => forgetStopRequestsForHub("hub-c")).toThrow();
	failure.remove = false;
	expect(stopRequests("hub-c")).not.toBe(before);
});
