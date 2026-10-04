import { afterEach, describe, expect, it, vi } from "vitest";
import {
  type AnnotationCommentWork,
  type CapturedPassage,
  compareDisplayedSources,
  contextForRange,
  type DisplayFingerprint,
  displayedFingerprint,
  editCommentText,
  formatAnnotationReference,
  hashAnnotationText,
  invalidateCommentCapture,
  selectCommentPassage,
  sourcePositions,
  validateSourceRange,
} from "./index";

const SOURCE = "First occurrence: same\n\nOther context for a second occurrence is nearby: same\n";
const FIRST = { start: 18, end: 22 };
const SECOND = { start: 73, end: 77 };
const RICH_SOURCE =
  "# Note\r\n\r\nfirst **same** [link](https://example.test) &amp; 😀\r\n\r\n" +
  "second same\r\n\r\n- item **same**\r\n\r\n" +
  "| Cell | Other |\r\n| --- | --- |\r\n| same | tail |\r\n\r\n```ts\r\nsame()\r\n```\r\n";

afterEach(() => {
  vi.unstubAllGlobals();
});

function passage(range = FIRST, selectedRenderedText = "same"): CapturedPassage {
  const positions =
    range === FIRST
      ? { start: { line: 1, column: 19 }, endInclusive: { line: 1, column: 22 } }
      : {
          start: { line: 3, column: 50 },
          endInclusive: { line: 3, column: 53 },
        };
  return {
    evidence: { wholeRevision: "rev-A", fingerprint: displayedFingerprint(SOURCE, false) },
    sourceRange: range,
    ...positions,
    selectedRenderedText,
    contextBefore: range === FIRST ? "First occurrence: " : "Other context for a second occurrence is nearby: ",
    contextAfter: range === FIRST ? "\n\nOther context for a second occurrence is nearby: same\n" : "\n",
    mappingId: "fixture-map",
    displayedSnapshotId: "snapshot-A",
  };
}

describe("source ranges", () => {
  it("uses the second literal occurrence and inclusive UTF16 positions", () => {
    expect(sourcePositions(SOURCE, SECOND)).toEqual({
      start: { line: 3, column: 50 },
      endInclusive: { line: 3, column: 53 },
    });
    expect(sourcePositions(RICH_SOURCE, { start: 60, end: 62 })).toEqual({
      start: { line: 3, column: 51 },
      endInclusive: { line: 3, column: 52 },
    });
    expect(() => validateSourceRange("a😀b", { start: 1, end: 2 })).toThrow();
  });

  it.each([
    ["empty", { start: 1, end: 1 }],
    ["backwards", { start: 2, end: 1 }],
    ["negative", { start: -1, end: 1 }],
    ["past the source", { start: 0, end: 4 }],
    ["fractional", { start: 0.5, end: 1 }],
    ["a split surrogate start", { start: 2, end: 3 }],
  ])("rejects %s ranges", (_name, range) => {
    expect(() => validateSourceRange("a😀", range)).toThrow();
  });

  it("counts CRLF as one line break while retaining both source units", () => {
    expect(sourcePositions("a\r\nb", { start: 3, end: 4 })).toEqual({
      start: { line: 2, column: 1 },
      endInclusive: { line: 2, column: 1 },
    });
  });
});

describe("display evidence", () => {
  it("builds exact full and prefix coverage without requiring Web Crypto", () => {
    vi.stubGlobal("crypto", undefined);
    expect(hashAnnotationText("")).toBe("wvjl67o803");
    expect(hashAnnotationText("abc")).toBe("1dtlchkfkb1");
    expect(displayedFingerprint("abc", false)).toEqual({
      algorithm: "cyrb53",
      value: "1dtlchkfkb1",
      coverage: { start: 0, end: 3 },
      scope: "displayed-full",
    });
    expect(displayedFingerprint("abc", true)).toEqual({
      algorithm: "cyrb53",
      value: "1dtlchkfkb1",
      coverage: { start: 0, end: 3 },
      scope: "displayed-prefix",
    });
  });

  it("does not treat a shorter equal prefix as an edit", () => {
    expect(
      compareDisplayedSources(
        { source: "abcdef", evidence: { fingerprint: displayedFingerprint("abcdef", true) } },
        { source: "abc", evidence: { fingerprint: displayedFingerprint("abc", true) } },
      ),
    ).toEqual({ changed: false, detection: "insufficient-coverage" });
  });

  it("expires on whole revision change despite equal visible source", () => {
    const fingerprint = displayedFingerprint("abc", true);
    expect(
      compareDisplayedSources(
        { source: "abc", evidence: { wholeRevision: "A", fingerprint } },
        { source: "abc", evidence: { wholeRevision: "B", fingerprint } },
      ).changed,
    ).toBe(true);
  });

  it("compares actual source despite an identical supplied fingerprint", () => {
    const fingerprint: DisplayFingerprint = {
      algorithm: "cyrb53",
      value: "deliberate-collision",
      coverage: { start: 0, end: 6 },
      scope: "displayed-full",
    };
    expect(
      compareDisplayedSources(
        { source: "abcdef", evidence: { fingerprint } },
        { source: "abcxef", evidence: { fingerprint } },
      ),
    ).toEqual({ changed: true, detection: "comparable-source" });
  });

  it("detects a changed byte inside shared prefix coverage", () => {
    expect(
      compareDisplayedSources(
        { source: "abcdef", evidence: { fingerprint: displayedFingerprint("abcdef", true) } },
        { source: "abx", evidence: { fingerprint: displayedFingerprint("abx", true) } },
      ),
    ).toEqual({ changed: true, detection: "comparable-source" });
  });
});

