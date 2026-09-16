import { expect, test } from "vitest";
import { segmentEntityIds } from "./entitySegments";
import { SUMMARY_ENTITY_JOB } from "./entityView.testFixture";

/** The job id the tests embed in URLs and plain text: one the product itself
 * emits, from the shared fixture. */
const ID = SUMMARY_ENTITY_JOB;

/** Renders segments readably: ids as <id>, text runs as themselves. */
function seg(text: string, protect?: { start: number; end: number }): string[] {
  return segmentEntityIds(text, protect).map((s) => (s.kind === "entity" ? `<${s.id}>` : s.text));
}

test("without protection, an id inside a URL segments like any other id", () => {
  expect(seg(`Read https://x/jobs/${ID}/log`)).toEqual(["Read https://x/jobs/", `<${ID}>`, "/log"]);
});

test("an id overlapping the protected span stays part of the surrounding text run", () => {
  // The span is a slice strictly inside the id: skipping the detected id (not
  // truncating it at the span's edge) is what keeps the span's text intact.
  const idStart = `Read `.length;
  const text = `Read ${ID} done`;
  const protect = { start: idStart + 2, end: idStart + 10 };
  expect(seg(text, protect)).toEqual([text]);
});

test("a span covering a whole URL keeps the URL one text segment", () => {
  const url = `https://x/jobs/${ID}/log`;
  const text = `Read ${url} · 200`;
  const start = text.indexOf(url);
  expect(segmentEntityIds(text, { start, end: start + url.length })).toEqual([{ kind: "text", text }]);
});

test("ids adjacent to the protected span still segment: touching edges is not overlap", () => {
  // Same id twice; only the first occurrence's span is protected, so the
  // first stays text and the second is still detected.
  const text = `${ID} and ${ID}`;
  expect(seg(text, { start: 0, end: ID.length })).toEqual([`${ID} and `, `<${ID}>`]);
});
