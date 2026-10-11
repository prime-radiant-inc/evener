// @vitest-environment node

import { describe, expect, test, vi } from "vitest";
import {
  createDisclosureStore,
  type DisclosureState,
  type DisclosureStore,
  isDisclosureOpenIn,
  scopedDisclosureId,
} from "./disclosure";

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

describe("baseline-skipping reads", () => {
  test("ignoreBaseline resolves explicit first, then the fallback, never the scope baseline", () => {
    const store = createDisclosureStore();
    const key = scopedDisclosureId("live", "tool");
    store.beginBaseline("live", ["tool"], true);
    // Without the option the open baseline wins over any fallback.
    expect(store.isOpen(key, false)).toBe(true);
    // With it, the baseline is skipped: an explicit choice still outranks
    // everything, and only then does the fallback answer.
    expect(isDisclosureOpenIn(store.getState(), key, false, { ignoreBaseline: true })).toBe(false);
    expect(isDisclosureOpenIn(store.getState(), key, true, { ignoreBaseline: true })).toBe(true);
    store.setOpen(key, true);
    expect(isDisclosureOpenIn(store.getState(), key, false, { ignoreBaseline: true })).toBe(true);
    store.setOpen(key, false);
    expect(isDisclosureOpenIn(store.getState(), key, true, { ignoreBaseline: true })).toBe(false);
  });
});

/** Reads an id the way every adapter's view hook does: through the store-bound
 * isOpen, with the scope's configuration default as the fallback. */
function readOpen(store: DisclosureStore, scope: string, id: string, fallback: boolean): boolean {
  return store.isOpen(scopedDisclosureId(scope, id), store.defaultFor(scope, id, fallback));
}

// The baseline boundary rules - Full versus Activity, stale false clearing and
// scope collisions - are the store's semantics, not any one adapter's, so they
// are pinned here against a fresh store with no view layer. Each test builds
// its own store: no shared module singleton, no reset seam to remember.
describe("baseline semantics", () => {
  test("unset id reports the fallback", () => {
    const store = createDisclosureStore();
    expect(store.isOpen("a", false)).toBe(false);
    expect(store.isOpen("a", true)).toBe(true);
  });

  test("setOpen overrides the fallback and persists", () => {
    const store = createDisclosureStore();
    store.setOpen("a", true);
    expect(store.isOpen("a", false)).toBe(true);
    store.setOpen("a", false);
    expect(store.isOpen("a", true)).toBe(false);
  });

  test("toggle flips from the fallback then from stored state", () => {
    const store = createDisclosureStore();
    store.toggle("a", false); // fallback false -> true
    expect(store.isOpen("a", false)).toBe(true);
    store.toggle("a", false); // stored true -> false
    expect(store.isOpen("a", false)).toBe(false);
  });

  test("Activity defaults eligible disclosures closed", () => {
    const store = createDisclosureStore();
    const scope = "live:activity";
    store.beginBaseline(scope, ["tool", "thought"], false);

    expect(store.defaultFor(scope, "tool", true)).toBe(false);
    expect(store.defaultFor(scope, "thought", true)).toBe(false);
    expect(readOpen(store, scope, "tool", false)).toBe(false);
  });

  test("entering Full clears closed overrides once and opens current eligible ids", () => {
    const store = createDisclosureStore();
    const scope = "live:full";
    store.beginBaseline(scope, ["tool", "thought"], false);
    store.setOpen(scopedDisclosureId(scope, "tool"), false);

    store.beginBaseline(scope, ["tool", "thought"], true);

    expect(readOpen(store, scope, "tool", false)).toBe(true);
    expect(readOpen(store, scope, "thought", false)).toBe(true);
  });

  test("an explicit open survives Full and a later Activity transition", () => {
    const store = createDisclosureStore();
    const scope = "live:explicit-open";
    store.beginBaseline(scope, ["tool"], false);
    store.setOpen(scopedDisclosureId(scope, "tool"), true);

    store.beginBaseline(scope, ["tool"], true);
    store.beginBaseline(scope, ["tool"], false);

    expect(readOpen(store, scope, "tool", false)).toBe(true);
  });

  test("a later manual collapse wins and a new eligible Full id opens by default", () => {
    const store = createDisclosureStore();
    const scope = "live:full-manual";
    store.beginBaseline(scope, ["tool"], false);
    store.beginBaseline(scope, ["tool"], true);
    store.toggle(scopedDisclosureId(scope, "tool"), store.defaultFor(scope, "tool", false));

    store.beginBaseline(scope, ["tool", "new-tool"], true);

    expect(readOpen(store, scope, "tool", false)).toBe(false);
    expect(readOpen(store, scope, "new-tool", false)).toBe(true);
  });

  test("a stale false choice opens when its id becomes newly eligible during Full", () => {
    const store = createDisclosureStore();
    const scope = "live:stale-new-id";
    store.setOpen(scopedDisclosureId(scope, "new-tool"), false);
    store.beginBaseline(scope, ["tool"], true);
    store.beginBaseline(scope, ["tool", "new-tool"], true);

    expect(readOpen(store, scope, "new-tool", false)).toBe(true);
  });

  test("a manual false choice made during the active Full baseline stays closed", () => {
    const store = createDisclosureStore();
    const scope = "live:current-close";
    store.beginBaseline(scope, ["tool"], true);
    store.setOpen(scopedDisclosureId(scope, "tool"), false);
    store.beginBaseline(scope, ["tool", "new-tool"], true);

    expect(readOpen(store, scope, "tool", false)).toBe(false);
    expect(readOpen(store, scope, "new-tool", false)).toBe(true);
  });

  test("returning to Full starts a new baseline", () => {
    const store = createDisclosureStore();
    const scope = "live:full-again";
    store.beginBaseline(scope, ["tool"], false);
    store.beginBaseline(scope, ["tool"], true);
    store.toggle(scopedDisclosureId(scope, "tool"), store.defaultFor(scope, "tool", false));
    store.beginBaseline(scope, ["tool"], false);
    store.beginBaseline(scope, ["tool"], true);

    expect(readOpen(store, scope, "tool", false)).toBe(true);
  });

  test("preview and live disclosure scopes never collide", () => {
    const store = createDisclosureStore();
    store.beginBaseline("live:session", ["shared"], true);
    store.beginBaseline("preview:test", ["shared"], false);
    store.setOpen(scopedDisclosureId("preview:test", "shared"), true);

    expect(readOpen(store, "live:session", "shared", false)).toBe(true);
    expect(readOpen(store, "preview:test", "shared", false)).toBe(true);
  });

  test("clearing one disclosure scope leaves other scopes intact", () => {
    const store = createDisclosureStore();
    store.beginBaseline("live:clear", ["shared"], true);
    store.beginBaseline("preview:keep", ["shared"], true);
    store.setOpen(scopedDisclosureId("live:clear", "shared"), false);

    store.clearScope("live:clear");

    expect(store.isOpen(scopedDisclosureId("live:clear", "shared"), false)).toBe(false);
    expect(readOpen(store, "preview:keep", "shared", false)).toBe(true);
  });
});
