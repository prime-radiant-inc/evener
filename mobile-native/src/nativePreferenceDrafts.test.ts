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
	retainingDraftStorage,
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
			if (JSON.stringify(values.get(key)) !== JSON.stringify(value)) return false;
			values.delete(key);
			return true;
		},
	};
	return { port, keys: () => [...values.keys()].sort() };
}

/** The real byte-aware backend (draftBackend) over a plain Map-backed
 * synchronous key-value store, with the raw bytes observable. What every
 * test below that cares about unreadable/malformed records actually runs
 * against - never a hand-rolled re-implementation of get/deleteIf, which
 * would only assert the stub. */
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
		const disk = deviceStore();
		const storage = nativeTranscriptDrafts("hub", disk.port);
		disk.raw.set("evener.native.transcript-draft.hub", "{not json");

		// Read back as the raw string, which is not a checkpoint.
		expect(storage.load()).toBe("{not json");

		// And removable by handing those same bytes back.
		storage.removeIf("{not json" as never);
		expect(storage.load()).toBeNull();
	});

	it("clears an unreadable record whose stored bytes are not canonical JSON", () => {
		const disk = deviceStore();
		const storage = nativeTranscriptDrafts("hub", disk.port);
		const key = "evener.native.transcript-draft.hub";

		// Parses, but is not a checkpoint, and its formatting is not what
		// JSON.stringify would produce - so its re-encoding cannot name it.
		disk.raw.set(key, '{\n  "id" : "" ,\n  "baseRevision": -1\n}');
		const read = storage.load();
		expect(read).not.toBeNull();
		expect(JSON.stringify(read)).not.toBe(disk.raw.get(key));

		storage.removeIf(read as never);
		expect(disk.raw.has(key)).toBe(false);
	});

	// One row per state in NativePreferencesProvider's reachability table with
	// no model at all (backgrounded, reconnecting): both take the identical
	// port path, so one test stands in for both rows. The screens' JSX gates
	// stay untested per #1526; what is pinned here is the path each button
	// calls, for either state.
	it("no model (backgrounded or reconnecting): the port path removes the record", () => {
		const disk = deviceStore();
		disk.raw.set("evener.native.transcript-draft.hub", "{not json");
		disk.raw.set("evener.native.keybinding-draft.hub", '{"id":""}');

		discardStoredTranscriptDraft(nativeTranscriptDrafts("hub", disk.port));
		discardStoredKeybindingDraft(nativeKeybindingDrafts("hub", disk.port));

		expect(disk.raw.size).toBe(0);
	});

	it("a discard naming a DIFFERENT record does not remove what is stored", () => {
		const disk = deviceStore();
		const storage = nativeTranscriptDrafts("hub", disk.port);
		const key = "evener.native.transcript-draft.hub";

		// A record that parses but is not a checkpoint, stored non-canonically.
		disk.raw.set(key, '{\n  "id" : "a" ,\n  "baseRevision": -1\n}');
		const read = storage.load();
		expect(read).not.toBeNull();

		// A stale cleanup for some OTHER checkpoint must not remove this record
		// just because nothing has been written since it was read.
		storage.removeIf({ id: "b", baseRevision: 9 } as never);
		expect(disk.raw.has(key)).toBe(true);

		// The record this read actually named still removes it.
		storage.removeIf(read as never);
		expect(disk.raw.has(key)).toBe(false);
	});

	it("a stored JSON null is an unreadable record, not a missing key", () => {
		const disk = deviceStore();
		const storage = nativeTranscriptDrafts("hub", disk.port);
		const key = "evener.native.transcript-draft.hub";
		disk.raw.set(key, "null");

		// Returning it as null would be indistinguishable from no record at all,
		// which is how it became invisible AND unremovable.
		const read = storage.load();
		expect(read).not.toBeNull();

		storage.removeIf(read as never);
		expect(disk.raw.has(key)).toBe(false);
	});

	it("a port failure during a local discard propagates, so a screen can show it", () => {
		const storage = nativeTranscriptDrafts("hub", {
			createId: () => "id-1",
			get: () => "{not json",
			set: () => {},
			delete: () => {},
			deleteIf: () => {
				throw new Error("disk unavailable");
			},
		});
		// The provider awaits this inside an async function, so a throw here is
		// the rejection its callers' error handlers already know how to show.
		expect(() => discardStoredTranscriptDraft(storage)).toThrow("disk unavailable");
	});

	it("no session: there is no hub key to clear, and nothing throws", () => {
		// hubId null never reaches a port at all (the provider returns early), so
		// the row has no record and no screen; the ports refuse a blank id.
		const noop = {
			getItemSync: () => null,
			setItemSync: () => {},
			removeItemSync: () => {},
		};
		expect(() => nativeTranscriptDrafts("", draftBackend(noop, () => "id-1"))).toThrow(
			"A hub id is required",
		);
	});

	it("a READABLE record with reordered keys is discarded and does not come back", () => {
		const disk = deviceStore();
		const storage = nativeTranscriptDrafts("hub", disk.port);
		const key = "evener.native.transcript-draft.hub";

		// A perfectly valid checkpoint, stored with its keys in a different order
		// from this build's canonical encoding - an older build, or another writer.
		disk.raw.set(
			key,
			JSON.stringify({
				writeUncertain: false,
				config,
				baseRevision: 4,
				layout: "mobile",
				id: "d1",
			}),
		);

		// The ordinary discard path rebuilds the checkpoint before handing it back,
		// so the object is not the one the port returned. It must still remove the
		// record, or the store reports success and the draft returns next launch.
		const decoded = storage.load() as Record<string, unknown>;
		storage.removeIf({
			id: decoded.id,
			layout: decoded.layout,
			baseRevision: decoded.baseRevision,
			config: decoded.config,
			writeUncertain: decoded.writeUncertain,
		} as never);
		expect(disk.raw.has(key)).toBe(false);
	});

	// RoboRev round 18: a value read back through get() carries its ORIGINAL
	// bytes (decodedFrom); a concurrent writer's replacement compares
	// semantically equal but is a DIFFERENT write under different bytes, and
	// must survive - sameRecord is for a checkpoint this build constructed
	// itself and never read back, which has no original bytes to be exact about.
	it("a concurrent replacement with equivalent JSON but different bytes is not removed when the value came from a read", () => {
		const disk = deviceStore();
		const storage = nativeTranscriptDrafts("hub", disk.port);
		const key = "evener.native.transcript-draft.hub";

		disk.raw.set(key, '{\n  "id" : "a" ,\n  "baseRevision": -1\n}');
		const read = storage.load();
		expect(read).not.toBeNull();

		// A concurrent writer replaces it with the SAME fields under DIFFERENT
		// bytes (reordered, no whitespace) - a write this store never read.
		const replacement = '{"baseRevision":-1,"id":"a"}';
		disk.raw.set(key, replacement);

		storage.removeIf(read as never);
		expect(disk.raw.get(key)).toBe(replacement);
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

	// RoboRev round 17 Medium 1 (eighth raise): a model's OWN repository
	// classifies a record and retains its raw identity for as long as that
	// model instance lives (round 16's createDraftRepository), but the
	// no-model discard path runs with NO model - including after one that
	// classified this record has since been disposed (a client dropped, a
	// hub reconnect). A fresh reload at that point names whatever is stored
	// NOW, not the record that justified showing the discard button.
	// retainingDraftStorage keeps the identity in the wrapper itself, which
	// the provider owns for the hub's whole lifetime - outliving any one
	// model - so the no-model path can still discard by it.
	it("discards the record most recently loaded, not a fresh reload - refusing once that record has been replaced", () => {
		const disk = deviceStore();
		const key = "evener.native.transcript-draft.hub";
		const storage = retainingDraftStorage(nativeTranscriptDrafts("hub", disk.port));

		// Classified unreadable - exactly what a model's own repository does
		// while it is still alive.
		disk.raw.set(key, "{not json");
		storage.load();

		// The model is disposed here (a client dropped, a hub reconnect); no
		// model exists to reclassify anything. Another store or app version
		// then replaces the SAME record with a valid, newer checkpoint.
		const newer = { id: "d1", layout: "mobile" as const, baseRevision: 4, config, writeUncertain: false };
		storage.save(newer);

		// The no-model discard path names the record load() actually
		// classified, not whatever is stored now: it refuses, and the newer
		// checkpoint survives. A refusal reports false - a caller must not
		// project "discarded" on it - and re-classifies against the newer
		// record, exactly as a live model's discardUnreadable+restoreDraft
		// would, so a follow-up discard targets what is actually there now.
		expect(storage.discardLastLoaded()).toBe(false);
		expect(disk.raw.get(key)).toBe(JSON.stringify(newer));
		expect(storage.discardLastLoaded()).toBe(true);
		expect(disk.raw.has(key)).toBe(false);
	});

	it("discardLastLoaded reports true once the named record is actually removed", () => {
		const disk = deviceStore();
		const key = "evener.native.transcript-draft.hub";
		const storage = retainingDraftStorage(nativeTranscriptDrafts("hub", disk.port));

		disk.raw.set(key, "{not json");
		storage.load();

		expect(storage.discardLastLoaded()).toBe(true);
		expect(disk.raw.has(key)).toBe(false);
	});

	it("discardLastLoaded is a no-op before anything has ever been loaded", () => {
		const disk = deviceStore();
		const storage = retainingDraftStorage(nativeTranscriptDrafts("hub", disk.port));
		expect(storage.discardLastLoaded()).toBe(false);
		expect(disk.raw.size).toBe(0);
	});
});
