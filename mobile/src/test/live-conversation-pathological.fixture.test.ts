import { describe, expect, it } from "vitest";
import {
  makePathological39ItemFixture,
  makeVariableHeight500ItemFixture,
} from "./live-conversation-pathological.fixture";

describe("pathological conversation fixtures", () => {
  it("produces exactly 39 items in the pathological fixture", () => {
    const pathological = makePathological39ItemFixture();
    expect(pathological.conversation.items).toHaveLength(39);
  });

  it("produces a 44,700-byte raw system prelude", () => {
    const pathological = makePathological39ItemFixture();
    expect(
      new TextEncoder().encode(pathological.rawSystemPrelude),
    ).toHaveLength(44_700);
  });

  it("produces exactly 500 items in the variable-height fixture", () => {
    expect(makeVariableHeight500ItemFixture().conversation.items).toHaveLength(
      500,
    );
  });

  it("exposes a non-empty raw system sentinel distinct from the prelude", () => {
    const pathological = makePathological39ItemFixture();
    expect(pathological.rawSystemSentinel.length).toBeGreaterThan(0);
    expect(pathological.rawSystemSentinel).not.toEqual(
      pathological.rawSystemPrelude,
    );
  });

  it("never carries the raw prelude text in any timeline item", () => {
    const pathological = makePathological39ItemFixture();
    for (const item of pathological.conversation.items) {
      const text =
        "markdown" in item ? item.markdown : "text" in item ? item.text : "";
      expect(text).not.toContain(pathological.rawSystemPrelude);
    }
  });

  it("produces deterministic fixtures across repeated calls", () => {
    const a = makePathological39ItemFixture();
    const b = makePathological39ItemFixture();
    expect(a.conversation.items).toEqual(b.conversation.items);
    expect(a.rawSystemPrelude).toEqual(b.rawSystemPrelude);
  });
});
