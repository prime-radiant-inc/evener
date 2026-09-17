/** The device store the draft ports write through, over any synchronous
 * key-value store. Exported as a factory so its compare/remove contract can be
 * tested without the platform store. */
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
		deleteIf(key: string, value: unknown) {
			// Synchronous compare/remove cannot interleave with a newer model's checkpoint.
			const stored = store.getItemSync(key);
			if (stored === null) return;
			// Three ways a value names what is stored, all of them about THIS value:
			// the raw bytes handed back for a record no decoder accepts, the bytes
			// the very object being handed back was decoded from (a record that
			// parses but is not a checkpoint keeps its original formatting), and the
			// canonical encoding of a checkpoint we wrote.
			const source = typeof value === "object" && value !== null ? decodedFrom.get(value) : undefined;
			if (stored === value || stored === source || stored === JSON.stringify(value))
				store.removeItemSync(key);
		},
	};
}

