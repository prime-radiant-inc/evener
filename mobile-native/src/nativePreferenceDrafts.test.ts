import { describe, expect, it } from "vitest";
import { fakeDraftBackend } from "./draftBackend.testkit";
import {
	draftUnreadableAfterDiscard,
	localDraftIsUnreadable,
	matchesStoredBytes,
	nativeKeybindingDrafts,
	nativeTranscriptDrafts,
	parseDraftBytes,
} from "./nativePreferenceDrafts";

// The keybindings store's discardClassified and its settle paths (a save,
// a discard or a rebase adopting a checkpoint replaced under them) branch on
// removeIf/replaceIf's own boolean: a refusal means another writer replaced
// the record, and the caller must adopt what is actually on disk rather than
// assume its own write succeeded. nativeKeybindingDrafts must propagate the
// backend's own deleteIf/replaceIf verdict faithfully, never silently
// reporting a refusal as if it had written the record (or vice versa).
const checkpoint = { id: "draft-1", baseRevision: 3, rules: [], writeUncertain: false };

// Mirrors the real backend's own get() (NativePreferencesProvider.tsx), which
// is Storage-backed and cannot be reached directly from this package's tests
// - a Map of raw bytes plus parseDraftBytes stands in for it, the same
// round-trip the real backend runs on stringified values. Shared by both
// drafts' describe blocks: keybindings and transcript drafts go through the
// SAME backend.get() in production.
function rawBytesBackend() {
	const raw = new Map<string, string>();
	const b = {
		createId: () => "draft-1",
		get: (key: string) => {
			const value = raw.get(key);
			return value === undefined ? null : parseDraftBytes(value);
		},
		set: (key: string, value: unknown) => {
			raw.set(key, JSON.stringify(value));
		},
		delete: (key: string) => {
			raw.delete(key);
		},
		deleteIf: (key: string, value: unknown): boolean => {
			const stored = raw.get(key);
			if (stored === undefined) return false;
			if (stored !== value && stored !== JSON.stringify(value)) return false;
			raw.delete(key);
			return true;
		},
		replaceIf: (key: string, expected: unknown, next: unknown): boolean => {
			const stored = raw.get(key);
			if (stored === undefined) return false;
			if (stored !== expected && stored !== JSON.stringify(expected)) return false;
			raw.set(key, JSON.stringify(next));
			return true;
		},
	};
	return { raw, b };
}

describe("nativeKeybindingDrafts", () => {
	it("propagates a refused deleteIf as false through removeIf, leaving the stored record intact", () => {
		const b = fakeDraftBackend();
		b.store.set("evener.native.keybinding-draft.hub", { ...checkpoint, id: "someone-else" });
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.removeIf(checkpoint)).toBe(false);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual({ ...checkpoint, id: "someone-else" });
	});

	it("propagates a successful deleteIf as true through removeIf, clearing the stored record", () => {
		const b = fakeDraftBackend();
		b.store.set("evener.native.keybinding-draft.hub", checkpoint);
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.removeIf(checkpoint)).toBe(true);
		expect(b.store.has("evener.native.keybinding-draft.hub")).toBe(false);
	});

	it("classifies bytes it cannot parse as an unreadable record, and clears them", () => {
		// The real backend hands back a value it cannot parse as its RAW bytes
		// rather than throwing, so the shared store reads it as an unreadable
		// record (draftUnreadable) instead of a dead port.
		const { raw, b } = rawBytesBackend();
		const storage = nativeKeybindingDrafts("hub", b);
		raw.set("evener.native.keybinding-draft.hub", "{not json");

		// Read back as the raw string, which is not a checkpoint.
		expect(storage.load()).toBe("{not json");

		// And removable by handing those same bytes back.
		expect(storage.removeIf("{not json" as never)).toBe(true);
		expect(storage.load()).toBeNull();
	});

	it("classifies a stored JSON null as an unreadable record, distinct from no record at all", () => {
		// A stored key whose bytes parse to JSON null is a PRESENT record - the
		// DraftPort contract treats a bare `null` as "no record", so handing
		// that back unchanged would make this record undiscardable through the
		// unreadable-draft recovery path.
		const { raw, b } = rawBytesBackend();
		const storage = nativeKeybindingDrafts("hub", b);
		raw.set("evener.native.keybinding-draft.hub", "null");

		const loaded = storage.load();
		expect(loaded).not.toBeNull();

		expect(storage.removeIf(loaded as never)).toBe(true);
		expect(storage.load()).toBeNull();
	});

	it("propagates a refused replaceIf as false through replaceIf, leaving the stored record intact", () => {
		const b = fakeDraftBackend();
		const someoneElse = { ...checkpoint, id: "someone-else" };
		b.store.set("evener.native.keybinding-draft.hub", someoneElse);
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.replaceIf(checkpoint, { ...checkpoint, writeUncertain: false })).toBe(false);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual(someoneElse);
	});

	it("propagates a successful replaceIf as true through replaceIf, writing the new record", () => {
		const b = fakeDraftBackend();
		b.store.set("evener.native.keybinding-draft.hub", checkpoint);
		const storage = nativeKeybindingDrafts("hub", b);
		const next = { ...checkpoint, writeUncertain: false };

		expect(storage.replaceIf(checkpoint, next)).toBe(true);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual(next);
	});
});

