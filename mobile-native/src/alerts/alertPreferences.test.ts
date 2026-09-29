import { describe, expect, it } from "vitest";
import { DEFAULT_ALERT_PREFERENCES } from "./alertCenter";
import type { SyncStringStorage } from "../syncStringStorage";
import { memoryStorage as memory } from "../syncStringStorageTestUtils";
import { ALERT_PREFERENCES_KEY, AlertPreferenceStore } from "./alertPreferences";

describe("In-app alerts preferences (spec 12)", () => {
	it("start at the spec's defaults", () => {
		expect(new AlertPreferenceStore(memory()).getSnapshot()).toEqual({
			failures: true,
			questions: true,
			finished: false,
			hold: true,
			haptics: true,
		});
	});

	it("remember a change across launches", () => {
		const storage = memory();
		new AlertPreferenceStore(storage).set({ finished: true, haptics: false });
		expect(new AlertPreferenceStore(storage).getSnapshot()).toEqual({
			...DEFAULT_ALERT_PREFERENCES,
			finished: true,
			haptics: false,
		});
		expect(storage.values.has(ALERT_PREFERENCES_KEY)).toBe(true);
	});

	it("read damaged or foreign values as the defaults, field by field", () => {
		expect(new AlertPreferenceStore(memory(new Map([[ALERT_PREFERENCES_KEY, "{not json"]]))).getSnapshot()).toEqual(
			DEFAULT_ALERT_PREFERENCES,
		);
		const stored = JSON.stringify({ failures: false, finished: "yes", extra: true });
		expect(new AlertPreferenceStore(memory(new Map([[ALERT_PREFERENCES_KEY, stored]]))).getSnapshot()).toEqual({
			...DEFAULT_ALERT_PREFERENCES,
			failures: false,
		});
	});

	it("keep working in memory when storage fails", () => {
		const broken: SyncStringStorage = {
			getItemSync: () => {
				throw new Error("disk");
			},
			setItemSync: () => {
				throw new Error("disk");
			},
			removeItemSync: () => {
				throw new Error("disk");
			},
		};
		const store = new AlertPreferenceStore(broken);
		store.set({ hold: false });
		expect(store.getSnapshot().hold).toBe(false);
	});

	it("tell subscribers about each change", () => {
		const store = new AlertPreferenceStore(memory());
		let calls = 0;
		const stop = store.subscribe(() => calls++);
		const before = store.getSnapshot();
		store.set({ questions: false });
		expect(calls).toBe(1);
		expect(store.getSnapshot()).not.toBe(before);
		stop();
		store.set({ questions: true });
		expect(calls).toBe(1);
	});
});
