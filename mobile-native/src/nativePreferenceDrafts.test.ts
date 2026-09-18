import { describe, expect, it } from "vitest";
import { fakeDraftBackend } from "./draftBackend.testkit";
import { nativeKeybindingDrafts, parseDraftBytes } from "./nativePreferenceDrafts";

// The keybindings store's discardClassified and its settle paths (a save,
// a discard or a rebase adopting a checkpoint replaced under them) branch on
// removeIf/replaceIf's own boolean: a refusal means another writer replaced
// the record, and the caller must adopt what is actually on disk rather than
// assume its own write succeeded. nativeKeybindingDrafts must propagate the
// backend's own deleteIf/replaceIf verdict faithfully, never silently
// reporting a refusal as if it had written the record (or vice versa).
const checkpoint = { id: "draft-1", baseRevision: 3, rules: [], writeUncertain: false };

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

	// Mirrors the real backend's own get() (NativePreferencesProvider.tsx),
	// which is Storage-backed and cannot be reached directly from this
	// package's tests - a Map of raw bytes plus parseDraftBytes stands in for
	// it, the same round-trip the real backend runs on stringified values.
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
