import { describe, expect, it } from "vitest";
import { enteredNeedsYou, HeldOrder } from "./heldOrder";

const item = (key: string, text = key, needsYou?: boolean) => ({ key, text, needsYou });
type Item = ReturnType<typeof item>;

describe("HeldOrder (spec 7.3, ruling 22)", () => {
	it("passes lists through while nothing holds them", () => {
		const order = new HeldOrder<Item>();
		expect(order.order([item("b"), item("a")])).toEqual([item("b"), item("a")]);
		expect(order.held).toBe(false);
	});

	it("keeps the held order and membership, with each row's freshest content", () => {
		const order = new HeldOrder<Item>();
		order.hold([item("a"), item("b"), item("c")]);
		expect(order.order([item("c", "c2"), item("a", "a2"), item("d")])).toEqual([
			item("a", "a2"),
			item("b"),
			item("c", "c2"),
		]);
	});

	it("keeps a departed row's last content until the release", () => {
		const order = new HeldOrder<Item>();
		order.hold([item("a"), item("b")]);
		expect(order.order([item("a"), item("b", "b2")])).toEqual([item("a"), item("b", "b2")]);
		expect(order.order([item("a")])).toEqual([item("a"), item("b", "b2")]);
	});

	it("applies departures, arrivals and moves in one step on release", () => {
		const order = new HeldOrder<Item>();
		order.hold([item("a"), item("b")]);
		order.order([item("d"), item("a")]);
		expect(order.release([item("d"), item("a")])).toEqual([item("d"), item("a")]);
		expect(order.held).toBe(false);
		expect(order.order([item("a"), item("d")])).toEqual([item("a"), item("d")]);
	});

	it("keeps the order of the first hold when asked to hold again", () => {
		const order = new HeldOrder<Item>();
		order.hold([item("a"), item("b")]);
		order.hold([item("b"), item("a")]);
		expect(order.order([item("b"), item("a")])).toEqual([item("a"), item("b")]);
	});
});

describe("rows entering Needs you (spec 7.3's wash)", () => {
	it("names rows that moved into Needs you or arrived there", () => {
		const before = [item("a", "a", false), item("b", "b", true), item("s")];
		const after = [item("a", "a", true), item("b", "b", true), item("c", "c", true), item("s")];
		expect([...enteredNeedsYou(before, after)].sort()).toEqual(["a", "c"]);
	});

	it("names none on the first list, or when the list before had no Live rows", () => {
		const after = [item("a", "a", true)];
		expect(enteredNeedsYou(null, after).size).toBe(0);
		expect(enteredNeedsYou([item("header")], after).size).toBe(0);
	});
});
