// The device store the draft ports write through, over any synchronous
// key-value store. Exported as a factory so its compare/remove contract can be
// tested without the platform store.

/** A value serialized with object keys in sorted order, so two encodings of the
 * SAME record compare equal however they were written. `JSON.stringify` alone
 * does not: it preserves insertion order, so a checkpoint rebuilt by the store's
 * own validator encodes differently from the bytes it was decoded from whenever
 * those bytes came from another writer or an older build. */
function canonical(value: unknown): string {
	if (value === null || typeof value !== "object") return JSON.stringify(value) ?? "null";
	if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
	const entries = Object.entries(value as Record<string, unknown>)
		.filter(([, member]) => member !== undefined)
		.sort(([left], [right]) => (left < right ? -1 : left > right ? 1 : 0));
	return `{${entries.map(([key, member]) => `${JSON.stringify(key)}:${canonical(member)}`).join(",")}}`;
}

/** Whether the stored bytes decode to the record `value` names. This is what
 * "still this one" means for a readable record: the store's repository rebuilds
 * every checkpoint through one validator, so the object handed back here is
 * equal to - but not byte-identical with - what the port returned. */
function sameRecord(stored: string, value: unknown): boolean {
	try {
		return canonical(JSON.parse(stored)) === canonical(value);
	} catch {
		return false;
	}
}

export interface SynchronousKeyValueStore {
	getItemSync(key: string): string | null;
	setItemSync(key: string, value: string): void;
	removeItemSync(key: string): void;
}

export function draftBackend(
	store: SynchronousKeyValueStore,
	createId: () => string,
) {
	/** The bytes each decoded record CAME FROM, keyed by the object handed to the
	 * caller. A record this build cannot decode keeps whatever formatting it was
	 * written with, which JSON.stringify does not reproduce, so its own bytes are
	 * the only thing that can name it in a compare/remove - and the object the
	 * caller hands back is what says WHICH read those bytes belong to. A shared
	 * per-key "last read" would instead let any value remove the record while the
	 * bytes happened to be unchanged. Weak, so a record the caller drops is not
	 * retained here. */
	const decodedFrom = new WeakMap<object, string>();
	return {
		createId: () => createId(),
		get(key: string): unknown {
			const value = store.getItemSync(key);
			if (value === null) return null;
			try {
				const decoded: unknown = JSON.parse(value);
				// A stored JSON `null` is A RECORD, and returning it as null would be
				// indistinguishable from a missing key: the store would read "no
				// draft" and the record would be neither surfaced nor removable. Its
				// own bytes are a record no decoder accepts, which is the truth.
				if (decoded === null) return value;
				if (typeof decoded === "object") decodedFrom.set(decoded, value);
				return decoded;
			} catch {
				// Bytes this build cannot parse are still A RECORD, and the shared
				// store's own decoder is what classifies them: handing back the raw
				// string makes it an unreadable RECORD (draftUnreadable, discardable)
				// instead of a throw it can only read as a dead port.
				return value;
			}
		},
		set(key: string, value: unknown) {
			store.setItemSync(key, JSON.stringify(value));
		},
		delete(key: string) {
			store.removeItemSync(key);
		},
		deleteIf(key: string, value: unknown): boolean {
			// Synchronous compare/remove cannot interleave with a newer model's checkpoint.
			const stored = store.getItemSync(key);
			if (stored === null) return false;
			// A value decoded from get() carries the bytes it came from: those exact
			// bytes are the only thing that names the SAME write, because a
			// concurrent replacement with equivalent JSON but different formatting
			// is a DIFFERENT write that only compares semantically equal. Semantic
			// comparison is for a checkpoint this build constructed itself and never
			// read back, which has no original bytes to be exact about.
			const source = typeof value === "object" && value !== null ? decodedFrom.get(value) : undefined;
			const matches = source !== undefined ? stored === source : stored === value || sameRecord(stored, value);
			if (matches) store.removeItemSync(key);
			return matches;
		},
		replaceIf(key: string, expected: unknown, next: unknown): boolean {
			// The atomic twin of deleteIf: the identical compare, a write instead
			// of a removal - needed so settling an uncertain checkpoint cannot
			// silently overwrite a concurrent writer's newer draft the way an
			// unconditional save would.
			const stored = store.getItemSync(key);
			if (stored === null) return false;
			const source = typeof expected === "object" && expected !== null ? decodedFrom.get(expected) : undefined;
			const matches =
				source !== undefined ? stored === source : stored === expected || sameRecord(stored, expected);
			if (!matches) return false;
			const encoded = JSON.stringify(next);
			store.setItemSync(key, encoded);
			if (typeof next === "object" && next !== null) decodedFrom.set(next, encoded);
			return true;
		},
	};
}
