import {
	discardStoredKeybindingDraft,
	discardStoredTranscriptDraft,
	makeTranscriptDisplayConfig,
} from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { draftBackend } from "./nativeDraftBackend";
import {
	type NativePreferenceDraftBackend,
	nativeKeybindingDrafts,
	nativeTranscriptDrafts,
} from "./nativePreferenceDrafts";

const config = makeTranscriptDisplayConfig();
const transcriptCheckpoint = {
	id: "t1",
	layout: "mobile" as const,
	baseRevision: 4,
	config,
	writeUncertain: true,
};
const keybindingCheckpoint = {
	id: "k1",
	baseRevision: 7,
	rules: [{ action: "palette.open", chord: "Meta+P" }],
	writeUncertain: false,
};

/** The device store, with the keys it was written under observable. */
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
	return { port, keys: () => [...values.keys()].sort() };
}

describe("native preference drafts", () => {
	it("keeps one hub's drafts out of another's", () => {
		const disk = backend();
		const a = nativeTranscriptDrafts("a", disk.port);
		const b = nativeTranscriptDrafts("b", disk.port);

		a.save(transcriptCheckpoint);
		b.save({ ...transcriptCheckpoint, id: "t2", baseRevision: 9 });

		expect(a.load()).toEqual(transcriptCheckpoint);
		expect(b.load()).toMatchObject({ baseRevision: 9 });
		a.removeIf(transcriptCheckpoint);
		expect(a.load()).toBeNull();
		expect(b.load()).toMatchObject({ baseRevision: 9 });
	});

	it("keeps the two sections' drafts out of each other, on one hub", () => {
		const disk = backend();
		const transcript = nativeTranscriptDrafts("hub", disk.port);
		const keybindings = nativeKeybindingDrafts("hub", disk.port);

		transcript.save(transcriptCheckpoint);
		keybindings.save(keybindingCheckpoint);

		expect(disk.keys()).toEqual([
			"evener.native.keybinding-draft.hub",
			"evener.native.transcript-draft.hub",
		]);
		transcript.removeIf(transcriptCheckpoint);
		expect(transcript.load()).toBeNull();
		expect(keybindings.load()).toEqual(keybindingCheckpoint);
	});

	it("classifies bytes it cannot parse as an unreadable record, and clears them", () => {
		// The device store as the provider wraps it: a value it cannot parse comes
		// back as its RAW bytes rather than throwing, so the shared store reads it
		// as an unreadable record (draftUnreadable) instead of a dead port.
		const raw = new Map<string, string>();
		const port: NativePreferenceDraftBackend = {
			createId: () => "1",
			get: (key) => {
				const value = raw.get(key);
				if (value === undefined) return null;
				try {
					return JSON.parse(value);
				} catch {
					return value;
				}
			},
			set: (key, value) => {
				raw.set(key, JSON.stringify(value));
			},
			delete: (key) => {
				raw.delete(key);
			},
			deleteIf: (key, value: unknown) => {
				const stored = raw.get(key);
				if (stored === undefined) return;
				if (stored === value || stored === JSON.stringify(value)) raw.delete(key);
			},
		};
		const storage = nativeTranscriptDrafts("hub", port);
		raw.set("evener.native.transcript-draft.hub", "{not json");

		// Read back as the raw string, which is not a checkpoint.
		expect(storage.load()).toBe("{not json");

		// And removable by handing those same bytes back.
		storage.removeIf("{not json" as never);
		expect(storage.load()).toBeNull();
	});

	it("clears an unreadable record whose stored bytes are not canonical JSON", () => {
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
		const storage = nativeTranscriptDrafts("hub", draftBackend(store, () => "id-1"));
		const key = "evener.native.transcript-draft.hub";

		// Parses, but is not a checkpoint, and its formatting is not what
		// JSON.stringify would produce - so its re-encoding cannot name it.
		raw.set(key, '{\n  "id" : "" ,\n  "baseRevision": -1\n}');
		const read = storage.load();
		expect(read).not.toBeNull();
		expect(JSON.stringify(read)).not.toBe(raw.get(key));

		storage.removeIf(read as never);
		expect(raw.has(key)).toBe(false);
	});

	it("clears an unreadable record with no client and no store", () => {
		// What the provider has while backgrounded or reconnecting: the ports for
		// this hub, and no model at all. The escape hatch must still work, because
		// that is exactly when a user is stuck behind such a record.
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
		const port = draftBackend(store, () => "id-1");
		raw.set("evener.native.transcript-draft.hub", "{not json");
		raw.set("evener.native.keybinding-draft.hub", '{"id":""}');

		discardStoredTranscriptDraft(nativeTranscriptDrafts("hub", port));
		discardStoredKeybindingDraft(nativeKeybindingDrafts("hub", port));

		expect(raw.size).toBe(0);
	});

	it("a discard naming a DIFFERENT record does not remove what is stored", () => {
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
		const port = draftBackend(store, () => "id-1");
		const storage = nativeTranscriptDrafts("hub", port);
		const key = "evener.native.transcript-draft.hub";

		// A record that parses but is not a checkpoint, stored non-canonically.
		raw.set(key, '{\n  "id" : "a" ,\n  "baseRevision": -1\n}');
		const read = storage.load();
		expect(read).not.toBeNull();

		// A stale cleanup for some OTHER checkpoint must not remove this record
		// just because nothing has been written since it was read.
		storage.removeIf({ id: "b", baseRevision: 9 } as never);
		expect(raw.has(key)).toBe(true);

		// The record this read actually named still removes it.
		storage.removeIf(read as never);
		expect(raw.has(key)).toBe(false);
	});

	it("a stored JSON null is an unreadable record, not a missing key", () => {
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
		const storage = nativeTranscriptDrafts("hub", draftBackend(store, () => "id-1"));
		const key = "evener.native.transcript-draft.hub";
		raw.set(key, "null");

		// Returning it as null would be indistinguishable from no record at all,
		// which is how it became invisible AND unremovable.
		const read = storage.load();
		expect(read).not.toBeNull();

		storage.removeIf(read as never);
		expect(raw.has(key)).toBe(false);
	});

	it("refuses a blank hub id rather than colliding every hub on one key", () => {
		const disk = backend();
		expect(() => nativeTranscriptDrafts(" ", disk.port)).toThrow(
			"A hub id is required",
		);
		expect(() => nativeKeybindingDrafts("", disk.port)).toThrow(
			"A hub id is required",
		);
	});
});
