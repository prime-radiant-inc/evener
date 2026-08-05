// @vitest-environment node
import { describe, expect, test } from "vitest";
import { imagePlaceholder, normalizeText, queueEntryPreviewText, truncateForDisplay } from "./queueDisplay";

// Ported verbatim from the legacy registry's own normalizeText
// (cmd/serf-hub/assets/pending.js:14-16): collapse all whitespace runs to a
// single space, then trim - the exact shape both the queue-preview
// reconciliation and the pending-turn reconciliation compare against.
describe("normalizeText", () => {
  test("collapses internal whitespace runs to a single space", () => {
    expect(normalizeText("fix   the\n\nlint   errors")).toBe("fix the lint errors");
  });

  test("trims leading and trailing whitespace", () => {
    expect(normalizeText("  hello world  ")).toBe("hello world");
  });

  test("returns empty string for blank/whitespace-only input", () => {
    expect(normalizeText("   \n\t  ")).toBe("");
  });
});

// Ported verbatim from the legacy registry's own imagePlaceholder
// (cmd/serf-hub/assets/pending.js:31-35): the exact synthetic text an
// image-only entry displays, and the same string the daemon's own
// queue-preview is trusted to produce for an image-only queued entry (see
// pendingReconcile.ts's own doc comment on this trust boundary).
describe("imagePlaceholder", () => {
  test("returns empty string for zero images", () => {
    expect(imagePlaceholder(0)).toBe("");
  });

  test("returns the singular form for exactly one image", () => {
    expect(imagePlaceholder(1)).toBe("[image]");
  });

  test("returns the plural form with a count for more than one image", () => {
    expect(imagePlaceholder(2)).toBe("[2 images]");
    expect(imagePlaceholder(8)).toBe("[8 images]");
  });
});

// queueEntryPreviewText composes the two helpers above exactly like the
// legacy registry's own queuePreviewText (pending.js:37-41): normalized text
// wins when present; only an entirely blank/whitespace-only text falls back
// to the image placeholder.
describe("queueEntryPreviewText", () => {
  test("returns the normalized text when non-blank, ignoring image count", () => {
    expect(queueEntryPreviewText("  Fix the lint errors  ", 3)).toBe("Fix the lint errors");
  });

  test("falls back to the image placeholder when text is blank and images are present", () => {
    expect(queueEntryPreviewText("", 1)).toBe("[image]");
    expect(queueEntryPreviewText("   ", 2)).toBe("[2 images]");
  });

  test("returns empty string when both text and image count are absent", () => {
    expect(queueEntryPreviewText("", 0)).toBe("");
  });
});

// truncateForDisplay is the client-side 140-char visual cap layered on top
// of the daemon's own first-line truncation (parity-m5-composer.md §B: "Each
// preview row is truncated to its first line server/daemon-side, then
// additionally visually capped at 140 characters client-side with a
// trailing '…'").
describe("truncateForDisplay", () => {
  test("returns text unchanged when at or under the 140-char default cap", () => {
    const exact = "x".repeat(140);
    expect(truncateForDisplay(exact)).toBe(exact);
    expect(truncateForDisplay("short")).toBe("short");
  });

  test("caps text over 140 chars to 140 chars plus a trailing ellipsis", () => {
    const long = "y".repeat(141);
    const result = truncateForDisplay(long);
    expect(result).toBe(`${"y".repeat(140)}…`);
    expect(result.length).toBe(141);
  });

  test("accepts a custom max length", () => {
    expect(truncateForDisplay("hello world", 5)).toBe("hello…");
  });
});
