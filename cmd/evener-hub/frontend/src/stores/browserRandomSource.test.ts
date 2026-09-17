import { afterEach, expect, test, vi } from "vitest";
import { browserRandomSource } from "./browserRandomSource";

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
  const original = Object.getOwnPropertyDescriptor(globalThis, "crypto");
  Object.defineProperty(globalThis, "crypto", {
    configurable: true,
    get(): never {
      throw new Error("crypto access denied");
    },
  });
  try {
    expect(() => browserRandomSource()).not.toThrow();
    expect(browserRandomSource()).toEqual({});
  } finally {
    if (original) Object.defineProperty(globalThis, "crypto", original);
  }
});
