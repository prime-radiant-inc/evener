import { describe, expect, it } from "vitest";
import {
  appendLiteralReference,
  documentPositionToTextOffset,
  materializeSkillReferences,
  parseSkillDocument,
  type SkillEditorValue,
  serializeSkillDocument,
  skillAtomMentions,
  textOffsetToDocumentPosition,
} from "./skillDocument";

describe("skill document", () => {
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

describe("appendLiteralReference", () => {
  // Every expected text, offset and atom below is an independently declared
  // literal; none is computed with the helper or parser under test. The parsed
  // result is read back to prove the appended value actually carries those atoms.
  function atomsOf(value: SkillEditorValue): Array<{ kind: string; name: string; text: string }> {
    return parseSkillDocument(value)
      .content.content.filter((node) => !node.isText)
      .map((node) => ({
        kind: node.type.name,
        name: String(node.attrs.name),
        text: `/${String(node.attrs.name)}`,
      }));
  }

  it("keeps an absent mention set absent when the suffix infers no atom", () => {
    const current = { text: "keep\n", skillNames: ["plan"] };
    const result = appendLiteralReference(current, "See /other.");
    // The input's own trailing newline is preserved ahead of the appended blank line.
    expect(result).toEqual({ text: "keep\n\n\nSee /other.", skillNames: ["plan"] });
    expect(Object.hasOwn(result, "mentions")).toBe(false);
    expect(atomsOf(result)).toEqual([]);
    expect(skillAtomMentions(parseSkillDocument(result))).toEqual([]);
    expect(current).toEqual({ text: "keep\n", skillNames: ["plan"] });
  });

  it("pins only the original inferred location when the suffix would add a skill atom", () => {
    const current = { text: "Run /plan", skillNames: ["plan", "review"] };
    const result = appendLiteralReference(current, "See /review.");
    expect(result).toEqual({
      text: "Run /plan\n\nSee /review.",
      skillNames: ["plan", "review"],
      mentions: [{ kind: "skill", name: "plan", offset: 4 }],
    });
    // The appended `/review` stays literal: only the pinned original atom parses.
    expect(atomsOf(result)).toEqual([{ kind: "skill", name: "plan", text: "/plan" }]);
    expect(skillAtomMentions(parseSkillDocument(result))).toEqual([{ kind: "skill", name: "plan", offset: 4 }]);
    expect(serializeSkillDocument(parseSkillDocument(result))).toEqual({
      text: "Run /plan\n\nSee /review.",
      skillNames: ["plan"],
    });
    expect(current).toEqual({ text: "Run /plan", skillNames: ["plan", "review"] });
  });

  it("pins an empty inferred set when a skill suffix first introduces an atom", () => {
    const current = { text: "keep", skillNames: ["plan"] };
    const result = appendLiteralReference(current, "See /plan.");
    expect(result).toEqual({ text: "keep\n\nSee /plan.", skillNames: ["plan"], mentions: [] });
    expect(atomsOf(result)).toEqual([]);
    expect(skillAtomMentions(parseSkillDocument(result))).toEqual([]);
    expect(current).toEqual({ text: "keep", skillNames: ["plan"] });
  });

  it("keeps a command suffix literal when only a command name is selected", () => {
    const current = { text: "step", skillNames: [], commandNames: ["plan"] };
    const result = appendLiteralReference(current, "Run /plan.");
    expect(result).toEqual({
      text: "step\n\nRun /plan.",
      skillNames: [],
      commandNames: ["plan"],
      mentions: [],
    });
    expect(atomsOf(result)).toEqual([]);
    expect(current).toEqual({ text: "step", skillNames: [], commandNames: ["plan"] });
  });

  it("keeps explicit empty and nonempty same-spelling skill/command mentions untouched", () => {
    const explicitEmpty = { text: "step", skillNames: ["plan"], commandNames: ["plan"], mentions: [] };
    const emptied = appendLiteralReference(explicitEmpty, "Run /plan.");
    expect(emptied).toEqual({
      text: "step\n\nRun /plan.",
      skillNames: ["plan"],
      commandNames: ["plan"],
      mentions: [],
    });
    expect(atomsOf(emptied)).toEqual([]);

    const explicitPinned = {
      text: "/same and /same",
      skillNames: ["same"],
      commandNames: ["same"],
      mentions: [
        { kind: "skill" as const, name: "same", offset: 0 },
        { kind: "command" as const, name: "same", offset: 10 },
      ],
    };
    const pinned = appendLiteralReference(explicitPinned, "Trailing prose.");
    expect(pinned).toEqual({ ...explicitPinned, text: "/same and /same\n\nTrailing prose." });
    expect(atomsOf(pinned)).toEqual([
      { kind: "skill", name: "same", text: "/same" },
      { kind: "command", name: "same", text: "/same" },
    ]);
    expect(skillAtomMentions(parseSkillDocument(pinned))).toEqual([
      { kind: "skill", name: "same", offset: 0 },
      { kind: "command", name: "same", offset: 10 },
    ]);
    // The input's text and mention array are not mutated.
    expect(explicitPinned.text).toBe("/same and /same");
    expect(explicitPinned.mentions).toEqual([
      { kind: "skill", name: "same", offset: 0 },
      { kind: "command", name: "same", offset: 10 },
    ]);
  });

  it("appends to empty text and preserves a generic extra field without mutating the input", () => {
    const rich = { text: "", skillNames: [], attachments: [{ id: "a", kind: "image" }] };
    const result = appendLiteralReference(rich, "Reference text.");
    expect(result).toEqual({
      text: "Reference text.",
      skillNames: [],
      attachments: [{ id: "a", kind: "image" }],
    });
    expect(result.attachments).toBe(rich.attachments);
    expect(atomsOf(result)).toEqual([]);
    expect(rich).toEqual({ text: "", skillNames: [], attachments: [{ id: "a", kind: "image" }] });
  });
});
