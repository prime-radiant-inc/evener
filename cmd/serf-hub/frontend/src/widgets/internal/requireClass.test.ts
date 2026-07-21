import { expect, test } from "vitest";
import { requireClass } from "./requireClass";

test("returns the value when it is defined", () => {
  expect(requireClass("_primary_ef88a3", "button.module.css", "primary")).toBe("_primary_ef88a3");
});

test("throws a message naming both the module and the missing class when undefined", () => {
  expect(() => requireClass(undefined, "button.module.css", "primary")).toThrowError(
    'button.module.css is missing the "primary" class',
  );
});
