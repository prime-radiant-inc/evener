// The Board's list as shown (spec 7.3): held while the settle machine
// (listSettle.ts) isn't idle, or while an interaction holds it (a row's open
// swipe, the row menu, select mode; ruling 22). Every change applies in one
// step when it settles, and the rows that entered Needs you then wash amber.
import { enteredNeedsYou, HeldOrder, type HoldableItem } from "./heldOrder";
import { nextSettleState, SETTLE_DEADLINE_MS, type SettleEvent, type SettleState } from "./listSettle";

/** How long a row that entered Needs you stays washed (spec 7.3: 1.2s). */
export const WASH_MS = 1200;

export interface SettledSnapshot<T> {
	/** What the list shows. */
	display: readonly T[];
	held: boolean;
	/** Each row washing now, for WASH_MS from when it entered Needs you, by
	 * its wash's number: a new number for every entry, so a row can wash
	 * again, and the same one until that wash ends, so a row entering later
	 * never restarts another's fade. */
	washed: ReadonlyMap<string, number>;
}

export interface SettleTimers {
	set(run: () => void, ms: number): unknown;
	clear(handle: unknown): void;
}
const systemTimers: SettleTimers = {
	set: (run, ms) => setTimeout(run, ms),
	clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

export class SettledList<T extends HoldableItem> {
	#state: SettleState = "idle";
	#deadline: unknown = null;
	readonly #interactions = new Set<string>();
	#latest: readonly T[] | null = null;
	#applied: readonly T[] | null = null;
	readonly #order = new HeldOrder<T>();
	readonly #washTimers = new Map<string, unknown>();
	#washCount = 0;
	#snapshot: SettledSnapshot<T> = { display: [], held: false, washed: new Map() };
	readonly #listeners = new Set<() => void>();
	#disposed = false;
	readonly #timers: SettleTimers;

	constructor(timers: SettleTimers = systemTimers) {
		this.#timers = timers;
	}

	getSnapshot = (): SettledSnapshot<T> => this.#snapshot;

	subscribe = (listener: () => void): (() => void) => {
		this.#listeners.add(listener);
		return () => this.#listeners.delete(listener);
	};

	get state(): SettleState {
		return this.#state;
	}

	/** The list the Board would show now, or null before its first load. */
	setItems(items: readonly T[] | null): void {
		this.#latest = items;
		if (items === null) return;
		if (!this.#held()) {
			this.#apply(items);
			return;
		}
		// A hold that began before the first list loaded had nothing on screen
		// to keep still: that list shows at once, and is what it holds from here.
		this.#order.hold(items);
		this.#publish({ display: this.#order.order(items) });
	}

	/** An event from the list (listScrollHandlers), "appScrollStart" before an
	 * animated scroll the Board starts, or "reset" when it leaves the screen. */
	send(event: SettleEvent): void {
		const wasHeld = this.#held();
		if (event === "reset") this.#interactions.clear();
		const next = nextSettleState(this.#state, event);
		if (next !== this.#state) {
			this.#timers.clear(this.#deadline);
			this.#deadline = null;
			this.#state = next;
			const ms = SETTLE_DEADLINE_MS[next];
			if (ms !== undefined)
				this.#deadline = this.#timers.set(() => {
					this.#deadline = null;
					this.send("deadline");
				}, ms);
		}
		this.#settle(wasHeld);
	}

	/** An interaction that holds the list opening (true) or closing (false). */
	setInteraction(id: string, active: boolean): void {
		const wasHeld = this.#held();
		if (active) this.#interactions.add(id);
		else this.#interactions.delete(id);
		this.#settle(wasHeld);
	}

	dispose(): void {
		this.#disposed = true;
		this.#timers.clear(this.#deadline);
		for (const timer of this.#washTimers.values()) this.#timers.clear(timer);
		this.#listeners.clear();
	}

	#held(): boolean {
		return this.#state !== "idle" || this.#interactions.size > 0;
	}

	#settle(wasHeld: boolean): void {
		const held = this.#held();
		if (held === wasHeld) return;
		if (held) {
			if (this.#latest !== null) this.#order.hold(this.#snapshot.display);
			this.#publish({ held: true });
			return;
		}
		if (this.#latest === null) {
			this.#order.release([]);
			this.#publish({ held: false });
			return;
		}
		this.#apply(this.#order.release(this.#latest));
	}

	#apply(items: readonly T[]): void {
		const entered = enteredNeedsYou(this.#applied, items);
		this.#applied = items;
		if (entered.size === 0) {
			this.#publish({ display: items, held: false });
			return;
		}
		const washed = new Map(this.#snapshot.washed);
		for (const key of entered) {
			washed.set(key, ++this.#washCount);
			this.#timers.clear(this.#washTimers.get(key));
			this.#washTimers.set(
				key,
				this.#timers.set(() => {
					this.#washTimers.delete(key);
					const remaining = new Map(this.#snapshot.washed);
					remaining.delete(key);
					this.#publish({ washed: remaining });
				}, WASH_MS),
			);
		}
		this.#publish({ display: items, held: false, washed });
	}

	#publish(change: Partial<SettledSnapshot<T>>): void {
		if (this.#disposed) return;
		this.#snapshot = { ...this.#snapshot, ...change };
		for (const listener of [...this.#listeners]) listener();
	}
}

/** The list's touch and scroll events as settle events. A touch ends when its
 * last finger lifts, or when the scroll view takes it over (onTouchCancel). */
export function listScrollHandlers(send: (event: SettleEvent) => void) {
	const lastFinger = (event: { nativeEvent: { touches: readonly unknown[] } }) => {
		if (event.nativeEvent.touches.length === 0) send("touchEnd");
	};
	return {
		onTouchStart: () => send("touchStart"),
		onTouchEnd: lastFinger,
		onTouchCancel: lastFinger,
		onScrollBeginDrag: () => send("scrollBeginDrag"),
		onScrollEndDrag: () => send("scrollEndDrag"),
		onMomentumScrollBegin: () => send("momentumBegin"),
		onMomentumScrollEnd: () => send("momentumEnd"),
	};
}
