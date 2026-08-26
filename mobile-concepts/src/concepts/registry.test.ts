import { describe, expect, it } from "vitest";
import { conceptRegistry } from "./registry";

describe("conceptRegistry", () => {
  it("is a total registry for the exact concept IDs", () => {
    expect(Object.keys(conceptRegistry).sort()).toEqual([
      "constellation",
      "field-notes",
      "stillwater",
    ]);
    expect(conceptRegistry.stillwater.id).toBe("stillwater");
    expect(conceptRegistry.constellation.id).toBe("constellation");
    expect(conceptRegistry["field-notes"].id).toBe("field-notes");
  });
});
