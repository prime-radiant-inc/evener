import { describe, expect, it } from "vitest";
import { forgetLaunchMemory, HISTORY_LIMIT, historyKey, LaunchMemory } from "./launchMemory";
import type { LaunchSetup } from "./launchSetup";

function memory(values = new Map<string, string>()) {
	return {
		values,
		getItemSync: (key: string) => values.get(key) ?? null,
		setItemSync: (key: string, value: string) => {
			values.set(key, value);
		},
		removeItemSync: (key: string) => {
			values.delete(key);
		},
	};
}
const setup = (over: Partial<LaunchSetup> = {}): LaunchSetup => ({
	host: "local",
	cwd: "/home/jesse/git/evener",
	model: { provider: "lunaroute", model: "deepseek-4.1-flash" },
	effort: "xhigh",
	overrides: { sandbox: "workspace-write" },
	...over,
});

describe("the starts New session opens on", () => {
	it("keeps the newest start per host and project, newest first, across launches", () => {
		const storage = memory();
		const starts = new LaunchMemory(storage, "hub-a");
		starts.recordStart(setup({ effort: "high" }), 1);
		starts.recordStart(setup({ cwd: "/home/jesse/git/docs" }), 2);
		starts.recordStart(setup({ effort: "max" }), 3);
		expect(
			new LaunchMemory(storage, "hub-a").history().map((entry) => [entry.setup.cwd, entry.setup.effort, entry.at]),
		).toEqual([
			["/home/jesse/git/evener", "max", 3],
			["/home/jesse/git/docs", "xhigh", 2],
		]);
	});

	it("forgets the oldest past the limit", () => {
		const starts = new LaunchMemory(memory(), "hub-a");
		for (let index = 0; index <= HISTORY_LIMIT; index++) starts.recordStart(setup({ cwd: `/p/${index}` }), index);
		expect(starts.history()).toHaveLength(HISTORY_LIMIT);
		expect(starts.history().some((entry) => entry.setup.cwd === "/p/0")).toBe(false);
	});

	it("leaves itself unchanged when the phone can't store a start", () => {
		const storage = memory();
		const starts = new LaunchMemory(storage, "hub-a");
		starts.recordStart(setup(), 1);
		storage.setItemSync = () => {
			throw new Error("disk full");
		};
		expect(() => starts.recordStart(setup({ cwd: "/other" }), 2)).toThrow("disk full");
		expect(starts.history().map((entry) => entry.setup.cwd)).toEqual(["/home/jesse/git/evener"]);
	});
});

describe("stored values", () => {
	it("drops entries this build can't read and keeps the rest", () => {
		const storage = memory(
			new Map([
				[
					historyKey("hub-a"),
					JSON.stringify([
						{ setup: { host: 7 }, at: 1 },
						{ setup: setup(), at: 2 },
						{ setup: setup({ cwd: "/p" }), at: "yesterday" },
					]),
				],
			]),
		);
		expect(new LaunchMemory(storage, "hub-a").history()).toEqual([{ setup: setup(), at: 2 }]);
	});

	it("reads a list that doesn't parse as empty", () => {
		const storage = memory(new Map([[historyKey("hub-a"), "{not json"]]));
		expect(new LaunchMemory(storage, "hub-a").history()).toEqual([]);
	});

	it("forgets one hub and keeps another's (Review Focus 5)", () => {
		const storage = memory();
		new LaunchMemory(storage, "hub-a").recordStart(setup(), 1);
		new LaunchMemory(storage, "hub-b").recordStart(setup({ cwd: "/b" }), 1);
		forgetLaunchMemory(storage, "hub-a");
		expect(new LaunchMemory(storage, "hub-a").history()).toEqual([]);
		expect(new LaunchMemory(storage, "hub-b").history().map((entry) => entry.setup.cwd)).toEqual(["/b"]);
	});
});

describe("a storage that fails", () => {
	it("opens empty when the phone can't read it", () => {
		const storage = memory();
		storage.getItemSync = () => {
			throw new Error("kv-store unavailable");
		};
		expect(new LaunchMemory(storage, "hub-a").history()).toEqual([]);
	});

	it("says so when the phone won't let go of a removed hub's starts", () => {
		const storage = memory();
		new LaunchMemory(storage, "hub-a").recordStart(setup(), 1);
		storage.removeItemSync = () => {
			throw new Error("disk busy");
		};
		expect(() => forgetLaunchMemory(storage, "hub-a")).toThrow();
	});
});
