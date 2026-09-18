// @vitest-environment node
// Contract for the shared plain-object guard: a non-null, non-array object
// whose prototype is Object.prototype or null. The prototype check is the
// strict behaviour the recursive activity parser relies on and is a no-op for
// JSON-derived values; class instances and dates are rejected.
import { describe, expect, test } from "vitest";
import { isPlainObject } from "./plainObject";

class NotPlain {
  field = 1;
}

describe("isPlainObject", () => {
  test("accepts plain objects, including ones with a null prototype", () => {
    expect(isPlainObject({})).toBe(true);
    expect(isPlainObject({ a: 1, nested: { b: 2 } })).toBe(true);
    expect(isPlainObject(Object.create(null))).toBe(true);
  });

  test("rejects null, arrays and primitives", () => {
    expect(isPlainObject(null)).toBe(false);
    expect(isPlainObject(undefined)).toBe(false);
    expect(isPlainObject([1, 2])).toBe(false);
    expect(isPlainObject("text")).toBe(false);
    expect(isPlainObject(1)).toBe(false);
    expect(isPlainObject(true)).toBe(false);
    expect(isPlainObject(() => undefined)).toBe(false);
  });

  test("rejects class instances, whose prototype is not Object.prototype", () => {
    expect(isPlainObject(new NotPlain())).toBe(false);
    expect(isPlainObject(new Date())).toBe(false);
  });
});
