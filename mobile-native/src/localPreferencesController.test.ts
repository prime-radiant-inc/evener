import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { LocalPreferencesController } from "./localPreferencesController";
import { draftBackend } from "./nativeDraftBackend";
import type { NativePreferenceDraftBackend } from "./nativePreferenceDrafts";

const config = makeTranscriptDisplayConfig();
const transcriptKey = "evener.native.transcript-draft.hub";
const keybindingKey = "evener.native.keybinding-draft.hub";

/** The device store, with the keys it was written under observable - the
 * same shape nativePreferences.test.ts and nativePreferenceDrafts.test.ts
 * already use for this backend. */
function backend() {
	const values = new Map<string, unknown>();
	let id = 0;
	const port: NativePreferenceDraftBackend = {
		createId: () => String(++id),
		get: (key) => values.get(key),
		set: (key, value) => {
			values.set(key, structuredClone(value));
		},
		delete: (key) => {
			values.delete(key);
		},
		deleteIf: (key, value) => {
			if (JSON.stringify(values.get(key)) !== JSON.stringify(value)) return false;
			values.delete(key);
			return true;
		},
		replaceIf: (key, expected, next) => {
			if (JSON.stringify(values.get(key)) !== JSON.stringify(expected)) return false;
			values.set(key, structuredClone(next));
			return true;
		},
	};
	return { port, values };
}

/** The real byte-aware backend (draftBackend), with the raw stored bytes
 * observable - what a discard's byte identity is actually measured against,
 * never the hand-rolled JSON.stringify comparison `backend()` above uses. */
function deviceStore() {
	const raw = new Map<string, string>();
	const store = {
		getItemSync: (key: string) => raw.get(key) ?? null,
		setItemSync: (key: string, value: string) => {
			raw.set(key, value);
		},
		removeItemSync: (key: string) => {
			raw.delete(key);
		},
	};
	return { raw, port: draftBackend(store, () => "id-1") };
}

describe("LocalPreferencesController", () => {
	it("exposes a readable draft with no client bound (cold start, round 14 claim b)", () => {
		const disk = backend();
		disk.values.set(transcriptKey, {
			id: "d0",
			layout: "mobile",
			baseRevision: 4,
			config,
			writeUncertain: false,
		});
		disk.values.set(keybindingKey, {
			id: "k0",
			baseRevision: 2,
			rules: [{ action: "palette.open", chord: "Meta+P" }],
			writeUncertain: true,
		});
		const controller = new LocalPreferencesController("hub", disk.port);

		// No model, no client, nothing bound - and both drafts are visible.
		const snapshot = controller.getSnapshot();
		expect(snapshot.transcriptMobile).toMatchObject({
			draft: { revision: 4, config },
			draftUnreadable: false,
			support: "unknown",
			confirmed: null,
		});
		expect(snapshot.keybindings).toMatchObject({
			draft: { version: 1, revision: 2, rules: [{ action: "palette.open", chord: "Meta+P" }] },
			draftUnreadable: false,
			writeUncertain: true,
		});
	});

	it("classifies a record neither section's decoder accepts as unreadable, with no client bound", () => {
		const disk = backend();
		disk.values.set(transcriptKey, { id: "d0", baseRevision: -1 });
		const controller = new LocalPreferencesController("hub", disk.port);

		expect(controller.getSnapshot().transcriptMobile).toMatchObject({
			draft: null,
			draftUnreadable: true,
			storageUnavailable: true,
		});
	});

	it("reports no draft for a hub with nothing stored", () => {
		const disk = backend();
		const controller = new LocalPreferencesController("hub", disk.port);

		expect(controller.getSnapshot().transcriptMobile).toMatchObject({
			draft: null,
			draftUnreadable: false,
		});
		expect(controller.getSnapshot().keybindings).toMatchObject({
			draft: null,
			draftUnreadable: false,
		});
	});

	it("discards a readable draft with no client bound (round 14 claim a's operational half)", async () => {
		const disk = backend();
		disk.values.set(transcriptKey, {
			id: "d0",
			layout: "mobile",
			baseRevision: 4,
			config,
			writeUncertain: false,
		});
		const controller = new LocalPreferencesController("hub", disk.port);
		expect(controller.getSnapshot().transcriptMobile.draft).not.toBeNull();

		await controller.discardTranscript();

		expect(disk.values.has(transcriptKey)).toBe(false);
		expect(controller.getSnapshot().transcriptMobile.draft).toBeNull();
	});

	it("discards an unreadable draft with no client bound", async () => {
		const disk = backend();
		disk.values.set(keybindingKey, { id: "k0", baseRevision: -1 });
		const controller = new LocalPreferencesController("hub", disk.port);
		expect(controller.getSnapshot().keybindings.draftUnreadable).toBe(true);

		await controller.discardKeybindings();

		expect(disk.values.has(keybindingKey)).toBe(false);
		expect(controller.getSnapshot().keybindings.draftUnreadable).toBe(false);
	});

	it("a port failure during discard rejects rather than throwing synchronously", async () => {
		const port: NativePreferenceDraftBackend = {
			createId: () => "id-1",
			get: () => "{not json",
			set: () => {},
			delete: () => {},
			deleteIf: () => {
				throw new Error("disk unavailable");
			},
			replaceIf: () => false,
		};
		const controller = new LocalPreferencesController("hub", port);
		await expect(controller.discardTranscript()).rejects.toThrow("disk unavailable");
	});

	// RoboRev round 18 Medium A: a value getSnapshot() classified carries its
	// original bytes, so a concurrent writer's replacement under DIFFERENT
	// bytes must survive a discard attempt even when it compares semantically
	// equal to what was read - it is a write this controller never classified.
	it("refuses a discard once the classified record has been replaced under different bytes, though equivalent JSON", async () => {
		const disk = deviceStore();
		disk.raw.set(transcriptKey, '{\n  "id" : "a" ,\n  "baseRevision": -1\n}');
		const controller = new LocalPreferencesController("hub", disk.port);
		expect(controller.getSnapshot().transcriptMobile.draftUnreadable).toBe(true);

		// Reordered, no whitespace: the same fields, different bytes.
		const replacement = '{"baseRevision":-1,"id":"a"}';
		disk.raw.set(transcriptKey, replacement);

		await controller.discardTranscript();

		expect(disk.raw.get(transcriptKey)).toBe(replacement);
		// A fresh read - never the discard call itself - is what shows the
		// replaced record is still there: getSnapshot() has no cached result to
		// go stale (round 18 Medium B's class, avoided here by construction).
		expect(controller.getSnapshot().transcriptMobile.draftUnreadable).toBe(true);
	});

	it("keeps one hub's drafts out of another's", () => {
		const disk = backend();
		disk.values.set(transcriptKey, {
			id: "d0",
			layout: "mobile",
			baseRevision: 4,
			config,
			writeUncertain: false,
		});
		const a = new LocalPreferencesController("hub", disk.port);
		const b = new LocalPreferencesController("other-hub", disk.port);

		expect(a.getSnapshot().transcriptMobile.draft).not.toBeNull();
		expect(b.getSnapshot().transcriptMobile.draft).toBeNull();
	});
});
