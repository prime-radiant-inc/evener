import * as appwire from "@evener/appwire-client";
import * as docContent from "@evener/appwire-client/docContent";
import { findFileReferences } from "@evener/appwire-client/docContent";
import { expect, test, vi } from "vitest";
import { createAnalysisCache } from "./analysisCache";

// The memo behind AgentMarkdown's per-delta enhancement must be exact: a hit
// has to return what the scan it stands in for would return. These are the
// cases where a wrong key would show up as a changed reference.

const TEXT = "The importer reads docs/report.md and writes docs/out.json.";
const CWD = "/workspace/project";
const OTHER_CWD = "/workspace/elsewhere";

test("a file-span hit equals the scan it replaces", () => {
  const analysis = createAnalysisCache();
  const first = analysis.fileSpans(TEXT, CWD, undefined);
  const second = analysis.fileSpans(TEXT, CWD, undefined);
  expect(first).toEqual(findFileReferences(TEXT, CWD, undefined));
  expect(second).toEqual(findFileReferences(TEXT, CWD, undefined));
  expect(first.length).toBeGreaterThan(0);
});

test("an entity-id hit equals the scan it replaces", () => {
  const id = "job_02wMz5TxvEMoJEDTDGOTil_000000000123";
  const analysis = createAnalysisCache();
  expect(analysis.entityIds(`see ${id} now`)).toEqual(appwire.findEntityIds(`see ${id} now`));
  expect(analysis.entityIds(`see ${id} now`)).toEqual(appwire.findEntityIds(`see ${id} now`));
});

test("the same text under a different cwd is a different answer", () => {
  const analysis = createAnalysisCache();
  const here = analysis.fileSpans(TEXT, CWD, undefined);
  // The memo must not serve the other root's spans for this root's text: an
  // absolute path binds against the working directory it names.
  const absolute = "Read /workspace/project/docs/report.md and /workspace/elsewhere/docs/report.md.";
  expect(analysis.fileSpans(absolute, CWD, undefined)).toEqual(findFileReferences(absolute, CWD, undefined));
  expect(analysis.fileSpans(absolute, OTHER_CWD, undefined)).toEqual(
    findFileReferences(absolute, OTHER_CWD, undefined),
  );
  expect(analysis.fileSpans(absolute, CWD, undefined)).toEqual(findFileReferences(absolute, CWD, undefined));
  expect(here).toEqual(findFileReferences(TEXT, CWD, undefined));
});

test("the same text under different served roots is a different answer", () => {
  const analysis = createAnalysisCache();
  const absolute = "Read /artifacts/docs/report.md now.";
  const withArtifacts = { artifactsDir: "/artifacts" };
  const withScratch = { artifactsDir: "", scratchDir: "/scratch" };
  expect(analysis.fileSpans(absolute, CWD, withArtifacts)).toEqual(findFileReferences(absolute, CWD, withArtifacts));
  expect(analysis.fileSpans(absolute, CWD, withScratch)).toEqual(findFileReferences(absolute, CWD, withScratch));
  expect(analysis.fileSpans(absolute, CWD, withArtifacts)).toEqual(findFileReferences(absolute, CWD, withArtifacts));
});

test("an alias that admits a reference is part of the key", () => {
  const analysis = createAnalysisCache();
  const aliased = "Read /workspace/link/docs/report.md now.";
  const withAlias = { cwdAlias: "/workspace/link" };
  // The alias decides whether this absolute path names a document at all, so a
  // pass run with it and a pass run without it answer differently for
  // identical text and served roots.
  expect(analysis.fileSpans(aliased, CWD, withAlias)).toEqual(findFileReferences(aliased, CWD, withAlias));
  expect(analysis.fileSpans(aliased, CWD, withAlias)).toHaveLength(1);
  expect(analysis.fileSpans(aliased, CWD, undefined)).toEqual(findFileReferences(aliased, CWD, undefined));
  expect(analysis.fileSpans(aliased, CWD, undefined)).toHaveLength(0);
  expect(analysis.fileSpans(aliased, CWD, withAlias)).toHaveLength(1);
});

