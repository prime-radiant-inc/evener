import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { listScrollHandlers, SettledList, WASH_MS } from "./settledList";

type Item = { key: string; text: string; needsYou?: boolean };
const item = (key: string, text = key, needsYou?: boolean): Item => ({ key, text, needsYou });
const keys = (list: SettledList<Item>) => list.getSnapshot().display.map((entry) => entry.key);
const texts = (list: SettledList<Item>) => list.getSnapshot().display.map((entry) => entry.text);
function loaded(...entries: Item[]) {
	const list = new SettledList<Item>();
	list.setItems(entries);
	return list;
}
beforeEach(() => {
	vi.useFakeTimers();
});
afterEach(() => {
	vi.useRealTimers();
});

it("applies a change at once while nothing holds the list", () => {
	const list = loaded(item("a"), item("b"));
	list.setItems([item("b"), item("a"), item("c")]);
	expect(keys(list)).toEqual(["b", "a", "c"]);
	expect(list.getSnapshot().held).toBe(false);
});

it("under a finger keeps order and membership with fresh content, and applies it all 100ms after the finger lifts", () => {
	const list = loaded(item("a"), item("b"), item("c"));
	list.send("touchStart");
	list.setItems([item("c", "c2"), item("a"), item("d")]);
	expect(keys(list)).toEqual(["a", "b", "c"]);
	expect(texts(list)).toEqual(["a", "b", "c2"]);
	list.send("touchEnd");
	vi.advanceTimersByTime(99);
	expect(keys(list)).toEqual(["a", "b", "c"]);
	vi.advanceTimersByTime(1);
	expect(keys(list)).toEqual(["c", "a", "d"]);
	expect(list.getSnapshot().held).toBe(false);
});

it("holds through a drag, the moment before momentum and the glide, and applies when the glide ends", () => {
	const list = loaded(item("a"), item("b"));
	list.send("touchStart");
	list.send("scrollBeginDrag");
	list.setItems([item("b"), item("a")]);
	list.send("scrollEndDrag");
	vi.advanceTimersByTime(50);
	list.send("momentumBegin");
	vi.advanceTimersByTime(3000);
	expect(keys(list)).toEqual(["a", "b"]);
	list.send("momentumEnd");
	expect(keys(list)).toEqual(["b", "a"]);
});

it("never fires the lifted deadline once momentum has begun", () => {
	const list = loaded(item("a"), item("b"));
	list.send("scrollBeginDrag");
	list.send("scrollEndDrag");
	list.send("momentumBegin");
	list.setItems([item("b"), item("a")]);
	vi.advanceTimersByTime(150);
	expect(list.state).toBe("momentum");
	expect(keys(list)).toEqual(["a", "b"]);
});

it("applies 100ms after a drag that ends without momentum", () => {
	const list = loaded(item("a"), item("b"));
	list.send("scrollBeginDrag");
	list.setItems([item("b"), item("a")]);
	list.send("scrollEndDrag");
	vi.advanceTimersByTime(99);
	expect(keys(list)).toEqual(["a", "b"]);
	vi.advanceTimersByTime(1);
	expect(keys(list)).toEqual(["b", "a"]);
});

it("lets no change through when a touch is cancelled just before its drag begins", () => {
	const list = loaded(item("a"), item("b"));
	const handlers = listScrollHandlers((event) => list.send(event));
	handlers.onTouchStart();
	handlers.onTouchCancel({ nativeEvent: { touches: [] } });
	list.setItems([item("b"), item("a")]);
	vi.advanceTimersByTime(20);
	handlers.onScrollBeginDrag();
	vi.advanceTimersByTime(500);
	expect(keys(list)).toEqual(["a", "b"]);
	handlers.onScrollEndDrag();
	vi.advanceTimersByTime(100);
	expect(keys(list)).toEqual(["b", "a"]);
});

it("keeps holding while a finger is still down", () => {
	const list = loaded(item("a"), item("b"));
	const handlers = listScrollHandlers((event) => list.send(event));
	handlers.onTouchStart();
	handlers.onTouchStart();
	handlers.onTouchEnd({ nativeEvent: { touches: [{}] } });
	list.setItems([item("b"), item("a")]);
	vi.advanceTimersByTime(500);
	expect(keys(list)).toEqual(["a", "b"]);
	handlers.onTouchEnd({ nativeEvent: { touches: [] } });
	vi.advanceTimersByTime(100);
	expect(keys(list)).toEqual(["b", "a"]);
});

it("lets go of a glide whose end never arrives after six seconds", () => {
	const list = loaded(item("a"), item("b"));
	list.send("scrollBeginDrag");
	list.send("scrollEndDrag");
	list.send("momentumBegin");
	list.setItems([item("b"), item("a")]);
	vi.advanceTimersByTime(5999);
	expect(keys(list)).toEqual(["a", "b"]);
	vi.advanceTimersByTime(1);
	expect(keys(list)).toEqual(["b", "a"]);
});

it("holds an app scroll until it ends, or for a second at most", () => {
	const list = loaded(item("a"), item("b"));
	list.send("appScrollStart");
	list.setItems([item("b"), item("a")]);
	list.send("momentumEnd");
	expect(keys(list)).toEqual(["b", "a"]);
	list.send("appScrollStart");
	list.setItems([item("a"), item("b")]);
	vi.advanceTimersByTime(999);
	expect(keys(list)).toEqual(["b", "a"]);
	vi.advanceTimersByTime(1);
	expect(keys(list)).toEqual(["a", "b"]);
});

it("holds while a row's actions are open, past the finger lifting, until they close", () => {
	const list = loaded(item("a"), item("b"));
	list.send("touchStart");
	list.setInteraction("swipe:a", true);
	list.send("touchEnd");
	vi.advanceTimersByTime(500);
	list.setItems([item("b"), item("a")]);
	expect(keys(list)).toEqual(["a", "b"]);
	list.setInteraction("swipe:a", false);
	expect(keys(list)).toEqual(["b", "a"]);
});

it("reset releases at once and forgets open interactions", () => {
	const list = loaded(item("a"), item("b"));
	list.send("touchStart");
	list.setInteraction("menu", true);
	list.setItems([item("b"), item("a")]);
	list.send("reset");
	expect(keys(list)).toEqual(["b", "a"]);
	expect(list.getSnapshot().held).toBe(false);
	list.setItems([item("a"), item("b")]);
	expect(keys(list)).toEqual(["a", "b"]);
});

it("washes rows that entered Needs you, never on the first list, and clears the wash after 1.2 seconds", () => {
	const list = loaded(item("a", "a", false), item("b", "b", true));
	expect(list.getSnapshot().washed.size).toBe(0);
	list.setItems([item("a", "a", true), item("b", "b", true), item("c", "c", true)]);
	expect([...list.getSnapshot().washed].sort()).toEqual(["a", "c"]);
	const token = list.getSnapshot().washToken;
	vi.advanceTimersByTime(WASH_MS);
	expect(list.getSnapshot().washed.size).toBe(0);
	expect(list.getSnapshot().washToken).toBe(token);
});

it("washes a row that entered Needs you while held only when the list settles, in its new place", () => {
	const list = loaded(item("w", "w", false), item("n", "n", true));
	list.send("touchStart");
	list.setItems([item("n", "n", true), item("w", "w2", true)]);
	expect(list.getSnapshot().washed.size).toBe(0);
	list.send("touchEnd");
	vi.advanceTimersByTime(100);
	expect(keys(list)).toEqual(["n", "w"]);
	expect([...list.getSnapshot().washed]).toEqual(["w"]);
});
