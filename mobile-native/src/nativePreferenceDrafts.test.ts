import { describe, expect, it } from "vitest";
import { fakeDraftBackend } from "./draftBackend.testkit";
import {
	classifyDraftRead,
	draftUnreadableAfterDiscard,
	isStoredNullRecord,
	isUnparseableDraftBytes,
	matchesStoredBytes,
	nativeKeybindingDrafts,
	nativeTranscriptDrafts,
	parseDraftBytes,
	readDraftOutcome,
	readDraftOutcomeWithValue,
	type RawStringStorage,
	rawStringDraftBackend,
} from "./nativePreferenceDrafts";

// The keybindings store's discardClassified and its settle paths (a save,
// a discard or a rebase adopting a checkpoint replaced under them) branch on
// removeIf/replaceIf's own boolean: a refusal means another writer replaced
// the record, and the caller must adopt what is actually on disk rather than
// assume its own write succeeded. nativeKeybindingDrafts must propagate the
// backend's own deleteIf/replaceIf verdict faithfully, never silently
// reporting a refusal as if it had written the record (or vice versa).
const checkpoint = { id: "draft-1", baseRevision: 3, rules: [], writeUncertain: false };

// The real backend's own get/deleteIf/replaceIf (NativePreferencesProvider.tsx)
// is Storage-backed and cannot be reached directly from this package's tests
// - a Map of raw bytes stands in for Storage, and rawStringDraftBackend is the
// same function production calls over it, not a parallel reimplementation.
function rawBytesBackend() {
	const raw = new Map<string, string>();
	const storage: RawStringStorage = {
		getItemSync: (key) => raw.get(key) ?? null,
		setItemSync: (key, value) => raw.set(key, value),
		removeItemSync: (key) => raw.delete(key),
	};
	const b = rawStringDraftBackend(storage, () => "draft-1");
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
		// The real backend hands back a value it cannot parse as a tagged
		// UnparseableDraftBytes marker rather than throwing, so the shared
		// store reads it as an unreadable record (draftUnreadable) instead of
		// a dead port.
		const { raw, b } = rawBytesBackend();
		const storage = nativeKeybindingDrafts("hub", b);
		raw.set("evener.native.keybinding-draft.hub", "{not json");

		const loaded = storage.load();
		expect(isUnparseableDraftBytes(loaded)).toBe(true);

		// And removable by handing that same identity back.
		expect(storage.removeIf(loaded as never)).toBe(true);
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
		expect(isStoredNullRecord(loaded)).toBe(true);

		expect(storage.removeIf(loaded as never)).toBe(true);
		expect(storage.load()).toBeNull();
	});

	it("classifies a stored JSON string \"null\" as an unreadable record, distinct from a stored JSON null", () => {
		// The stored bytes `"null"` (six characters, a JSON string) decode to
		// the SAME primitive a stored JSON `null` would if either returned a
		// bare string - the collision that used to let a corrupt checkpoint
		// read as no draft at all (see nativeTranscriptDrafts below).
		const { raw, b } = rawBytesBackend();
		const storage = nativeKeybindingDrafts("hub", b);
		raw.set("evener.native.keybinding-draft.hub", '"null"');

		const loaded = storage.load();
		expect(loaded).toBe("null");
		expect(isStoredNullRecord(loaded)).toBe(false);
		expect(isUnparseableDraftBytes(loaded)).toBe(false);

		expect(storage.removeIf(loaded as never)).toBe(true);
		expect(storage.load()).toBeNull();
	});

	it("propagates a refused insertIfAbsent as false through insertIfAbsent, leaving the stored record intact", () => {
		const b = fakeDraftBackend();
		const someoneElse = { ...checkpoint, id: "someone-else" };
		b.store.set("evener.native.keybinding-draft.hub", someoneElse);
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.insertIfAbsent(checkpoint)).toBe(false);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual(someoneElse);
	});

	it("propagates a successful insertIfAbsent as true through insertIfAbsent, writing the new record", () => {
		const b = fakeDraftBackend();
		const storage = nativeKeybindingDrafts("hub", b);

		expect(storage.insertIfAbsent(checkpoint)).toBe(true);
		expect(b.store.get("evener.native.keybinding-draft.hub")).toEqual(checkpoint);
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

	it("removes a stored record through the shared backend with a different key order", () => {
		const { raw, b } = rawBytesBackend();
		const storage = nativeKeybindingDrafts("hub", b);
		raw.set(
			"evener.native.keybinding-draft.hub",
			JSON.stringify({ writeUncertain: false, rules: [], baseRevision: 3, id: "draft-1" }),
		);

		expect(storage.removeIf(checkpoint)).toBe(true);
		expect(raw.has("evener.native.keybinding-draft.hub")).toBe(false);
	});
});

