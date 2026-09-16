import { describe, expect, it } from "vitest";
import {
  documentPositionToTextOffset,
  parseSkillDocument,
  serializeSkillDocument,
  textOffsetToDocumentPosition,
} from "./skillDocument";

describe("skill document", () => {
  it("round trips text, newlines, and repeated atoms with document-ordered deduplicated metadata", () => {
    const value = {
      text: "Run /skill-1\nand then /skill-2 and /skill-1",
      skillNames: ["skill-2", "skill-1", "skill-1", "invisible"],
    };
    const doc = parseSkillDocument(value);
    expect(serializeSkillDocument(doc)).toEqual({ text: value.text, skillNames: ["skill-1", "skill-2"] });
    expect(doc.content.content.filter((node) => node.type.name === "skill").map((node) => node.attrs.name)).toEqual([
      "skill-1",
      "skill-2",
      "skill-1",
    ]);
  });

  it("activates complete canonical tokens only, not prefixes, aliases, paths, or unknown slash text", () => {
    const text = "/reviewer /review-extra x/review /review:other /team:review /unknown (/review), /review";
    const doc = parseSkillDocument({ text, skillNames: ["review", "team:review", "missing"] });
    expect(doc.content.content.filter((node) => node.type.name === "skill").map((node) => node.attrs.name)).toEqual([
      "team:review",
      "review",
      "review",
    ]);
    expect(serializeSkillDocument(doc)).toEqual({ text, skillNames: ["team:review", "review"] });
    expect(serializeSkillDocument(parseSkillDocument({ text: "/review", skillNames: [] }))).toEqual({
      text: "/review",
      skillNames: [],
    });
  });

  it("keeps empty documents and whitespace without invisible metadata", () => {
    for (const text of ["", " \n\n ", "/unknown\n"]) {
      expect(serializeSkillDocument(parseSkillDocument({ text, skillNames: ["review"] }))).toEqual({
        text,
        skillNames: [],
      });
    }
  });

  it("maps UTF-16 offsets around atoms and literal newlines, snapping interiors by bias", () => {
    const doc = parseSkillDocument({ text: "😀\n/review\nz", skillNames: ["review"] });
    // A flat inline document has no paragraph opening/closing positions.
    for (const [position, offset] of [
      [0, 0],
      [1, 1],
      [2, 2],
      [3, 3],
      [4, 10],
      [5, 11],
      [6, 12],
    ] as const) {
      expect(documentPositionToTextOffset(doc, position)).toBe(offset);
      expect(textOffsetToDocumentPosition(doc, offset)).toBe(position);
    }
    expect(textOffsetToDocumentPosition(doc, 6, -1)).toBe(3);
    expect(textOffsetToDocumentPosition(doc, 6, 1)).toBe(4);
    expect(textOffsetToDocumentPosition(doc, -1)).toBe(0);
    expect(textOffsetToDocumentPosition(doc, 100)).toBe(6);
  });
});