test("a text that splits a reference analyses as its own text, never a prefix's answer", () => {
  const analysis = createAnalysisCache();
  const complete = "see docs/report.md";
  const partial = "see docs/rep";
  expect(analysis.fileSpans(complete, CWD, undefined)).toEqual(findFileReferences(complete, CWD, undefined));
  // The partial token names no file; a memo keyed on the wrong thing (the
  // block, the paragraph, the message) would hand back the complete answer.
  expect(analysis.fileSpans(partial, CWD, undefined)).toEqual([]);
  expect(analysis.fileSpans(complete, CWD, undefined)).toEqual(findFileReferences(complete, CWD, undefined));
});

test("many distinct blocks still answer correctly past the entry budget", () => {
  const analysis = createAnalysisCache();
  for (let index = 0; index < 700; index += 1) {
    const text = `paragraph ${index} names docs/file-${index}.md`;
    expect(analysis.fileSpans(text, CWD, undefined)).toEqual(findFileReferences(text, CWD, undefined));
  }
  // The most recent entries survive; an evicted one is simply re-analysed.
  const last = "paragraph 699 names docs/file-699.md";
  expect(analysis.fileSpans(last, CWD, undefined)).toEqual(findFileReferences(last, CWD, undefined));
});

test("a long message's settled head keeps its entries while its tail streams", () => {
  const fileSpy = vi.spyOn(docContent, "findFileReferences");
  const analysis = createAnalysisCache();
  // A long answer: 200 settled paragraphs, each naming a file, then a tail
  // that grows a few characters at a time the way a streamed delta does.
  const settled = Array.from({ length: 200 }, (_value, index) => `Paragraph ${index} reads docs/part-${index}.md.`);
  const tail = "The closing paragraph opens docs/closing.md and ends here.";
  for (const paragraph of settled) analysis.fileSpans(paragraph, CWD, undefined);
  for (let length = 1; length <= tail.length; length += 5) {
    analysis.fileSpans(tail.slice(0, length), CWD, undefined);
  }
  // The whole settled head is still cached: re-analysing it for the final
  // frame is the per-delta cost the change removes.
  fileSpy.mockClear();
  for (const paragraph of settled) analysis.fileSpans(paragraph, CWD, undefined);
  expect(fileSpy).not.toHaveBeenCalled();
  fileSpy.mockRestore();
});

// Issue #6602: both memos are keyed per text node or per block, so a long
// message's own working set is several hundred keys - measured over 150 real
// assistant answers, up to 922 distinct entity keys on one answer and 1,607
// for a 400-row x 4-column table. A budget below that evicts each key moments
// before the next pass asks for it again, so every lookup misses and the memo
// costs Map churn on top of the scans it exists to remove.
//
// Every pass asks for its keys in document order, so the pass that follows a
// delta asks for the same keys the previous pass did. 300 distinct texts is the
// working set of a single long answer; the second pass over them must scan
// nothing at all.
test("a 300-distinct-text message answers the pass that follows it with no scans", () => {
  const analysis = createAnalysisCache();
  const fileSpy = vi.spyOn(docContent, "findFileReferences");
  const entitySpy = vi.spyOn(appwire, "findEntityIds");
  const fileTexts = Array.from({ length: 300 }, (_value, index) => `Paragraph ${index} names docs/file-${index}.md`);
  const entityTexts = Array.from(
    { length: 300 },
    (_value, index) => `Paragraph ${index} names job_02wMz5TxvEMoJEDTDGOTil_${String(index).padStart(12, "0")}`,
  );
  for (const text of fileTexts) analysis.fileSpans(text, CWD, undefined);
  for (const text of entityTexts) analysis.entityIds(text);
  fileSpy.mockClear();
  entitySpy.mockClear();
  for (const text of fileTexts) analysis.fileSpans(text, CWD, undefined);
  for (const text of entityTexts) analysis.entityIds(text);
  expect({ fileScans: fileSpy.mock.calls.length, entityScans: entitySpy.mock.calls.length }).toEqual({
    fileScans: 0,
    entityScans: 0,
  });
  fileSpy.mockRestore();
  entitySpy.mockRestore();
});
