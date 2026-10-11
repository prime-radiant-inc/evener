// Independent fixture input for the small annotation component browser cases.
//
// These literals define the ONE thing the runner may treat as expected without
// consulting product code: the fixture source text, the literal source ranges
// the saved annotations cover, and the literal note/reference strings. Offsets
// are derived from the fixture source by `indexOf`, never from a product
// mapper, so the expectations stay independent of production rendering.
//
// Every value here is consumed by scripts/documentannotationguard/components.mjs.
// The guard page (src/dev/documentannotationguard-entry.tsx) mounts the same
// source strings; the runner asserts the page rendered THIS literal text, so a
// drift between the mount and the fixture fails loudly instead of silently.

export function rangeOf(source, selected) {
  const start = source.indexOf(selected);
  if (start < 0) throw new Error(`fixture passage not found in source: ${JSON.stringify(selected)}`);
  return { start, end: start + selected.length };
}

// Markdown renders each paragraph as its own text node; the runner locates the
// rendered passage by the paragraph's own literal text, so the expected ranges
// below are stated in first-paragraph coordinates (identical to source
// coordinates for the first paragraph).
export function firstParagraph(source) {
  return source.split("\n\n")[0];
}
export function nthParagraph(source, index) {
  // A paragraph that is not last keeps the section's trailing newline in the
  // split, but markdown renders no trailing newline in the paragraph's text
  // node, so strip it before the runner searches for the node.
  return source.split("\n\n")[index].replace(/\n+$/, "");
}

// --- case 1: pane-only width change -----------------------------------------
// The first paragraph is long enough to fit on one line in a wide pane and to
// wrap onto more lines in a narrow one. The annotated passage is a middle
// slice; the control word lives in the second paragraph, far from any band, so
// it is a valid unpainted reference pixel. The control range is the WHOLE
// second paragraph, giving a stable visible-glyph reference.
export const PANE_SOURCE =
  "Alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu pi rho.\n\n" +
  "Second paragraph keeps unrelated words entirely separate from the band.\n";
export const PANE_FIRST = firstParagraph(PANE_SOURCE);
export const PANE_SECOND = nthParagraph(PANE_SOURCE, 1);
export const PANE_SELECTED = "gamma delta epsilon zeta eta";
export const PANE_RANGE = rangeOf(PANE_FIRST, PANE_SELECTED);
export const PANE_CONTROL_RANGE = { start: 0, end: PANE_SECOND.length };

// --- case 2: nested reference wheel ------------------------------------------
export const NESTED_TARGET = "Nested target passage words here";
export const NESTED_SOURCE =
  `${NESTED_TARGET} and trailing words on the same line.\n\n` +
  Array.from(
    { length: 26 },
    (_, index) => `Filler paragraph ${index + 1} keeps the document body long enough to scroll on its own.`,
  ).join("\n\n") +
  "\n";
export const NESTED_FIRST = firstParagraph(NESTED_SOURCE);
export const NESTED_RANGE = rangeOf(NESTED_FIRST, NESTED_TARGET);
// Three DIFFERENT overlapping ranges over the touched passage (offset/narrowed
// differently), so each pending reference must carry its OWN range/selected
// text/locator; the runner never reuses the first one for the others.
export const NESTED_RANGES = [
  NESTED_RANGE,
  { start: NESTED_RANGE.start + 1, end: NESTED_RANGE.end - 1 },
  { start: NESTED_RANGE.start + 2, end: NESTED_RANGE.end - 2 },
];
// The first pending note is deliberately long: its copyable reference must
// exceed the reference element's own 12lh cap so the nested element is itself
// scrollable and can consume a wheel direction.
const LONG_NESTED_NOTE = `${Array.from(
  { length: 16 },
  (_, index) => `Long nested reference line ${index + 1} keeps the pending reference taller than its own scroll cap.`,
).join("\n")}`;
// Three overlapping saved operations over the touched passage, all pending, so
// the revealed stack carries three literal notes and three copyable references.
export const NESTED_NOTES = [
  LONG_NESTED_NOTE,
  Array.from(
    { length: 14 },
    (_, index) => `Second overlapping note line ${index + 1} adds to the revealed stack height.`,
  ).join("\n"),
  Array.from(
    { length: 14 },
    (_, index) => `Third overlapping note line ${index + 1} adds to the revealed stack height.`,
  ).join("\n"),
];
export const NESTED_PATH = "NOTES.md";
// The independent per-annotation reference facts. `locator` is computed here
// from the fixture range alone (line 1, columns are 1-based; a range end is
// exclusive so the inclusive end column equals the end offset). No product
// formatter is imported.
export const NESTED_REFERENCES = NESTED_RANGES.map((range, index) => ({
  index,
  range,
  note: NESTED_NOTES[index],
  selected: NESTED_FIRST.slice(range.start, range.end),
  locator: `${NESTED_PATH}:1:${range.start + 1}-1:${range.end}`,
}));

