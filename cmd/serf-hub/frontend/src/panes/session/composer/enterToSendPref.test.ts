import { renderHook } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ENTER_TO_SEND_STORAGE_KEY, readEnterToSendPref, useEnterToSendPref } from "./enterToSendPref";

// See draft.test.ts's identical comment (Node 26 shadows jsdom's real
// window.localStorage under vitest).
class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see MemoryStorage's own comment for why this is needed
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.restoreAllMocks();
});

test("defaults to off (false) when the key is absent, matching the legacy default", () => {
  expect(readEnterToSendPref()).toBe(false);
});

test("reads on when the key is exactly '1'", () => {
  localStorage.setItem(ENTER_TO_SEND_STORAGE_KEY, "1");
  expect(readEnterToSendPref()).toBe(true);
});

test("reads off for '0' or any other stored value", () => {
  localStorage.setItem(ENTER_TO_SEND_STORAGE_KEY, "0");
  expect(readEnterToSendPref()).toBe(false);
  localStorage.setItem(ENTER_TO_SEND_STORAGE_KEY, "true");
  expect(readEnterToSendPref()).toBe(false);
});

test("degrades to off when localStorage throws (private mode / disabled storage)", () => {
  vi.spyOn(localStorage, "getItem").mockImplementation(() => {
    throw new Error("storage disabled");
  });
  expect(readEnterToSendPref()).toBe(false);
});

test("useEnterToSendPref reflects the stored value at render time", () => {
  localStorage.setItem(ENTER_TO_SEND_STORAGE_KEY, "1");
  const { result } = renderHook(() => useEnterToSendPref());
  expect(result.current).toBe(true);
});

test("useEnterToSendPref defaults to false with nothing stored", () => {
  const { result } = renderHook(() => useEnterToSendPref());
  expect(result.current).toBe(false);
});
