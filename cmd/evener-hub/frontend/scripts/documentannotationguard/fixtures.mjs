// Literal, test-only fixtures for the early production-stack annotation
// journey. Nothing in this file imports, calls or derives from the production
// mapper, renderer, context helper or draft formatter: these values are copied
// verbatim from the approved independent-literals record so a driver assertion
// cannot be satisfied by the code under test agreeing with itself.
//
// SOURCE is the exact fixture document the Go test writes into the session
// workspace. The hub hashes those exact bytes into the lowercase SHA256 below
// and serves it as the /doc/file?format=raw response ETag; SOURCE_SHA256 is the
// value JOURNEY_REFERENCE quotes as the captured whole-file revision.

export const SOURCE = "First occurrence: same\n\nOther context for a second occurrence is nearby: same\n";

// The intended passage: offsets 18..22 inside SOURCE (1:19-1:22 in the
// reference's line:column spelling), selecting "same".
export const FIRST = { start: 18, end: 22 };

// The next textual "same" in SOURCE, quoted only so the fixture is complete.
export const SECOND = { start: 73, end: 77 };

// sha256("First occurrence: same\n\nOther context for a second occurrence is nearby: same\n").
// SOURCE.length is 78.
export const SOURCE_SHA256 = "4808d04cf747af87429a6d65d3c229bba77e2ce07a9145e520400d9fdebff0a6";

// The exact production reference a first annotation on FIRST must append, and
// the exact accumulated draft after the annotation Send. The revision and the
// empty after-context are the accepted contextForRange bounds for this
// paragraph, not values this driver computes.
export const JOURNEY_REFERENCE =
  "A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: \"Keep this scope.\"\n" +
  "Captured whole-file revision: \"4808d04cf747af87429a6d65d3c229bba77e2ce07a9145e520400d9fdebff0a6\".\n" +
  "Context before: \"First occurrence: \"; after: \"\".\n" +
  "Verify the quotation and context against current content before relying on these captured coordinates.";

export const JOURNEY_APPENDED = "Existing draft\n\n" + JOURNEY_REFERENCE;

// The literal note text the annotation Send types into the comment box.
export const NOTE = "Keep this scope.";

// ---- long-document fixture (visible controls/notes at the reading position) ----
//
// A genuinely overflowing document whose FIRST passage sits near the top. The
// short fixture above is 78 bytes and never scrolls; this one is 6807 bytes and
// forces the PaneScaffold scroll body to overflow, so a comment box or revealed
// note placed at the document's END (the defect) falls outside the visible clip.
// Built ONLY from the literal first line and literal filler sections - never the
// production mapper, renderer, context helper or draft formatter.

// The opening line's literal text; the whole first paragraph is this one line.
export const LONG_FIRST_LINE = "Long document opening target: same";
export const LONG_FILLER_SECTIONS = 160;
export const LONG_SOURCE =
  `${LONG_FIRST_LINE}\n\n` +
  Array.from({ length: LONG_FILLER_SECTIONS }, (_, index) => `Section ${index + 1} of the long fixture document.`).join(
    "\n\n",
  ) +
  "\n";

// The intended early passage: offsets 30..34 inside LONG_SOURCE (1:31-1:34 in
// the reference's line:column spelling), selecting "same".
export const LONG_FIRST = { start: 30, end: 34 };

// Independent display positions for LONG_FIRST, computed from the literal line
// ("Long document opening target: " is 30 characters before "same").
export const LONG_FIRST_POSITIONS = { start: { line: 1, column: 31 }, endInclusive: { line: 1, column: 34 } };

// sha256(LONG_SOURCE), identical to the Go test's annotationLongSource() bytes.
export const LONG_SOURCE_SHA256 = "d202b4b90d7caa31111dc04884d9cc300b79ea2070edaa344023ae377540ddae";

// The exact production reference a first annotation on LONG_FIRST must append.
// The revision and empty after-context are the accepted contextForRange bounds
// for this opening paragraph, independent of the formatter under test.
export const LONG_REFERENCE =
  'A note on `NOTES.md:1:31-1:34` (captured source positions), selecting “same”: "Keep this scope."\n' +
  'Captured whole-file revision: "d202b4b90d7caa31111dc04884d9cc300b79ea2070edaa344023ae377540ddae".\n' +
  'Context before: "Long document opening target: "; after: "".\n' +
  "Verify the quotation and context against current content before relying on these captured coordinates.";

export const LONG_JOURNEY_APPENDED = "Existing draft\n\n" + LONG_REFERENCE;

