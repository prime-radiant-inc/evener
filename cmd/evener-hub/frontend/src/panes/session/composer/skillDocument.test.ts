import { describe, expect, it } from "vitest";
import {
  documentPositionToTextOffset,
  materializeSkillReferences,
  parseSkillDocument,
  patchSelectionText,
  serializeSkillDocument,
  textOffsetToDocumentPosition,
} from "./skillDocument";

describe("skill document", () => {
  it("patches explicit same-name identities without selecting newly typed prose", () => {
    const value = {
      text: "/inspect then /inspect",
      skillNames: ["inspect"],
      commandNames: ["inspect"],
      mentions: [
        { kind: "skill" as const, name: "inspect", offset: 0 },
        { kind: "command" as const, name: "inspect", offset: 14 },
      ],
    };
    const shifted = patchSelectionText(value, `😀 ${value.text}`);
    expect(shifted).toEqual({
      ...value,
      text: `😀 ${value.text}`,
      mentions: [
        { kind: "skill", name: "inspect", offset: 3 },
        { kind: "command", name: "inspect", offset: 17 },
      ],
    });
    expect(patchSelectionText(shifted, value.text)).toEqual(value);
    expect(patchSelectionText(value, `${value.text} /inspect`)).toEqual({ ...value, text: `${value.text} /inspect` });
    expect(patchSelectionText(value, "/changed then /inspect")).toEqual({
      text: "/changed then /inspect",
      skillNames: [],
      commandNames: ["inspect"],
      mentions: [{ kind: "command", name: "inspect", offset: 14 }],
    });
    // Browser parsing still drops incomplete references and derives implicit atoms.
    expect(patchSelectionText(value, `${value.text}x`)).toEqual({ text: `${value.text}x`, skillNames: ["inspect"] });
    expect(patchSelectionText({ text: "/inspect", skillNames: ["inspect"] }, "😀 /inspect")).toEqual({
      text: "😀 /inspect",
      skillNames: ["inspect"],
    });
  });

  it("reads a trailing colon as sentence punctuation, like a period", () => {
    for (const text of ["Run /cleanup.", "Run /cleanup: fix the parser", "Run /cleanup,"]) {
      expect(serializeSkillDocument(parseSkillDocument({ text, skillNames: ["cleanup"] }))).toEqual({
        text,
        skillNames: ["cleanup"],
      });
    }
  });

  it("still prefers a longer colon-qualified name over its own prefix", () => {
    // The colon inside `plugin:hidden` continues the name; only a trailing one
    // is punctuation.
    expect(
      serializeSkillDocument(
        parseSkillDocument({ text: "Run /plugin:hidden", skillNames: ["plugin", "plugin:hidden"] }),
      ),
    ).toEqual({ text: "Run /plugin:hidden", skillNames: ["plugin:hidden"] });
    expect(serializeSkillDocument(parseSkillDocument({ text: "Run /cleanup.v2", skillNames: ["cleanup"] }))).toEqual({
      text: "Run /cleanup.v2",
      skillNames: [],
    });
  });

  it("appends a missing reference without disturbing the draft's own whitespace", () => {
    // The append happens when a restore carries a selection with no visible
    // reference; the text around it must survive exactly as the user left it.
    expect(materializeSkillReferences("follow up\n", ["probe"])).toBe("follow up\n/probe");
    expect(materializeSkillReferences("follow up", ["probe"])).toBe("follow up /probe");
    expect(materializeSkillReferences("", ["probe"])).toBe("/probe");
    expect(materializeSkillReferences("also /probe\n\n", ["probe"])).toBe("also /probe\n\n");
  });
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

  it.each([
    { text: "Run /cleanup.", selected: ["cleanup"], active: ["cleanup"] },
    { text: "Run /cleanup.v2", selected: ["cleanup", "cleanup.v2"], active: ["cleanup.v2"] },
    { text: "Run /cleanup.v2", selected: ["cleanup"], active: [] },
    { text: "Run /cleanup/extra", selected: ["cleanup"], active: [] },
  ])("review regression: round trips $text with selected $selected", ({ text, selected, active }) => {
    const doc = parseSkillDocument({ text, skillNames: selected });
    expect.soft(serializeSkillDocument(doc)).toEqual({ text, skillNames: active });
    expect
      .soft(doc.content.content.filter((node) => node.type.name === "skill").map((node) => node.attrs.name))
      .toEqual(active);
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
