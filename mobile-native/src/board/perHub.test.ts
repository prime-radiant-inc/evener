import { expect, it } from "vitest";
import { perHub } from "./perHub";

it("makes one instance per hub on first use, and a fresh one once the hub is forgotten", () => {
	let made = 0;
	const each = perHub((hubId) => ({ hubId, n: ++made }));
	const first = each.get("hub-a");
	expect(each.get("hub-a")).toBe(first);
	expect(each.get("hub-b")).not.toBe(first);
	expect(each.forget("hub-a")).toBe(first);
	expect(each.forget("hub-a")).toBeUndefined();
	expect(each.get("hub-a")).not.toBe(first);
	expect(made).toBe(3);
});

it("hands what the first get passes to the maker, so each hub's instance keeps its own", () => {
	const each = perHub((hubId: string, source: string) => ({ hubId, source }));
	expect(each.get("hub-a", "first")).toEqual({ hubId: "hub-a", source: "first" });
	expect(each.get("hub-a", "second")).toEqual({ hubId: "hub-a", source: "first" });
	expect(each.get("hub-b", "second")).toEqual({ hubId: "hub-b", source: "second" });
});

it("keeps an instance that is falsy, making it once", () => {
	let made = 0;
	const each = perHub(() => {
		made++;
		return 0;
	});
	expect(each.get("hub-a")).toBe(0);
	expect(each.get("hub-a")).toBe(0);
	expect(made).toBe(1);
});

it("can look up an existing hub without allocating memory for an uncommitted reader", () => {
	let made = 0;
	const each = perHub(() => ++made);
	expect(each.peek("hub-a")).toBeUndefined();
	expect(made).toBe(0);
	expect(each.get("hub-a")).toBe(1);
	expect(each.peek("hub-a")).toBe(1);
	each.forget("hub-a");
	expect(each.peek("hub-a")).toBeUndefined();
	expect(made).toBe(1);
});
