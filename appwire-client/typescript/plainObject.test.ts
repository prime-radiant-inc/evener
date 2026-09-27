// @vitest-environment node
// Contract for the shared plain-object guard: a non-null, non-array object
// whose prototype is Object.prototype or null. The prototype check is the
// strict behaviour the recursive activity parser relies on and is a no-op for
// JSON-derived values; class instances and dates are rejected.
import { describe, expect, test } from "vitest";
import { isPlainObject, sameJsonValue } from "./plainObject";

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

describe("sameJsonValue", () => {
  // Every read of stored or fetched data hands back new objects, so content is
  // what tells a real change from a fresh copy.
  test("a fresh copy of the same content is the same value", () => {
    const read = () => ({
      text: "hello",
      count: 2,
      flags: [true, false],
      nested: { items: [{ id: "a" }], none: null },
    });
    expect(sameJsonValue(read(), read())).toBe(true);
  });

  test.each([
    ["a nested value", { items: [{ id: "a" }] }, { items: [{ id: "b" }] }],
    ["an array's length", [1], [1, 2]],
    ["an array against an object with the same keys", [1], { 0: 1 }],
    ["which keys are present, at the same count", { a: 1, b: undefined }, { a: 1, c: undefined }],
    ["a key missing on one side", { a: 1 }, { a: 1, b: 2 }],
    ["null against an empty object", null, {}],
    ["a number against its string", 1, "1"],
  ])("a difference in %s is a different value", (_difference, left, right) => {
    expect(sameJsonValue(left, right)).toBe(false);
    expect(sameJsonValue(right, left)).toBe(false);
  });
});
