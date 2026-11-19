import { EditorState, type Transaction } from "prosemirror-state";
import { describe, expect, it } from "vitest";
import {
  documentPositionToTextOffset,
  mapStagedImageRanges,
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
  it("marks exactly the adopted occurrences and round trips them", () => {
    // Only marker 2 is adopted: marker 1 is prose the same text spells, and a
    // marker the caller did not name must never gain a mark.
    const doc = parseSkillDocument({ text: "a [image 1] b [image 2]", skillNames: [], adoptedImageMarkers: [2] });
    const marked = doc.content.content.filter((node) => node.marks.some((mark) => mark.type.name === "stagedAnchor"));
    expect(marked.map((node) => node.text)).toEqual(["[image 2]"]);
    expect(serializeSkillDocument(doc)).toEqual({
      text: "a [image 1] b [image 2]",
      skillNames: [],
      adoptedImageMarkers: [2],
      adoptedImageRanges: [{ marker: 2, start: 14, end: 23 }],
    });
  });

  it("keeps the mark on the literal only, not on text that follows it", () => {
    // A mark that leaked past its own occurrence would refuse a person's prose.
    const doc = parseSkillDocument({ text: "[image 1] tail", skillNames: [], adoptedImageMarkers: [1] });
    const runs = doc.content.content.map((node) => ({
      text: node.text,
      marked: node.marks.some((mark) => mark.type.name === "stagedAnchor"),
    }));
    expect(runs).toEqual([
      { text: "[image 1]", marked: true },
      { text: " tail", marked: false },
    ]);
  });

  it("drops the adoption when the occurrence is gone and restores nothing it did not mark", () => {
    // What a delete does to the document: no literal, so nothing to report.
    expect(
      serializeSkillDocument(parseSkillDocument({ text: "a b", skillNames: [], adoptedImageMarkers: [2] })),
    ).toEqual({
      text: "a b",
      skillNames: [],
    });
    // A marker with no adoption is not provenance, whatever the number.
    expect(serializeSkillDocument(parseSkillDocument({ text: "[image 2]", skillNames: [] }))).toEqual({
      text: "[image 2]",
      skillNames: [],
    });
  });

  it("splits a run so an adopted occurrence between atoms keeps its mark", () => {
    const doc = parseSkillDocument({
      text: "/cleanup and [image 1] done",
      skillNames: [],
      commandNames: ["cleanup"],
      adoptedImageMarkers: [1],
    });
    expect(serializeSkillDocument(doc)).toEqual({
      text: "/cleanup and [image 1] done",
      skillNames: [],
      commandNames: ["cleanup"],
      mentions: [{ kind: "command", name: "cleanup", offset: 0 }],
      adoptedImageMarkers: [1],
      adoptedImageRanges: [{ marker: 1, start: 13, end: 22 }],
    });
  });

  it("keeps the adoption across a plain-text patch that leaves the occurrence alone", () => {
    const value = { text: "kept [image 1]", skillNames: [], adoptedImageMarkers: [1] };
    expect(patchSelectionText(value, "😀 kept [image 1]")).toEqual({
      text: "😀 kept [image 1]",
      skillNames: [],
      adoptedImageMarkers: [1],
      adoptedImageRanges: [{ marker: 1, start: 8, end: 17 }],
    });
    // The same patch with the occurrence deleted reflects that, because the
    // patched document no longer holds the mark.
    expect(patchSelectionText(value, "kept ")).toEqual({ text: "kept ", skillNames: [] });
  });

  // Two spellings of one number in one document. The ranges say WHICH one has
  // no image behind it, so the literal the person typed stays unmarked - that
  // distinction is the whole fix: without it any reader of the record has to
  // guess, and deleting "the first [image 1]" removes the wrong one.
  it("marks only the occurrence an explicit range names, not an identical literal", () => {
    const text = "[image 1] tail [image 1]";
    const doc = parseSkillDocument({
      text,
      skillNames: [],
      adoptedImageMarkers: [1],
      adoptedImageRanges: [{ marker: 1, start: 15, end: 24 }],
    });
    const marked = doc.content.content
      .filter((node) => node.marks.some((mark) => mark.type.name === "stagedAnchor"))
      .map((node) => node.text);
    expect(marked).toEqual(["[image 1]"]);
    // Round trip: the same ranges come back, still naming the adopted one.
    expect(serializeSkillDocument(doc)).toEqual({
      text,
      skillNames: [],
      adoptedImageMarkers: [1],
      adoptedImageRanges: [{ marker: 1, start: 15, end: 24 }],
    });
  });

  it("falls back to every occurrence of an adopted number when a value carries no ranges", () => {
    // A record an older build wrote has numbers only. Marking all of them is
    // the only honest reading of such a record - and the ranges it serializes
    // back out name each one, so the next reader can be exact.
    const text = "[image 1] tail [image 1]";
    expect(serializeSkillDocument(parseSkillDocument({ text, skillNames: [], adoptedImageMarkers: [1] }))).toEqual({
      text,
      skillNames: [],
      adoptedImageMarkers: [1],
      adoptedImageRanges: [
        { marker: 1, start: 0, end: 9 },
        { marker: 1, start: 15, end: 24 },
      ],
    });
  });

  it("discards a range whose slice is not that marker", () => {
    // A range is only a fact about the text when it still spells the marker.
    // With nothing else to go on (no number list), the stale offset marks
    // nothing - prose never becomes a refusal on the strength of a bad offset.
    const doc = parseSkillDocument({
      text: "[image 1] tail [image 1]",
      skillNames: [],
      adoptedImageMarkers: [],
      adoptedImageRanges: [{ marker: 1, start: 2, end: 11 }],
    });
    const marked = doc.content.content.filter((node) => node.marks.some((mark) => mark.type.name === "stagedAnchor"));
    expect(marked.map((node) => node.text)).toEqual([]);
  });

  it("shifts the ranges when a patch prepends text before them", () => {
    const value = {
      text: "[image 1] tail [image 1]",
      skillNames: [],
      adoptedImageMarkers: [1],
      adoptedImageRanges: [{ marker: 1, start: 15, end: 24 }],
    };
    expect(patchSelectionText(value, "😀 [image 1] tail [image 1]")).toEqual({
      text: "😀 [image 1] tail [image 1]",
      skillNames: [],
      adoptedImageMarkers: [1],
      adoptedImageRanges: [{ marker: 1, start: 18, end: 27 }],
    });
  });

  it("keeps an adopted occurrence across a splice the caller reports exactly", () => {
    const value = {
      text: "[image 1]",
      skillNames: [],
      adoptedImageMarkers: [1],
      adoptedImageRanges: [{ marker: 1, start: 0, end: 9 }],
    };
    // An attachment gesture inserted "[image 2]" at the caret before the
    // adopted literal. The inferred splice shares "[image " with it and reads
    // as a replacement THROUGH the anchor; the gesture's own coordinates say
    // it replaced nothing before the anchor, so the anchor survives.
    expect(patchSelectionText(value, "[image 2][image 1]")).toEqual({ text: "[image 2][image 1]", skillNames: [] });
    expect(patchSelectionText(value, "[image 2][image 1]", { start: 0, end: 0, insertedLength: 9 })).toEqual({
      text: "[image 2][image 1]",
      skillNames: [],
      adoptedImageMarkers: [1],
      adoptedImageRanges: [{ marker: 1, start: 9, end: 18 }],
    });
  });

  // The staged occurrences are positions in a document, so the only honest
  // oracle for moving them is the editor's own transaction mapping: these
  // cases apply a real ProseMirror transaction and ask the mapper what it says
  // about them. A before/after pair would have to GUESS.
  describe("mapStagedImageRanges", () => {
    /** Apply `edit` to a fresh state parsed from `text` and return the one
     * transaction it produced - a real one, with its own mapping. */
    function applied(text: string, edit: (state: EditorState) => Transaction) {
      const state = EditorState.create({ doc: parseSkillDocument({ text, skillNames: [] }) });
      const transaction = edit(state);
      return { transaction, text: serializeSkillDocument(transaction.doc).text };
    }

    const staged = { marker: 1, start: 0, end: 9 };

    it("leaves an occurrence alone when the edit does not touch it", () => {
      expect(mapStagedImageRanges([], [staged])).toEqual([]);
      expect(mapStagedImageRanges([], undefined)).toEqual([]);
      const { transaction, text } = applied("[image 1] tail", (state) => state.tr.insertText("!", 14));
      expect(text).toBe("[image 1] tail!");
      expect(mapStagedImageRanges([transaction], [staged])).toEqual([staged]);
    });

    it("moves the occurrence by what the transaction inserted before it", () => {
      // Paste "[image 1] is literal. " in front of the staged literal. Both
      // spellings are identical, so a range left at its old offsets names the
      // pasted prose instead of the anchor.
      const { transaction, text } = applied("[image 1]", (state) => state.tr.insertText("[image 1] is literal. ", 0));
      expect(text).toBe("[image 1] is literal. [image 1]");
      expect(mapStagedImageRanges([transaction], [staged])).toEqual([{ marker: 1, start: 22, end: 31 }]);
    });

    it("keeps an occurrence whose own edges were typed against", () => {
      // Typing at the first character pushes the literal right; typing at the
      // character after its last one must not grow it into the person's prose.
      const left = applied("[image 1]", (state) => state.tr.insertText("-", 0));
      expect(mapStagedImageRanges([left.transaction], [staged])).toEqual([{ marker: 1, start: 1, end: 10 }]);
      const right = applied("[image 1]", (state) => state.tr.insertText("-", 9));
      expect(mapStagedImageRanges([right.transaction], [staged])).toEqual([staged]);
    });

    it("drops an occurrence the transaction deleted or cut through", () => {
      const deleted = applied("[image 1] tail", (state) => state.tr.delete(0, 9));
      expect(deleted.text).toBe(" tail");
      expect(mapStagedImageRanges([deleted.transaction], [staged])).toEqual([]);
      // One bracket gone: a fragment is not the literal its number names, and
      // the refusal must not outlive the occurrence.
      const cut = applied("[image 1] tail", (state) => state.tr.delete(8, 9));
      expect(cut.text).toBe("[image 1 tail");
      expect(mapStagedImageRanges([cut.transaction], [staged])).toEqual([]);
    });

    it("moves the occurrence back when an undo reverses what a keystroke moved", () => {
      // Undo arrives as the inverse transaction the history plugin builds, so
      // the occurrence follows the same mapping it followed forward. Typing a
      // character and undoing it must leave the range exactly as it started.
      const state = EditorState.create({ doc: parseSkillDocument({ text: "[image 1] tail", skillNames: [] }) });
      const typed = state.tr.insertText("-", 0);
      expect(serializeSkillDocument(typed.doc).text).toBe("-[image 1] tail");
      const afterTyping = state.apply(typed);
      const undone = afterTyping.tr.delete(0, 1);
      expect(serializeSkillDocument(undone.doc).text).toBe("[image 1] tail");
      const shifted = mapStagedImageRanges([typed], [staged]);
      expect(shifted).toEqual([{ marker: 1, start: 1, end: 10 }]);
      expect(mapStagedImageRanges([undone], shifted)).toEqual([staged]);
    });
  });
});