// --- case 3/4/5: selection, Shift, Copy --------------------------------------
export const SELECTION_SOURCE =
  "Selection alpha beta gamma delta epsilon zeta theta.\n\nSecond paragraph plain text for the selection case.\n";
export const SELECTION_FIRST = firstParagraph(SELECTION_SOURCE);
export const SELECTION_SELECTED = "alpha beta gamma delta";
export const SELECTION_RANGE = rangeOf(SELECTION_FIRST, SELECTION_SELECTED);
// The saved band covers a middle slice the drag crosses, so a selection that
// runs across the saved highlight is exercised.
export const SELECTION_BAND = rangeOf(SELECTION_FIRST, "beta gamma");
export const SELECTION_NOTE = "Saved band crossed by the selection.";
export const SELECTION_SHIFT_SELECTED = "alpha beta";
export const SELECTION_SHIFT_RANGE = rangeOf(SELECTION_FIRST, SELECTION_SHIFT_SELECTED);

// --- case 6: overlap and access ----------------------------------------------
// The passage paragraph leads; filler after it makes the pane body genuinely
// scrollable, so a reveal that jumped the reading position would change the
// owner's scrollTop from its 0 baseline and be detected.
export const OVERLAP_SOURCE =
  "Overlap alpha beta gamma delta epsilon passage.\n\n" +
  Array.from(
    { length: 18 },
    (_, index) => `Overlap filler paragraph ${index + 1} keeps the reading body scrollable under the fixed viewport.`,
  ).join("\n\n") +
  "\n";
export const OVERLAP_FIRST = firstParagraph(OVERLAP_SOURCE);
// An unannotated word in the same production pane is the neutral touch control.
// Its equivalent emulated tap establishes the browser's ordinary focus change;
// the annotation transition must not cause a different active element.
export const OVERLAP_NEUTRAL = "Overlap";
export const OVERLAP_NEUTRAL_RANGE = rangeOf(OVERLAP_FIRST, OVERLAP_NEUTRAL);
export const OVERLAP_TARGET = "alpha beta gamma delta";
export const OVERLAP_RANGE = rangeOf(OVERLAP_FIRST, OVERLAP_TARGET);
export const OVERLAP_NOTES = ["Overlapping note one.", "Overlapping note two."];

// --- case 7: visible comment controls and long/wrapped note -------------------
export const COMMENT_SOURCE =
  "Comment control source passage with enough words to stay visible.\n\nAnother paragraph.\n";
export const COMMENT_PASSAGE =
  "This capture passage is deliberately long so the comment box has to wrap it instead of growing a horizontal scrollbar.";
export const COMMENT_NOTE =
  "A deliberately long saved note.\nIt has several lines.\nEach line must stay literally preserved in the editor.";
// A rendered SAVED note (distinct from the editor's draft): long, multi-line and
// carrying one very long unbroken token, so both wrapping and height are
// exercised on real note content.
export const COMMENT_SAVED_NOTE = [
  "A long saved note that must wrap inside the revealed stack.",
  `One unbroken token: ${"W".repeat(120)}`,
  "A fourth line.",
  "A fifth line so the note is tall as well as wide.",
].join("\n");
export const COMMENT_SAVED_RANGE = rangeOf(COMMENT_SOURCE, "source passage");
