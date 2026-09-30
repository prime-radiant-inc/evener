import { describe, expect, it } from "vitest";
import { forgetLaunchMemory, LaunchMemory, lastSetupKey } from "./launchMemory";
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

describe("the start New session opens on", () => {
	it("keeps only the newest start, across launches", () => {
		const storage = memory();
		const starts = new LaunchMemory(storage, "hub-a");
		expect(starts.lastSetup()).toBeNull();
		starts.recordStart(setup({ effort: "high" }));
		starts.recordStart(setup({ cwd: "/home/jesse/git/docs" }));
		starts.recordStart(setup({ effort: "max" }));
		expect(starts.lastSetup()).toEqual(setup({ effort: "max" }));
		expect(new LaunchMemory(storage, "hub-a").lastSetup()).toEqual(setup({ effort: "max" }));
	});

	it("leaves itself unchanged when the phone can't store a start", () => {
		const storage = memory();
		const starts = new LaunchMemory(storage, "hub-a");
		starts.recordStart(setup());
		storage.setItemSync = () => {
			throw new Error("disk full");
		};
		expect(() => starts.recordStart(setup({ cwd: "/other" }))).toThrow("disk full");
		expect(starts.lastSetup()?.cwd).toBe("/home/jesse/git/evener");
	});
});

describe("stored values", () => {
	it("reads a setup this build can't read as nothing", () => {
		const storage = memory(new Map([[lastSetupKey("hub-a"), JSON.stringify({ ...setup(), host: 7 })]]));
		expect(new LaunchMemory(storage, "hub-a").lastSetup()).toBeNull();
	});

	it("reads a value that doesn't parse as nothing", () => {
		const storage = memory(new Map([[lastSetupKey("hub-a"), "{not json"]]));
		expect(new LaunchMemory(storage, "hub-a").lastSetup()).toBeNull();
	});

	it("forgets one hub and keeps another's (Review Focus 5)", () => {
		const storage = memory();
		new LaunchMemory(storage, "hub-a").recordStart(setup());
		new LaunchMemory(storage, "hub-b").recordStart(setup({ cwd: "/b" }));
		forgetLaunchMemory(storage, "hub-a");
		expect(new LaunchMemory(storage, "hub-a").lastSetup()).toBeNull();
		expect(new LaunchMemory(storage, "hub-b").lastSetup()?.cwd).toBe("/b");
	});
});

describe("a storage that fails", () => {
	it("opens empty when the phone can't read it", () => {
		const storage = memory();
		storage.getItemSync = () => {
			throw new Error("kv-store unavailable");
		};
		expect(new LaunchMemory(storage, "hub-a").lastSetup()).toBeNull();
	});

	it("says so when the phone won't let go of a removed hub's remembered start", () => {
		const storage = memory();
		new LaunchMemory(storage, "hub-a").recordStart(setup());
		storage.removeItemSync = () => {
			throw new Error("disk busy");
		};
		expect(() => forgetLaunchMemory(storage, "hub-a")).toThrow();
	});
});
