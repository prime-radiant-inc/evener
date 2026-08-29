import { describe, expect, it } from "vitest";
import { liveConceptRegistry } from "./registry";

describe("liveConceptRegistry", () => {
  it("contains exactly the three production concepts in display order", () => {
    expect(Object.keys(liveConceptRegistry)).toEqual([
      "stillwater",
      "constellation",
      "field-notes",
    ]);
  });

  it("keeps each module keyed by its own concept ID", () => {
    expect(liveConceptRegistry.stillwater.id).toBe("stillwater");
    expect(liveConceptRegistry.constellation.id).toBe("constellation");
    expect(liveConceptRegistry["field-notes"].id).toBe("field-notes");
  });

  it("requires Stillwater's shared conversation skin", () => {
    expect(liveConceptRegistry.stillwater.conversationSkin.id).toBe(
      "stillwater",
    );
  });

  it("requires Constellation's shared conversation skin", () => {
    expect(liveConceptRegistry.constellation.conversationSkin.id).toBe(
      "constellation",
    );
  });

  it("requires Field Notes' shared conversation skin", () => {
    expect(liveConceptRegistry["field-notes"].conversationSkin.id).toBe(
      "field-notes",
    );
  });
});
