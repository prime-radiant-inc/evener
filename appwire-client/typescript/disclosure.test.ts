import { describe, expect, test, vi } from "vitest";
import { createDisclosureStore, type DisclosureState, isDisclosureOpenIn, scopedDisclosureId } from "./disclosure";

describe("two stores share nothing", () => {
  test("an open, a baseline and a clearScope in one store leave the other on its fallbacks", () => {
    const first = createDisclosureStore();
    const second = createDisclosureStore();
    for (const store of [first, second]) {
      store.beginBaseline("shared", ["row"], true);
      store.setOpen(scopedDisclosureId("shared", "row"), false);
    }

    first.setOpen("a", true);
    first.beginBaseline("live", ["tool"], true);
    first.clearScope("shared");

    expect(first.isOpen("a", false)).toBe(true);
    expect(second.isOpen("a", false)).toBe(false);
    expect(first.defaultFor("live", "tool", false)).toBe(true);
    expect(first.isOpen(scopedDisclosureId("live", "tool"), false)).toBe(true);
    expect(second.defaultFor("live", "tool", false)).toBe(false);
    expect(second.isOpen(scopedDisclosureId("live", "tool"), false)).toBe(false);
    expect(first.defaultFor("shared", "row", false)).toBe(false);
    expect(first.isOpen(scopedDisclosureId("shared", "row"), false)).toBe(false);
    expect(second.defaultFor("shared", "row", false)).toBe(true);
    expect(second.isOpen(scopedDisclosureId("shared", "row"), true)).toBe(false);
  });
});

describe("store shape", () => {
  test("subscribe delivers the new and previous state on every change; the disposer stops delivery", () => {
    const store = createDisclosureStore();
    const before = store.getState();
    const listener = vi.fn();
    const unsubscribe = store.subscribe(listener);
    store.setOpen("a", true);
    expect(listener).toHaveBeenCalledTimes(1);
    const [state, previous] = listener.mock.calls[0] as [DisclosureState, DisclosureState];
    expect(state).toBe(store.getState());
    expect(state.open.get("a")?.open).toBe(true);
    expect(previous).toBe(before);
    unsubscribe();
    store.toggle("a", false);
    expect(listener).toHaveBeenCalledTimes(1);
  });

  test("getInitialState is the empty state the store was created with, and setState back to it resets", () => {
    const store = createDisclosureStore();
    const initial = store.getInitialState();
    store.beginBaseline("live", ["tool"], true);
    store.setOpen(scopedDisclosureId("live", "tool"), false);
    expect(store.getState()).not.toBe(initial);
    store.setState(initial);
    expect(store.getState()).toEqual(initial);
    expect(store.isOpen(scopedDisclosureId("live", "tool"), true)).toBe(true);
    expect(store.getInitialState()).toBe(initial);
  });

  test("isDisclosureOpenIn over a snapshot answers what the store-bound isOpen answers", () => {
    const store = createDisclosureStore();
    store.beginBaseline("live", ["tool", "thought"], false);
    store.setOpen(scopedDisclosureId("live", "tool"), true);
    const snapshot = store.getState();
    for (const id of ["tool", "thought", "unlisted"]) {
      for (const fallback of [true, false]) {
        expect(isDisclosureOpenIn(snapshot, scopedDisclosureId("live", id), fallback)).toBe(
          store.isOpen(scopedDisclosureId("live", id), fallback),
        );
      }
    }
    expect(isDisclosureOpenIn(snapshot, scopedDisclosureId("live", "tool"), false)).toBe(true);
    // A closed baseline defers to the fallback, which callers take from defaultFor.
    expect(
      isDisclosureOpenIn(snapshot, scopedDisclosureId("live", "thought"), store.defaultFor("live", "thought", true)),
    ).toBe(false);
    expect(isDisclosureOpenIn(snapshot, scopedDisclosureId("live", "unlisted"), true)).toBe(true);
  });
});
