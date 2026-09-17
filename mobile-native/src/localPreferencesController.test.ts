import { makeTranscriptDisplayConfig } from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { LocalPreferencesController } from "./localPreferencesController";
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
			if (JSON.stringify(values.get(key)) === JSON.stringify(value))
				values.delete(key);
		},
	};
	return { port, values };
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
		};
		const controller = new LocalPreferencesController("hub", port);
		await expect(controller.discardTranscript()).rejects.toThrow("disk unavailable");
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
