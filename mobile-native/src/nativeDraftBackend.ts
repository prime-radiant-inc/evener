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
	/** The exact bytes each key was last READ as. A record this build cannot
	 * decode keeps whatever formatting it was written with, which JSON.stringify
	 * does not reproduce, so its own bytes are the only thing that can name it
	 * in a compare/remove. Cleared by a write, so a newer checkpoint cannot be
	 * removed by an older discard. */
	const lastRead = new Map<string, string>();
	return {
		createId: () => createId(),
		get(key: string): unknown {
			const value = store.getItemSync(key);
			if (value === null) {
				lastRead.delete(key);
				return null;
			}
			lastRead.set(key, value);
			try {
				return JSON.parse(value);
			} catch {
				// Bytes this build cannot parse are still A RECORD, and the shared
				// store's own decoder is what classifies them: handing back the raw
				// string makes it an unreadable RECORD (draftUnreadable, discardable)
				// instead of a throw it can only read as a dead port.
				return value;
			}
		},
		set(key: string, value: unknown) {
			lastRead.delete(key);
			store.setItemSync(key, JSON.stringify(value));
		},
		delete(key: string) {
			lastRead.delete(key);
			store.removeItemSync(key);
		},
		deleteIf(key: string, value: unknown) {
			// Synchronous compare/remove cannot interleave with a newer model's checkpoint.
			const stored = store.getItemSync(key);
			if (stored === null) return;
			// Three ways a value names what is stored: the raw bytes handed back for
			// an unparsable record, the bytes this key was last read as (a record
			// that PARSES but is not a checkpoint keeps its original formatting),
			// and the canonical encoding of a checkpoint we wrote.
			if (stored === value || stored === lastRead.get(key) || stored === JSON.stringify(value)) {
				lastRead.delete(key);
				store.removeItemSync(key);
			}
		},
	};
}

