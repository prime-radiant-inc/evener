import {
  buildComposerInput,
  type CapturedPassage,
  displayedFingerprint,
  formatAnnotationReference,
  markerPattern,
} from "@evener/appwire-client";
import { describe, expect, it } from "vitest";
import { appendLiteralReference } from "./skillDocument";

const SOURCE = "click [image 1] now\n";

// The append contract as the composer and the send boundary exercise it: the
// formatter lives in the package, the append lives here, and the composed text
// becomes the input the agent receives. This file lives in the app tree because
// it appends through the composer's own skill document; the package's test file
// keeps only the pure formatting coverage.
function passage(): CapturedPassage {
  return {
    evidence: { wholeRevision: "rev [image 1]", fingerprint: displayedFingerprint(SOURCE, false) },
    sourceRange: { start: 0, end: 5 },
    start: { line: 1, column: 1 },
    endInclusive: { line: 1, column: 5 },
    selectedRenderedText: "click [image 1] now",
    contextBefore: "before [image 1] ",
    contextAfter: " after [image 1]",
    mappingId: "fixture-map",
    displayedSnapshotId: "snapshot-A",
  };
}

describe("annotation reference append and send boundary", () => {
  it("keeps captured attachment-marker text literal at the send boundary", () => {
    const note = "keep [image 1] as text";
    const reference = formatAnnotationReference(passage(), "NOTES.md", note);

    // The real append and the real send boundary: the composer stages
    // attachment 1 under its own marker, appends the reference, and then builds
    // the input the agent receives.
    const composed = appendLiteralReference({ text: "[image 1]", skillNames: [] }, reference);
    const input = buildComposerInput(composed.text, [{ marker: 1, mediaType: "image/png", data: "AAAA" }]);
    const text = input.find((item) => item.type === "text")?.text ?? "";
    const staged = "(attached image 1)\n\n";
    expect(text.startsWith(staged)).toBe(true);
    // Only the composer's own staged marker translates; the appended reference
    // reaches the agent byte for byte.
    expect(text.slice(staged.length)).toBe(reference);
    expect(text.match(/\(attached image 1\)/g)).toHaveLength(1);
    expect(input.filter((item) => item.type === "image")).toHaveLength(1);
    // The formatter's own output must contain no attachment marker for the
    // send-time translation to rewrite.
    expect(markerPattern().test(reference)).toBe(false);
  });
});