describe("nativeTranscriptDrafts", () => {
	it("treats a stored JSON null as no draft, not an unreadable record", () => {
		// Both drafts share one backend.get(): parseDraftBytes hands back the
		// RAW string "null" for a stored JSON null so the KEYBINDINGS port can
		// classify it as a present-but-unreadable record (see above). The
		// transcript port has no unreadable-record recovery path, so it keeps
		// the pre-existing meaning of a stored null - no draft - rather than
		// have TranscriptDraftRepository's validateCheckpoint throw on it and
		// strand the section in storageUnavailable.
		const { raw, b } = rawBytesBackend();
		raw.set("evener.native.transcript-draft.hub", "null");
		const storage = nativeTranscriptDrafts("hub", b);

		expect(storage.load()).toBeNull();
	});

	it("still loads a real stored checkpoint", () => {
		const { raw, b } = rawBytesBackend();
		const transcriptCheckpoint = { id: "t1", baseRevision: 2, config: {}, writeUncertain: false };
		raw.set("evener.native.transcript-draft.hub", JSON.stringify(transcriptCheckpoint));
		const storage = nativeTranscriptDrafts("hub", b);

		expect(storage.load()).toEqual(transcriptCheckpoint);
	});
});

describe("draftUnreadableAfterDiscard", () => {
	it("clears the notice on a plain removal", () => {
		expect(draftUnreadableAfterDiscard("removed", false)).toBe(false);
	});

	it("clears the notice when nothing was stored", () => {
		expect(draftUnreadableAfterDiscard("absent", false)).toBe(false);
	});

	it("clears the notice when a refusal's replacement now decodes as valid", () => {
		expect(draftUnreadableAfterDiscard("refused", true)).toBe(false);
	});

	it("keeps the notice when a refusal's replacement is still unreadable", () => {
		expect(draftUnreadableAfterDiscard("refused", false)).toBe(true);
	});
});

describe("localDraftIsUnreadable", () => {
	const isReadable = (value: unknown) =>
		typeof value === "object" && value !== null && "id" in value;

	it("is false when nothing is stored", () => {
		expect(localDraftIsUnreadable(null, isReadable)).toBe(false);
		expect(localDraftIsUnreadable(undefined, isReadable)).toBe(false);
	});

	it("is false when the stored record decodes as valid", () => {
		expect(localDraftIsUnreadable({ id: "d1" }, isReadable)).toBe(false);
	});

	it("is true when a record is present but does not decode", () => {
		expect(localDraftIsUnreadable("{not json", isReadable)).toBe(true);
	});
});

describe("matchesStoredBytes", () => {
	it("is false when nothing is stored", () => {
		expect(matchesStoredBytes(null, "{not json")).toBe(false);
	});

	it("matches raw bytes this build cannot parse at all", () => {
		expect(matchesStoredBytes("{not json", "{not json")).toBe(true);
	});

	it("matches a decoded value against its exact re-encoding", () => {
		const value = { id: "d1", value: "a" };
		expect(matchesStoredBytes(JSON.stringify(value), value)).toBe(true);
	});

	// A record that parses as JSON but is not a valid checkpoint (still
	// "unreadable" - see isReadableKeybindingDraft) comes back from
	// parseDraftBytes as the PARSED value, not raw bytes - JSON.parse
	// succeeded, so parseDraftBytes has no reason to preserve the original
	// bytes. Noncanonical formatting in the stored bytes (key order,
	// whitespace) then defeats a byte-for-byte JSON.stringify compare even
	// though the value is unchanged.
	it("matches a parsed value against noncanonically formatted stored bytes", () => {
		const stored = '{\n  "value": "a",\n  "id": "d1"\n}';
		const value = JSON.parse(stored) as unknown;
		expect(matchesStoredBytes(stored, value)).toBe(true);
	});

	it("does not match a genuinely different value", () => {
		expect(matchesStoredBytes(JSON.stringify({ id: "d1" }), { id: "d2" })).toBe(false);
	});
});

describe("parseDraftBytes", () => {
	it("parses valid JSON", () => {
		expect(parseDraftBytes('{"id":"d1"}')).toEqual({ id: "d1" });
	});

	it("returns bytes it cannot parse unchanged, as a present-but-unreadable record", () => {
		expect(parseDraftBytes("{not json")).toBe("{not json");
	});

	it("returns the raw bytes for a stored JSON null, never the absent-record sentinel", () => {
		expect(parseDraftBytes("null")).toBe("null");
	});
});

describe("fakeDraftBackend", () => {
	it("get() returns a clone, not the live stored reference", () => {
		// The real backend re-parses stringified bytes on every get() (a fresh
		// value each time); a fake that hands back the live object lets a
		// caller mutating what it read corrupt the store without ever going
		// through set/replaceIf, masking mutation or compare-and-swap bugs.
		const b = fakeDraftBackend();
		b.store.set("k", { id: "draft-1", nested: { value: 1 } });

		const loaded = b.get("k") as { nested: { value: number } };
		loaded.nested.value = 999;

		expect(b.store.get("k")).toEqual({ id: "draft-1", nested: { value: 1 } });
	});

	it("deleteIf(key, undefined) reports false when nothing is stored, never a false match", () => {
		// A missing Map key and an `undefined` identity both stringify to
		// `undefined` itself (not a string) - a bare JSON.stringify compare
		// would see them as equal and report a match that never happened.
		const b = fakeDraftBackend();

		expect(b.deleteIf("k", undefined)).toBe(false);
	});

	it("replaceIf(key, undefined, ...) reports false when nothing is stored, never a false match", () => {
		const b = fakeDraftBackend();

		expect(b.replaceIf("k", undefined, checkpoint)).toBe(false);
		expect(b.store.has("k")).toBe(false);
	});
});