// ---- note-stack fixture (long / unbroken / overlapping revealed notes) ----
//
// Three OVERLAPPING passages in a paragraph LOW in LONG_SOURCE, saved through
// real selections. The revealed note stack is pinned to the top of the visible
// scroll clip, so a target lower in the body stays clear of the short notes that
// reveal while the earlier drags cross their bands; the tall note is saved LAST,
// so the final reveal is the complete stack that must overflow the body. Built
// from literal strings and source arithmetic only - no production formatter,
// mapper or renderer.

// The target paragraph's literal line text, uniquely present in LONG_SOURCE.
export const NOTE_STACK_TARGET_LINE = "Section 40 of the long fixture document.";
const NOTE_STACK_LINE_START = LONG_SOURCE.indexOf(NOTE_STACK_TARGET_LINE);
if (NOTE_STACK_LINE_START < 0) throw new Error("the note-stack target line is missing from LONG_SOURCE");
const NOTE_STACK_LINE_NUMBER = (LONG_SOURCE.slice(0, NOTE_STACK_LINE_START).match(/\n/g)?.length ?? 0) + 1;
// The target paragraph's own bounds (paragraphs are blank-line separated).
export const NOTE_STACK_PARAGRAPH_START = LONG_SOURCE.lastIndexOf("\n\n", NOTE_STACK_LINE_START - 1) + 2;
const NOTE_STACK_PARAGRAPH_END = LONG_SOURCE.indexOf("\n\n", NOTE_STACK_LINE_START);

// Line-relative offsets, all pairwise overlapping, in creation order.
// RIGHT-to-LEFT creation so each later drag starts LEFT of, and ends RIGHT of,
// every earlier band (a mouseup on a band button yields a runaway selection).
const NOTE_STACK_LINE_OFFSETS = [
  { start: 20, end: 24 }, // "fixt"
  { start: 16, end: 28 }, // "long fixture"
  { start: 12, end: 32 }, // "f the long fixture d"
];
export const NOTE_STACK_RANGES = NOTE_STACK_LINE_OFFSETS.map((offset) => ({
  start: NOTE_STACK_LINE_START + offset.start,
  end: NOTE_STACK_LINE_START + offset.end,
}));
// The same ranges as LINE-RELATIVE offsets, for the DOM Range expressions that
// operate on the target paragraph's single text node.
export const NOTE_STACK_LINE_RANGES = NOTE_STACK_LINE_OFFSETS.map((offset) => ({ start: offset.start, end: offset.end }));
export const NOTE_STACK_POSITIONS = NOTE_STACK_RANGES.map((range) => ({
  start: { line: NOTE_STACK_LINE_NUMBER, column: range.start - NOTE_STACK_LINE_START + 1 },
  endInclusive: { line: NOTE_STACK_LINE_NUMBER, column: range.end - NOTE_STACK_LINE_START },
}));

// The accepted contextForRange bounds for the target paragraph (unique text, so
// no paragraph expansion): the whole paragraph around each range.
function noteStackContext(range) {
  return {
    before: LONG_SOURCE.slice(NOTE_STACK_PARAGRAPH_START, range.start),
    after: LONG_SOURCE.slice(range.end, NOTE_STACK_PARAGRAPH_END),
  };
}

// Notes in creation order: a short multiline note, an unbroken token wider than
// the body, then a tall multiline note taller than the body.
export const NOTE_STACK_NOTES = [
  "stack-A-line-01\nstack-A-line-02\nSTACK-A-END",
  `${"Y".repeat(240)}\nSTACK-B-END`,
  Array.from({ length: 90 }, (_, index) => `stack-C-line-${String(index + 1).padStart(2, "0")}`).join("\n") +
    "\nSTACK-C-END",
];
export const NOTE_STACK_END_MARKERS = ["STACK-A-END", "STACK-B-END", "STACK-C-END"];

// The complete snapshot reference each stack note must append, formatted from
// the literal target-paragraph facts alone, never by the production formatter.
function noteStackReference(range, position, note) {
  const context = noteStackContext(range);
  const selection = LONG_SOURCE.slice(range.start, range.end);
  return [
    `A note on \`NOTES.md:${position.start.line}:${position.start.column}-${position.endInclusive.line}:${position.endInclusive.column}\` (captured source positions), selecting “${selection}”: ${JSON.stringify(note)}`,
    `Captured whole-file revision: ${JSON.stringify(LONG_SOURCE_SHA256)}.`,
    `Context before: ${JSON.stringify(context.before)}; after: ${JSON.stringify(context.after)}.`,
    "Verify the quotation and context against current content before relying on these captured coordinates.",
  ].join("\n");
}
export const NOTE_STACK_REFERENCES = NOTE_STACK_RANGES.map((range, index) =>
  noteStackReference(range, NOTE_STACK_POSITIONS[index], NOTE_STACK_NOTES[index]),
);
