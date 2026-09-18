// @vitest-environment node
// The package's one plain-object guard, shared by the four sites that used to
// carry private copies (#1425). It is the strict variant the recursive activity
// parser used: a non-null, non-array object whose prototype is Object.prototype
// or null. JSON-derived values (the wire shapes the other three sites read) are
// always plain, so the prototype check is a no-op there while keeping the
// parser's original guarantee.
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
