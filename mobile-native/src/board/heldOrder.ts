// What the Board's list shows while it is held (spec 7.3): the order and
// membership it had when the hold began, each row with its freshest content.
// A row that leaves keeps its last content, and a row that arrives waits,
// until the release applies everything in one step (ruling 22).

/** A list item: `key` is unique in the list and stable across reads. */
export interface HoldableItem {
	key: string;
	/** Set on Live's rows only: whether the row sits in the Needs you band. */
	needsYou?: boolean;
}

export class HeldOrder<T extends { key: string }> {
	#frame: readonly T[] | null = null;
	#latest = new Map<string, T>();

	get held(): boolean {
		return this.#frame !== null;
	}

	/** Freezes `current`, the list as shown, as the order to keep. */
	hold(current: readonly T[]): void {
		if (this.#frame) return;
		this.#frame = [...current];
		this.#latest = new Map(current.map((item) => [item.key, item]));
	}

	/** While held: the held order, with each row's freshest content, departed
	 * rows kept and new rows withheld. Otherwise `next` itself. */
	order(next: readonly T[]): T[] {
		const frame = this.#frame;
		if (!frame) return [...next];
		for (const item of next) if (this.#latest.has(item.key)) this.#latest.set(item.key, item);
		return frame.map((item) => this.#latest.get(item.key) ?? item);
	}

	/** Ends the hold and returns `next`: departures, arrivals and moves all
	 * apply at once. */
	release(next: readonly T[]): T[] {
		this.#frame = null;
		this.#latest = new Map();
		return [...next];
	}
}

/** The Live rows that entered Needs you between two lists the Board applied
 * (spec 7.3's amber wash): none on the first list shown, or while the list
 * before held no Live rows yet. */
export function enteredNeedsYou(before: readonly HoldableItem[] | null, after: readonly HoldableItem[]): Set<string> {
	const entered = new Set<string>();
	if (!before || !before.some((item) => item.needsYou !== undefined)) return entered;
	const was = new Map(before.map((item) => [item.key, item.needsYou === true]));
	for (const item of after) if (item.needsYou === true && was.get(item.key) !== true) entered.add(item.key);
	return entered;
}
