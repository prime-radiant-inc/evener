// @vitest-environment node

import { expect, test, vi } from "vitest";
import { createFrameworkFreeStore } from "./frameworkFreeStore";

interface Counter {
  n: number;
  label: string;
  bump(): void;
  read(): number;
}

const createCounter = () =>
  createFrameworkFreeStore<Counter>((set, get) => ({
    n: 0,
    label: "start",
    bump: () => set((s) => ({ n: s.n + 1 })),
    read: () => get().n,
  }));

test("state methods close over the store's own set and get", () => {
  const store = createCounter();
  store.getState().bump();
  store.getState().bump();
  expect(store.getState().n).toBe(2);
  expect(store.getState().read()).toBe(2);
});

test("setState shallow-merges a partial or an updater's result and notifies with new and previous state", () => {
  const store = createCounter();
  const before = store.getState();
  const listener = vi.fn();
  store.subscribe(listener);
  store.setState({ label: "renamed" });
  expect(store.getState().label).toBe("renamed");
  expect(store.getState().n).toBe(0);
  expect(listener).toHaveBeenLastCalledWith(store.getState(), before);
  store.setState((s) => ({ n: s.n + 5 }));
  expect(store.getState()).toMatchObject({ n: 5, label: "renamed" });
  expect(listener).toHaveBeenCalledTimes(2);
});

test("every setState notifies, even one that changes nothing; the disposer stops delivery", () => {
  const store = createCounter();
  const listener = vi.fn();
  const unsubscribe = store.subscribe(listener);
  store.setState({});
  expect(listener).toHaveBeenCalledTimes(1);
  unsubscribe();
  unsubscribe();
  store.getState().bump();
  expect(listener).toHaveBeenCalledTimes(1);
});

test("getInitialState is the creation state and stays so after changes", () => {
  const store = createCounter();
  const initial = store.getState();
  store.getState().bump();
  expect(store.getInitialState()).toBe(initial);
  expect(store.getState()).not.toBe(initial);
  expect(initial.n).toBe(0);
});
