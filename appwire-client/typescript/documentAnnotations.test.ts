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
import { markerPattern } from "./textareaMarkers";

const SOURCE = "First occurrence: same\n\nOther context for a second occurrence is nearby: same\n";
const FIRST = { start: 18, end: 22 };
const SECOND = { start: 73, end: 77 };
const RICH_SOURCE =
  "# Note\r\n\r\nfirst **same** [link](https://example.test) &amp; 😀\r\n\r\n" +
  "second same\r\n\r\n- item **same**\r\n\r\n" +
  "| Cell | Other |\r\n| --- | --- |\r\n| same | tail |\r\n\r\n```ts\r\nsame()\r\n```\r\n";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
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

// The reference formatter produces prose for a person to read, so these tests
// never match that prose. They read the DATA the reference carries instead:
// every JSON-quoted field, and the code spans around the locator. A change to
// wording that preserves the captured bytes and the structural choices
// (delimiter length, JSON-quoted selection) keeps them green; a change that
// drops, reorders or rewrites captured data fails them.
function jsonQuoted(text: string): string[] {
  return [...text.matchAll(/"((?:[^"\\]|\\.)*)"/g)].map((match) => JSON.parse(`"${match[1]}"`) as string);
}

function codeSpans(text: string): { delimiter: string; content: string }[] {
  const spans: { delimiter: string; content: string }[] = [];
  const pattern = /(`+)([\s\S]*?)\1/g;
  for (let match = pattern.exec(text); match !== null; match = pattern.exec(text)) {
    spans.push({ delimiter: match[1] ?? "", content: match[2] ?? "" });
  }
  return spans;
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
    expect(() => validateSourceRange("a😀b", { start: 1, end: 2 })).toThrow(RangeError);
  });

  it.each([
    ["empty", { start: 1, end: 1 }],
    ["backwards", { start: 2, end: 1 }],
    ["negative", { start: -1, end: 1 }],
    ["past the source", { start: 0, end: 4 }],
    ["fractional", { start: 0.5, end: 1 }],
    ["a split surrogate start", { start: 2, end: 3 }],
  ])("rejects %s ranges", (_name, range) => {
    expect(() => validateSourceRange("a😀", range)).toThrow(RangeError);
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

  it("detects coverage beyond a complete observation's end", () => {
    expect(
      compareDisplayedSources(
        { source: "abcdef", evidence: { fingerprint: displayedFingerprint("abcdef", true) } },
        { source: "abc", evidence: { fingerprint: displayedFingerprint("abc", false) } },
      ),
    ).toEqual({ changed: true, detection: "comparable-source" });
    expect(
      compareDisplayedSources(
        { source: "abc", evidence: { fingerprint: displayedFingerprint("abc", false) } },
        { source: "abcdef", evidence: { fingerprint: displayedFingerprint("abcdef", true) } },
      ),
    ).toEqual({ changed: true, detection: "comparable-source" });
  });

  it("detects a truncated side's proven continuation changing at an equal complete end", () => {
    expect(
      compareDisplayedSources(
        { source: "abc", evidence: { fingerprint: displayedFingerprint("abc", true) } },
        { source: "abc", evidence: { fingerprint: displayedFingerprint("abc", false) } },
      ),
    ).toEqual({ changed: true, detection: "comparable-source" });
    expect(
      compareDisplayedSources(
        { source: "abc", evidence: { fingerprint: displayedFingerprint("abc", false) } },
        { source: "abc", evidence: { fingerprint: displayedFingerprint("abc", true) } },
      ),
    ).toEqual({ changed: true, detection: "comparable-source" });
  });

  it("keeps uncertainty when a complete end extends beyond the prefix coverage", () => {
    expect(
      compareDisplayedSources(
        { source: "abc", evidence: { fingerprint: displayedFingerprint("abc", true) } },
        { source: "abcdef", evidence: { fingerprint: displayedFingerprint("abcdef", false) } },
      ),
    ).toEqual({ changed: false, detection: "insufficient-coverage" });
  });

  it.each([
    ["a suffix deletion", "abcdef", "abc", { changed: true, detection: "comparable-source" }],
    ["a suffix insertion", "abc", "abcdef", { changed: true, detection: "comparable-source" }],
    ["deletion to empty", "abc", "", { changed: true, detection: "comparable-source" }],
    ["insertion from empty", "", "abc", { changed: true, detection: "comparable-source" }],
  ] as const)("detects %s between complete displayed sources", (_case, captured, current, expected) => {
    expect(
      compareDisplayedSources(
        { source: captured, evidence: { fingerprint: displayedFingerprint(captured, false) } },
        { source: current, evidence: { fingerprint: displayedFingerprint(current, false) } },
      ),
    ).toEqual(expected);
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

  it("expands to a whole repeated document without searching every intermediate window", () => {
    // A document whose every paragraph is identical never distinguishes a
    // window, so the expansion must reach the whole document - without
    // scanning each intermediate window, which is quadratic work a
    // selection must not stall the reader on.
    const repeated = "repeat\n\n".repeat(80000);
    // Count the calculation's whole-source searches instead of timing it:
    // each candidate window costs two indexOf scans, so this bound is
    // deterministic work that CPU contention cannot perturb. The binary
    // expansion examines about log2(80,001) windows, while one-at-a-time
    // expansion would examine one per paragraph.
    const searches = vi.spyOn(String.prototype, "indexOf");
    const context = contextForRange(repeated, { start: 0, end: 6 });
    const examined = searches.mock.calls.length;
    searches.mockRestore();
    // The distinguishing window ends at the last paragraph: the trailing
    // separator has nothing after it to repeat, so the window that ends
    // there is already unique.
    expect(context).toEqual({ before: "", after: repeated.slice(6, repeated.length - 2) });
    expect(examined).toBeGreaterThan(0);
    expect(examined).toBeLessThanOrEqual(40);
  }, 60_000);
});

describe("annotation reference formatting", () => {
  it("prints captured data in ordinary draft text without changing entered bytes", () => {
    const capture = passage();
    const note = "Keep this scope.";
    const [locatorLine, evidenceLine, contextLine] = formatAnnotationReference(capture, "NOTES.md", note).split("\n");
    // The locator is the captured range in a code span, and the selection and
    // note survive as the exact bytes entered.
    expect(codeSpans(locatorLine ?? "")).toContainEqual({ delimiter: "`", content: "NOTES.md:1:19-1:22" });
    expect(locatorLine).toContain(capture.selectedRenderedText);
    expect(jsonQuoted(locatorLine ?? "")).toEqual([note]);
    // Every captured evidence and context field round-trips through JSON.
    expect(jsonQuoted(evidenceLine ?? "")).toEqual([capture.evidence.wholeRevision]);
    expect(jsonQuoted(contextLine ?? "")).toEqual([capture.contextBefore, capture.contextAfter]);
  });

  it("uses a longer code delimiter for backtick paths and encodes control characters unambiguously", () => {
    const path = "dir/a`b\nback\\slash.md";
    const reference = formatAnnotationReference(passage(), path, 'line one\n"line two"');
    const firstLine = reference.split("\n", 1)[0] ?? "";
    // The path contains a backtick, so the locator's code span needs a longer
    // delimiter, and the locator itself carries the escaped path and the
    // captured positions.
    expect(codeSpans(firstLine)).toEqual([{ delimiter: "``", content: " dir/a`b\\nback\\\\slash.md:1:19-1:22 " }]);
    expect(jsonQuoted(firstLine)).toEqual(['line one\n"line two"']);
  });

  it("uses a JSON-quoted selection when curly quote delimiters occur in selected text", () => {
    const selected = "left “same” right";
    const reference = formatAnnotationReference(passage(FIRST, selected), "NOTES.md", "note");
    const firstLine = reference.split("\n", 1)[0] ?? "";
    // Curly quotes force the JSON form; the captured selection still decodes
    // to exactly the bytes selected, and the note stays separately quoted.
    expect(jsonQuoted(firstLine)).toEqual([selected, "note"]);
  });

  it("labels prefix-only evidence without claiming whole-file coverage", () => {
    const fingerprint = displayedFingerprint(SOURCE, true);
    const prefixCapture: CapturedPassage = { ...passage(), evidence: { fingerprint } };
    const fullCapture: CapturedPassage = {
      ...passage(),
      evidence: { fingerprint: displayedFingerprint(SOURCE, false) },
    };
    const prefixLine = formatAnnotationReference(prefixCapture, "NOTES.md", "note").split("\n")[1] ?? "";
    const fullLine = formatAnnotationReference(fullCapture, "NOTES.md", "note").split("\n")[1] ?? "";
    // The scope is behaviorally distinct: the same fingerprint data renders a
    // different reference for a truncated read than for a complete one, and
    // both carry the fingerprint value and the coverage they were built from.
    expect(prefixLine).not.toBe(fullLine);
    for (const line of [prefixLine, fullLine]) {
      expect(jsonQuoted(line)).toContain(fingerprint.value);
      expect(line).toContain(fingerprint.algorithm);
      expect(line).toContain(`[${fingerprint.coverage.start},${fingerprint.coverage.end})`);
    }
  });

  it("neutralizes attachment markers in every captured field it prints", () => {
    const capture = passage(FIRST, "click [image 1] now");
    capture.contextBefore = "before [image 1] ";
    capture.contextAfter = " after [image 1]";
    capture.evidence = {
      wholeRevision: "rev [image 1]",
      fingerprint: displayedFingerprint(SOURCE, false),
    };
    const note = "keep [image 1] as text";
    const reference = formatAnnotationReference(capture, "NOTES.md", note);

    // Send-time translation rewrites marker-shaped text, including inside a
    // code span, so the formatter itself must leave no marker to match. The
    // append-and-send boundary is exercised in the app tree, where the
    // composer's own append lives.
    expect(markerPattern().test(reference)).toBe(false);
    // Removing the inserted separators restores every captured field byte for
    // byte, so neutralization neither drops nor rewrites the user's text.
    const [locatorLine, evidenceLine, contextLine] = reference.replaceAll("\u200b", "").split("\n");
    expect(jsonQuoted(locatorLine ?? "")).toEqual([note]);
    expect(locatorLine).toContain(capture.selectedRenderedText);
    expect(jsonQuoted(evidenceLine ?? "")).toEqual([capture.evidence.wholeRevision]);
    expect(jsonQuoted(contextLine ?? "")).toEqual([capture.contextBefore, capture.contextAfter]);
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
