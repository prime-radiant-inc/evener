import { describe, expect, it } from "vitest";
import { nextSettleState, SETTLE_DEADLINE_MS, type SettleEvent, type SettleState } from "./listSettle";

// Written from the plan's table, independently of the implementation.
const TABLE: [SettleState, SettleEvent, SettleState][] = [
	["idle", "touchStart", "touching"],
	["idle", "touchEnd", "idle"],
	["idle", "scrollBeginDrag", "dragging"],
	["idle", "scrollEndDrag", "idle"],
	["idle", "momentumBegin", "momentum"],
	["idle", "momentumEnd", "idle"],
	["idle", "appScrollStart", "appScrolling"],
	["idle", "deadline", "idle"],
	["idle", "reset", "idle"],
	["touching", "touchStart", "touching"],
	["touching", "touchEnd", "lifted"],
	["touching", "scrollBeginDrag", "dragging"],
	["touching", "scrollEndDrag", "touching"],
	["touching", "momentumBegin", "touching"],
	["touching", "momentumEnd", "touching"],
	["touching", "appScrollStart", "touching"],
	["touching", "deadline", "touching"],
	["touching", "reset", "idle"],
	["dragging", "touchStart", "dragging"],
	["dragging", "touchEnd", "dragging"],
	["dragging", "scrollBeginDrag", "dragging"],
	["dragging", "scrollEndDrag", "lifted"],
	["dragging", "momentumBegin", "momentum"],
	["dragging", "momentumEnd", "dragging"],
	["dragging", "appScrollStart", "dragging"],
	["dragging", "deadline", "dragging"],
	["dragging", "reset", "idle"],
	["lifted", "touchStart", "touching"],
	["lifted", "touchEnd", "lifted"],
	["lifted", "scrollBeginDrag", "dragging"],
	["lifted", "scrollEndDrag", "lifted"],
	["lifted", "momentumBegin", "momentum"],
	["lifted", "momentumEnd", "idle"],
	["lifted", "appScrollStart", "appScrolling"],
	["lifted", "deadline", "idle"],
	["lifted", "reset", "idle"],
	["momentum", "touchStart", "touching"],
	["momentum", "touchEnd", "momentum"],
	["momentum", "scrollBeginDrag", "dragging"],
	["momentum", "scrollEndDrag", "momentum"],
	["momentum", "momentumBegin", "momentum"],
	["momentum", "momentumEnd", "idle"],
	["momentum", "appScrollStart", "appScrolling"],
	["momentum", "deadline", "idle"],
	["momentum", "reset", "idle"],
	["appScrolling", "touchStart", "touching"],
	["appScrolling", "touchEnd", "appScrolling"],
	["appScrolling", "scrollBeginDrag", "dragging"],
	["appScrolling", "scrollEndDrag", "appScrolling"],
	["appScrolling", "momentumBegin", "appScrolling"],
	["appScrolling", "momentumEnd", "idle"],
	["appScrolling", "appScrollStart", "appScrolling"],
	["appScrolling", "deadline", "idle"],
	["appScrolling", "reset", "idle"],
];

describe("the settle machine (spec 7.3)", () => {
	it.each(TABLE)("%s + %s → %s", (state, event, next) => {
		expect(nextSettleState(state, event)).toBe(next);
	});

	it("the table covers every state and event once", () => {
		const pairs = new Set(TABLE.map(([state, event]) => `${state}:${event}`));
		expect(pairs.size).toBe(6 * 9);
		expect(TABLE).toHaveLength(6 * 9);
	});

	it("gives only the waiting states a deadline", () => {
		expect(SETTLE_DEADLINE_MS).toEqual({ lifted: 100, momentum: 6000, appScrolling: 1000 });
	});

	it("holds a fling from the first touch to the glide's end, even when the touch is cancelled before the drag begins", () => {
		const events: SettleEvent[] = ["touchStart", "touchEnd", "scrollBeginDrag", "scrollEndDrag", "momentumBegin", "momentumEnd"];
		const trail: SettleState[] = [];
		let state: SettleState = "idle";
		for (const event of events) {
			state = nextSettleState(state, event);
			trail.push(state);
		}
		expect(trail).toEqual(["touching", "lifted", "dragging", "lifted", "momentum", "idle"]);
	});
});