describe("captured context", () => {
  it("starts at the selected passage's paragraph boundaries", () => {
    expect(contextForRange(SOURCE, FIRST)).toEqual({ before: "First occurrence: ", after: "" });
    expect(contextForRange(SOURCE, SECOND)).toEqual({
      before: "Other context for a second occurrence is nearby: ",
      after: "\n",
    });
  });

  it("expands across neighboring paragraphs when the displayed paragraph is repeated", () => {
    const repeated = "before one\n\nsame\n\ndivider\n\nsame\n\nafter two";
    expect(contextForRange(repeated, { start: 12, end: 16 })).toEqual({
      before: "before one\n\n",
      after: "\n\ndivider",
    });
  });
});

describe("annotation reference formatting", () => {
  it("prints captured data in ordinary draft text without changing entered bytes", () => {
    const reference = formatAnnotationReference(passage(), "NOTES.md", "Keep this scope.");
    expect(reference.split("\n").slice(0, 3)).toEqual([
      'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Keep this scope."',
      'Captured whole-file revision: "rev-A".',
      'Context before: "First occurrence: "; after: "\\n\\nOther context for a second occurrence is nearby: same\\n".',
    ]);
  });

  it("uses a longer code delimiter for backtick paths and encodes control characters unambiguously", () => {
    const path = "dir/a`b\nback\\slash.md";
    const reference = formatAnnotationReference(passage(), path, 'line one\n"line two"');
    const firstLine = reference.split("\n", 1)[0] ?? "";
    expect(firstLine).toContain("`` dir/a`b\\nback\\\\slash.md:1:19-1:22 ``");
    expect(firstLine).toContain(': "line one\\n\\"line two\\""');
  });

  it("uses a JSON-quoted selection when curly quote delimiters occur in selected text", () => {
    const reference = formatAnnotationReference(passage(FIRST, "left “same” right"), "NOTES.md", "note");
    expect(reference.split("\n", 1)[0]).toContain('selecting "left “same” right": "note"');
  });

  it("labels prefix-only evidence without claiming whole-file coverage", () => {
    const capture = passage();
    capture.evidence = { fingerprint: displayedFingerprint(SOURCE, true) };
    const evidenceLine = formatAnnotationReference(capture, "NOTES.md", "note").split("\n")[1];
    expect(evidenceLine).toBe(
      'Captured displayed-prefix reference: cyrb53 "l56j2xu13f", covering UTF-16 source range [0,78); content after this displayed prefix was not captured.',
    );
  });
});

describe("annotation comment work", () => {
  it("freezes the first passage on the first note edit and retains it through later selections", () => {
    const first = passage();
    const second = passage(SECOND);
    const selected = selectCommentPassage({ note: "", captureFrozen: false }, first);
    const edited = editCommentText(selected, "Keep this scope.\n");
    const reselected = selectCommentPassage(edited, second);
    expect(edited).toEqual({ note: "Keep this scope.\n", capture: first, captureFrozen: true });
    expect(reselected).toEqual(edited);
    expect(selected).toEqual({ note: "", capture: first, captureFrozen: false });
  });

  it("treats a whitespace-only first edit as capture-freezing work", () => {
    const first = passage();
    expect(editCommentText({ note: "", capture: first, captureFrozen: false }, " ")).toEqual({
      note: " ",
      capture: first,
      captureFrozen: true,
    });
  });

  it("removes an expired capture, retains exact note bytes, then freezes the first fresh selection", () => {
    const first = passage();
    const second = passage(SECOND);
    const frozen: AnnotationCommentWork = { note: "Keep this scope.\n", capture: first, captureFrozen: true };
    const invalidated = invalidateCommentCapture(frozen);
    const attached = selectCommentPassage(invalidated, second);
    expect(invalidated).toEqual({ note: "Keep this scope.\n", captureFrozen: false });
    expect(attached).toEqual({ note: "Keep this scope.\n", capture: second, captureFrozen: true });
    expect(frozen).toEqual({ note: "Keep this scope.\n", capture: first, captureFrozen: true });
  });
});
