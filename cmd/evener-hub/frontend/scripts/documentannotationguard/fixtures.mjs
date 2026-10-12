// Literal, test-only fixtures for the retained full-stack annotation journey.
// Nothing in this file imports, calls or derives from the production mapper,
// renderer, context helper or draft formatter: these values are copied verbatim
// from the approved independent-literals record so a driver assertion cannot be
// satisfied by the code under test agreeing with itself.
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
  'A note on `NOTES.md:1:19-1:22` (captured source positions), selecting “same”: "Keep this scope."\n' +
  'Captured whole-file revision: "4808d04cf747af87429a6d65d3c229bba77e2ce07a9145e520400d9fdebff0a6".\n' +
  'Context before: "First occurrence: "; after: "".\n' +
  "Verify the quotation and context against current content before relying on these captured coordinates.";

export const JOURNEY_APPENDED = `Existing draft\n\n${JOURNEY_REFERENCE}`;

// The literal note text the annotation Send types into the comment box.
export const NOTE = "Keep this scope.";
