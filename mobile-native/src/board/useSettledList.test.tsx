import { act } from "react-test-renderer";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { renderHook, unmountMountedTrees } from "../renderNative.testkit";
import { useSettledList } from "./useSettledList";

type Item = { key: string; text: string; needsYou?: boolean };
const item = (key: string, needsYou?: boolean): Item => ({ key, text: key, needsYou });

beforeEach(() => {
	vi.useFakeTimers();
});
afterEach(() => {
	unmountMountedTrees();
	vi.useRealTimers();
});

it("clears the settle deadline it started when the Board unmounts mid-settle", () => {
	const items: readonly Item[] = [item("a")];
	const hook = renderHook(() => useSettledList<Item>(items));
	act(() => {
		hook.result.current.list.send("touchStart");
		hook.result.current.list.send("touchEnd");
	});
	expect(vi.getTimerCount()).toBeGreaterThan(0);
	hook.unmount();
	expect(vi.getTimerCount()).toBe(0);
});

it("clears a wash it started when the Board unmounts mid-wash", () => {
	let items: readonly Item[] = [item("a", false)];
	const hook = renderHook(() => useSettledList<Item>(items));
	items = [item("a", true)];
	hook.rerender();
	expect(vi.getTimerCount()).toBeGreaterThan(0);
	hook.unmount();
	expect(vi.getTimerCount()).toBe(0);
});