// The valid replaceIf/insertIfAbsent cases above run through the in-memory
// fake (draftBackend.testkit.ts). These exercise the SAME contracts directly
// through rawStringDraftBackend - the function production wraps Storage in -
// over a Map-backed fake string port, so the real backend's own compare-and-
// swap behavior (byte parsing, marker identity, canonical key-order compare,
// and what it leaves on disk) is pinned, not just the fake that stands in
// for it. Each assertion names both the returned verdict and the persisted
// bytes, so a CAS that reports success without writing - or writes without
// reporting - cannot pass.
describe("rawStringDraftBackend compare-and-swap", () => {
	const key = "evener.native.keybinding-draft.hub";
	// Observably distinct from `checkpoint` in a field the writer must persist
	// (not merely a different key order), so a replaceIf that reports success
	// WITHOUT writing cannot satisfy the persisted-bytes assertion below.
	const next = { ...checkpoint, baseRevision: 4 };

	it("insertIfAbsent writes the new record and reports true when the key is empty", () => {
		const { raw, b } = rawBytesBackend();

		expect(b.insertIfAbsent(key, checkpoint)).toBe(true);
		expect(raw.get(key)).toBe(JSON.stringify(checkpoint));
	});

	it("insertIfAbsent refuses an occupied key, leaving the stored bytes untouched", () => {
		const { raw, b } = rawBytesBackend();
		const someoneElse = { ...checkpoint, id: "someone-else" };
		raw.set(key, JSON.stringify(someoneElse));

		expect(b.insertIfAbsent(key, checkpoint)).toBe(false);
		expect(raw.get(key)).toBe(JSON.stringify(someoneElse));
	});

	it("insertIfAbsent refuses bytes it cannot parse - a present record is occupied, not absent", () => {
		const { raw, b } = rawBytesBackend();
		raw.set(key, "{not json");

		expect(b.insertIfAbsent(key, checkpoint)).toBe(false);
		expect(raw.get(key)).toBe("{not json");
	});

	it("insertIfAbsent refuses a stored JSON null - present, distinct from no record", () => {
		const { raw, b } = rawBytesBackend();
		raw.set(key, "null");

		expect(b.insertIfAbsent(key, checkpoint)).toBe(false);
		expect(raw.get(key)).toBe("null");
	});

	it("replaceIf writes the next record and reports true when expected still names the stored one", () => {
		const { raw, b } = rawBytesBackend();
		raw.set(key, JSON.stringify(checkpoint));

		expect(b.replaceIf(key, checkpoint, next)).toBe(true);
		expect(raw.get(key)).toBe(JSON.stringify(next));
	});

	it("replaceIf matches identity across key order, not byte order", () => {
		const { raw, b } = rawBytesBackend();
		raw.set(key, JSON.stringify({ writeUncertain: false, rules: [], baseRevision: 3, id: "draft-1" }));
		const reordered = { id: "draft-1", baseRevision: 3, rules: [], writeUncertain: false };

		expect(b.replaceIf(key, reordered, next)).toBe(true);
		expect(raw.get(key)).toBe(JSON.stringify(next));
	});

	it("replaceIf refuses a stale identity, leaving the newer record untouched", () => {
		const { raw, b } = rawBytesBackend();
		const newer = { ...checkpoint, id: "someone-else" };
		raw.set(key, JSON.stringify(newer));

		expect(b.replaceIf(key, checkpoint, next)).toBe(false);
		expect(raw.get(key)).toBe(JSON.stringify(newer));
	});

	it("replaceIf refuses when nothing is stored rather than creating a record", () => {
		const { raw, b } = rawBytesBackend();

		expect(b.replaceIf(key, checkpoint, next)).toBe(false);
		expect(raw.has(key)).toBe(false);
	});

	it("replaceIf replaces unparseable bytes only when named by their own parsed identity", () => {
		const { raw, b } = rawBytesBackend();
		raw.set(key, "{not json");

		expect(b.replaceIf(key, parseDraftBytes("{different"), next)).toBe(false);
		expect(raw.get(key)).toBe("{not json");

		expect(b.replaceIf(key, parseDraftBytes("{not json"), next)).toBe(true);
		expect(raw.get(key)).toBe(JSON.stringify(next));
	});

	it("replaceIf replaces a stored JSON null only by its StoredNullRecord identity, never a bare null", () => {
		const { raw, b } = rawBytesBackend();
		raw.set(key, "null");

		// A bare null names "no record" and must not collide with the present
		// stored-null marker (the collision parseDraftBytes's brand closes).
		expect(b.replaceIf(key, null, next)).toBe(false);
		expect(raw.get(key)).toBe("null");

		expect(b.replaceIf(key, b.get(key), next)).toBe(true);
		expect(raw.get(key)).toBe(JSON.stringify(next));
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

	it("does not treat a stored JSON string \"null\" as no draft - it is a present, invalid checkpoint", () => {
		// Distinct from the stored-JSON-null case above: the bytes `"null"`
		// (a JSON string) decode to the JS string "null", which used to be
		// indistinguishable from a stored JSON null once both came back as
		// the bare string "null" - silently treating a corrupt checkpoint as
		// no draft at all, contrary to this repository's contract to reject
		// invalid checkpoints (see preferenceDraftRepository's
		// validateCheckpoint, which throws on exactly this shape).
		const { raw, b } = rawBytesBackend();
		raw.set("evener.native.transcript-draft.hub", '"null"');
		const storage = nativeTranscriptDrafts("hub", b);

		expect(storage.load()).toBe("null");
	});

	it("still loads a real stored checkpoint", () => {
		const { raw, b } = rawBytesBackend();
		const transcriptCheckpoint = { id: "t1", baseRevision: 2, config: {}, writeUncertain: false };
		raw.set("evener.native.transcript-draft.hub", JSON.stringify(transcriptCheckpoint));
		const storage = nativeTranscriptDrafts("hub", b);

		expect(storage.load()).toEqual(transcriptCheckpoint);
	});
});

describe("classifyDraftRead", () => {
	const isReadable = (value: unknown) =>
		typeof value === "object" && value !== null && "id" in value;

	it("is absent when nothing is stored", () => {
		expect(classifyDraftRead(null, isReadable)).toBe("absent");
		expect(classifyDraftRead(undefined, isReadable)).toBe("absent");
	});

	it("is readable when the stored record decodes as valid", () => {
		expect(classifyDraftRead({ id: "d1" }, isReadable)).toBe("readable");
	});

	it("is unreadable when a record is present but does not decode", () => {
		expect(classifyDraftRead("{not json", isReadable)).toBe("unreadable");
	});
});

describe("readDraftOutcome", () => {
	const isReadable = (value: unknown) =>
		typeof value === "object" && value !== null && "id" in value;

	it("classifies a normal read the same as classifyDraftRead", () => {
		expect(readDraftOutcome({ load: () => null }, isReadable)).toBe("absent");
		expect(readDraftOutcome({ load: () => ({ id: "d1" }) }, isReadable)).toBe("readable");
		expect(readDraftOutcome({ load: () => "{not json" }, isReadable)).toBe("unreadable");
	});

	it("degrades a throwing port to storageUnavailable, never an uncaught exception", () => {
		const storage = {
			load: () => {
				throw new Error("disk unavailable");
			},
		};

		expect(readDraftOutcome(storage, isReadable)).toBe("storageUnavailable");
	});
});

describe("readDraftOutcomeWithValue", () => {
	const isReadable = (value: unknown) =>
		typeof value === "object" && value !== null && "id" in value;

	it("returns the classification and the exact value from one read", () => {
		const record = { id: "d1" };
		expect(readDraftOutcomeWithValue({ load: () => record }, isReadable)).toEqual({
			outcome: "readable",
			value: record,
		});
		expect(readDraftOutcomeWithValue({ load: () => null }, isReadable)).toEqual({
			outcome: "absent",
			value: null,
		});
	});

	it("calls load once and degrades a throwing port", () => {
		let calls = 0;
		const storage = {
			load: () => {
				calls++;
				return { id: "d1" };
			},
		};
		expect(readDraftOutcomeWithValue(storage, isReadable).outcome).toBe("readable");
		expect(calls).toBe(1);
		expect(
		readDraftOutcomeWithValue(
			{
				load: () => {
					throw new Error("disk unavailable");
				},
			},
			isReadable,
		),
	).toEqual({ outcome: "storageUnavailable", value: undefined });
	});
});

describe("matchesStoredBytes", () => {
	it("is false when nothing is stored", () => {
		expect(matchesStoredBytes(null, "{not json")).toBe(false);
	});

	it("matches raw bytes this build cannot parse at all, by their UnparseableDraftBytes identity", () => {
		expect(matchesStoredBytes("{not json", parseDraftBytes("{not json"))).toBe(true);
	});

	it("does not match UnparseableDraftBytes against a genuinely different raw value", () => {
		expect(matchesStoredBytes("{not json", parseDraftBytes("{different"))).toBe(false);
	});

	it("matches a stored JSON null against its StoredNullRecord identity", () => {
		expect(matchesStoredBytes("null", parseDraftBytes("null"))).toBe(true);
	});

	it("does not match a StoredNullRecord identity against a stored JSON string \"null\"", () => {
		// The two decode to the same JS primitive if either comes back as a
		// bare string - the collision parseDraftBytes's tagged markers exist
		// to close.
		expect(matchesStoredBytes('"null"', parseDraftBytes("null"))).toBe(false);
	});

	it("matches a decoded value against its exact re-encoding", () => {
		const value = { id: "d1", value: "a" };
		expect(matchesStoredBytes(JSON.stringify(value), value)).toBe(true);
	});

	// canonicalJson sorts every object's keys recursively, so two
	// structurally equal values compare equal regardless of key order - not
	// only the whitespace a byte-for-byte JSON.stringify compare tolerates.
	it("matches a parsed value against noncanonically formatted stored bytes", () => {
		const stored = '{\n  "value": "a",\n  "id": "d1"\n}';
		const value = JSON.parse(stored) as unknown;
		expect(matchesStoredBytes(stored, value)).toBe(true);
	});

	it("matches two values with the same fields in different key order", () => {
		const stored = JSON.stringify({ a: 1, b: 2 });
		expect(matchesStoredBytes(stored, { b: 2, a: 1 })).toBe(true);
	});

	it("does not match a genuinely different value", () => {
		expect(matchesStoredBytes(JSON.stringify({ id: "d1" }), { id: "d2" })).toBe(false);
	});

	it("does not collide with valid JSON shaped like the unreadable marker", () => {
		const identity = parseDraftBytes("{not json");
		expect(matchesStoredBytes(JSON.stringify(identity), identity)).toBe(false);
	});

	it("does not collide with valid JSON shaped like the stored-null marker", () => {
		const identity = parseDraftBytes("null");
		expect(matchesStoredBytes(JSON.stringify(identity), identity)).toBe(false);
	});
});

describe("parseDraftBytes", () => {
	it("parses valid JSON", () => {
		expect(parseDraftBytes('{"id":"d1"}')).toEqual({ id: "d1" });
	});

	it("returns a tagged UnparseableDraftBytes marker for bytes it cannot parse, never a bare string", () => {
		const value = parseDraftBytes("{not json");
		expect(isUnparseableDraftBytes(value)).toBe(true);
		expect((value as { raw: string }).raw).toBe("{not json");
	});

	it("returns a tagged StoredNullRecord marker for a stored JSON null, never the absent-record sentinel", () => {
		const value = parseDraftBytes("null");
		expect(value).not.toBeNull();
		expect(isStoredNullRecord(value)).toBe(true);
	});

	it("returns the plain JS string for a stored JSON string \"null\", distinct from a stored JSON null", () => {
		// Both used to collide on the bare string "null" - the bug this
		// marker design closes (see nativeTranscriptDrafts' own test).
		const value = parseDraftBytes('"null"');
		expect(value).toBe("null");
		expect(isStoredNullRecord(value)).toBe(false);
	});

	it("never mistakes a real checkpoint carrying an overloaded kind field for the stored-null sentinel", () => {
		// A future checkpoint shape could legitimately have its own `kind`
		// field; the marker predicates must not fire on that field alone, or a
		// valid, present record reads as no draft (isStoredNullRecord) or an
		// unreadable one (isUnparseableDraftBytes) instead of itself.
		expect(isStoredNullRecord({ kind: "storedNull", id: "d1", value: "a" })).toBe(false);
		expect(isUnparseableDraftBytes({ kind: "unparseable", raw: "irrelevant", id: "d1" })).toBe(false);
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
		// A missing Map key and an `undefined` identity both canonicalize to
		// the string "undefined" itself - a bare compare would see them as
		// equal and report a match that never happened.
		const b = fakeDraftBackend();

		expect(b.deleteIf("k", undefined)).toBe(false);
	});

	it("replaceIf(key, undefined, ...) reports false when nothing is stored, never a false match", () => {
		const b = fakeDraftBackend();

		expect(b.replaceIf("k", undefined, checkpoint)).toBe(false);
		expect(b.store.has("k")).toBe(false);
	});
});

describe("draftUnreadableAfterDiscard", () => {
	it("keeps the notice when the current record is still unreadable", () => {
		expect(draftUnreadableAfterDiscard("unreadable")).toBe(true);
	});

	it("clears the notice when the current record is readable or absent", () => {
		expect(draftUnreadableAfterDiscard("readable")).toBe(false);
		expect(draftUnreadableAfterDiscard("absent")).toBe(false);
	});
});
