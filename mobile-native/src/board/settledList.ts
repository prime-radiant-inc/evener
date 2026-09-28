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
	/** The rows that entered Needs you at the last change applied, for WASH_MS. */
	washed: ReadonlySet<string>;
	/** Moves on whenever `washed` gains rows, so a row can wash again. */
	washToken: number;
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
	#washTimer: unknown = null;
	#snapshot: SettledSnapshot<T> = { display: [], held: false, washed: new Set(), washToken: 0 };
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
		if (this.#held()) this.#publish({ display: this.#order.order(items) });
		else this.#apply(items);
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
		this.#timers.clear(this.#washTimer);
		this.#listeners.clear();
	}

	#held(): boolean {
		return this.#state !== "idle" || this.#interactions.size > 0;
	}

	#settle(wasHeld: boolean): void {
		const held = this.#held();
		if (held === wasHeld) return;
		if (held) {
			this.#order.hold(this.#snapshot.display);
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
		this.#timers.clear(this.#washTimer);
		this.#washTimer = this.#timers.set(() => {
			this.#washTimer = null;
			this.#publish({ washed: new Set() });
		}, WASH_MS);
		this.#publish({ display: items, held: false, washed: entered, washToken: this.#snapshot.washToken + 1 });
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
