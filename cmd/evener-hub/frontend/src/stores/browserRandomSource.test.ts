import { afterEach, expect, test, vi } from "vitest";
import { browserRandomSource } from "./browserRandomSource";
import { stubThrowingGetter } from "./throwingGetterTestUtils";

afterEach(() => {
  vi.unstubAllGlobals();
});

test("offers randomUUID and getRandomValues when crypto has both", () => {
  const source = browserRandomSource();
  expect(typeof source.randomUUID).toBe("function");
  expect(typeof source.getRandomValues).toBe("function");
});

test("omits randomUUID when crypto lacks it", () => {
  vi.stubGlobal("crypto", {
    getRandomValues: globalThis.crypto.getRandomValues.bind(globalThis.crypto),
    randomUUID: undefined,
  });
  const source = browserRandomSource();
  expect(source.randomUUID).toBeUndefined();
  expect(typeof source.getRandomValues).toBe("function");
});

test("offers neither method when the crypto property access itself throws", () => {
  const restore = stubThrowingGetter(globalThis, "crypto");
  try {
    expect(() => browserRandomSource()).not.toThrow();
    expect(browserRandomSource()).toEqual({});
  } finally {
    restore();
  }
});
