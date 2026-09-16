import { describe, expect, test, vi } from "vitest";
import { createDisclosureStore, type DisclosureState, isDisclosureOpenIn, scopedDisclosureId } from "./disclosure";

const scoped = (scope: string, id: string): string => scopedDisclosureId(scope, id);

describe("two stores share nothing", () => {
  test("an open in one store leaves the other on its fallback", () => {
    const first = createDisclosureStore();
    const second = createDisclosureStore();
    first.setOpen("a", true);
    expect(first.isOpen("a", false)).toBe(true);
    expect(second.isOpen("a", false)).toBe(false);
  });

  test("a baseline in one store leaves the other's defaults untouched", () => {
    const first = createDisclosureStore();
    const second = createDisclosureStore();
    first.beginBaseline("live", ["tool"], true);
    expect(first.defaultFor("live", "tool", false)).toBe(true);
    expect(first.isOpen(scoped("live", "tool"), false)).toBe(true);
    expect(second.defaultFor("live", "tool", false)).toBe(false);
    expect(second.isOpen(scoped("live", "tool"), false)).toBe(false);
  });

  test("clearScope on one store leaves the other's scope intact", () => {
    const first = createDisclosureStore();
    const second = createDisclosureStore();
    for (const store of [first, second]) {
      store.beginBaseline("live", ["shared"], true);
      store.setOpen(scoped("live", "shared"), false);
    }
    first.clearScope("live");
    expect(first.isOpen(scoped("live", "shared"), false)).toBe(false);
    expect(first.defaultFor("live", "shared", false)).toBe(false);
    expect(second.isOpen(scoped("live", "shared"), true)).toBe(false);
    expect(second.defaultFor("live", "shared", false)).toBe(true);
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
    store.setOpen(scoped("live", "tool"), false);
    expect(store.getState()).not.toBe(initial);
    store.setState(initial);
    expect(store.getState().open.size).toBe(0);
    expect(store.getState().baselines.size).toBe(0);
    expect(store.getState().revision).toBe(0);
    expect(store.isOpen(scoped("live", "tool"), true)).toBe(true);
    expect(store.getInitialState()).toBe(initial);
  });

  test("isDisclosureOpenIn over a snapshot answers what the store-bound isOpen answers", () => {
    const store = createDisclosureStore();
    store.beginBaseline("live", ["tool", "thought"], false);
    store.setOpen(scoped("live", "tool"), true);
    const snapshot = store.getState();
    for (const id of ["tool", "thought", "unlisted"]) {
      for (const fallback of [true, false]) {
        expect(isDisclosureOpenIn(snapshot, scoped("live", id), fallback)).toBe(
          store.isOpen(scoped("live", id), fallback),
        );
      }
    }
    expect(isDisclosureOpenIn(snapshot, scoped("live", "tool"), false)).toBe(true);
    // A closed baseline defers to the fallback, which callers take from defaultFor.
    expect(isDisclosureOpenIn(snapshot, scoped("live", "thought"), store.defaultFor("live", "thought", true))).toBe(
      false,
    );
    expect(isDisclosureOpenIn(snapshot, scoped("live", "unlisted"), true)).toBe(true);
  });
});
