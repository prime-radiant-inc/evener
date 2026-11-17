import type { ThreadModel } from "@evener/appwire-client";
import * as appwire from "@evener/appwire-client";
import * as docContent from "@evener/appwire-client/docContent";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import { AgentMarkdown } from "./AgentMarkdown";
import * as cacheModule from "./analysisCache";

// What this file pins, from issue #6455: the enhancement pass AgentMarkdown
// runs after each streamed delta must produce exactly the DOM one full pass
// over the same text produces, and it must not re-derive analysis for text
// that did not change.
//
// Why the second half counts analysis rather than nodes: React replaces the
// whole rendered subtree whenever the sanitized HTML changes, so after a delta
// the root <p> and its text node are both new objects and every reference has
// to be wrapped in the fresh DOM again. Nothing about the decoration is
// incremental. What a delta does not need is a fresh SCAN of the text it did
// not touch, and that scan is the O(nodes) work the issue names. The equality
// tests pin the contract and pass on the pre-change code; the call-count tests
// are the half that fails before the change.

const SESSION_ID = "02wMz5Txv1C3Hut0M8GCeB";
const REF = `local:${SESSION_ID}`;

function thread(): ThreadModel {
  return { ref: REF, cwd: "/workspace/project", artifactsDir: "", scratchDir: "" } as unknown as ThreadModel;
}

// Every shape the two passes decorate, one per paragraph, so a delta that
// grows the last paragraph leaves the rest untouched: prose filenames with
// trailing punctuation, entity ids in prose, an inline code reference, and a
// markdown link.
const PARAGRAPHS = [
  "The importer reads docs/report.md and writes docs/out.json.",
  "The runner names job_02wMz5TxvEMoJEDTDGOTil_000000000123 in prose.",
  "A fenced path `docs/code.md` counts as a whole-token code reference.",
  "The delegate dlg_02wMz5TxvEMoJEDTDGOTil owns the child transcript.",
  "A markdown link to [the spec](docs/spec.md) activates in place.",
  "Trailing punctuation docs/tail.md. stays outside the reference.",
  "A short closing line names no file at all.",
];

const SETTLED = PARAGRAPHS.join("\n\n");
const TAIL = "The growing paragraph opens docs/streamed.md and closes here.";

function view(source: string, live: boolean) {
  return (
    <TranscriptRenderProvider thread={thread()} sourcePaneId="pane-1">
      <AgentMarkdown source={source} live={live} />
    </TranscriptRenderProvider>
  );
}

// The decoration's own markers: entity hosts, and anchors the file pass turned
// into session-scoped document destinations (authored markdown links plus
// filenames found in prose).
function decorationCounts(container: HTMLElement) {
  const docLinks = [...container.querySelectorAll("a[href]")].filter((link) => {
    const href = link.getAttribute("href") ?? "";
    return new URL(href, "https://hub.example").pathname.startsWith("/doc/");
  });
  return { entityHosts: container.querySelectorAll("[data-entity-host]").length, docLinks: docLinks.length };
}

// A container whose decoration is empty would satisfy every equality below
// for the wrong reason, so the fixtures pin that there is work to compare.
function expectDecorated(container: HTMLElement) {
  const counts = decorationCounts(container);
  expect(counts.entityHosts).toBeGreaterThan(1);
  expect(counts.docLinks).toBeGreaterThan(1);
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

test("every streamed frame's decoration equals one full pass over that frame's text", () => {
  const cuts: number[] = [];
  for (let length = 40; length < TAIL.length; length += 17) cuts.push(length);
  cuts.push(TAIL.length);

  const streamed = render(view(`${SETTLED}\n\n${TAIL.slice(0, cuts[0] ?? 0)}`, true));
  try {
    for (const cut of cuts.slice(1)) {
      act(() => {
        streamed.rerender(view(`${SETTLED}\n\n${TAIL.slice(0, cut)}`, true));
      });
      // A fresh mount is one full pass over exactly this text: no prior frame,
      // no reused analysis. The streamed instance must match it byte for byte.
      const fresh = render(view(`${SETTLED}\n\n${TAIL.slice(0, cut)}`, true));
      const streamedHtml = streamed.container.innerHTML;
      const freshHtml = fresh.container.innerHTML;
      fresh.unmount();
      expect(streamedHtml).toBe(freshHtml);
    }
    expectDecorated(streamed.container);
  } finally {
    streamed.unmount();
  }
});

test("the settled render after the stream equals one full settled render", () => {
  const streamed = render(view(`${SETTLED}\n\n${TAIL.slice(0, 10)}`, true));
  try {
    for (let cut = 10; cut < TAIL.length; cut += 13) {
      act(() => {
        streamed.rerender(view(`${SETTLED}\n\n${TAIL.slice(0, cut)}`, true));
      });
    }
    act(() => {
      streamed.rerender(view(`${SETTLED}\n\n${TAIL}`, false));
    });
    const settled = render(view(`${SETTLED}\n\n${TAIL}`, false));
    const streamedHtml = streamed.container.innerHTML;
    const settledHtml = settled.container.innerHTML;
    settled.unmount();
    expect(streamedHtml).toBe(settledHtml);
    expectDecorated(streamed.container);
  } finally {
    streamed.unmount();
  }
});

test("an edit to an earlier paragraph equals one full pass over the edited text", () => {
  const rendered = render(view(SETTLED, false));
  try {
    const edited = SETTLED.replace(
      "The importer reads docs/report.md",
      "The importer reads docs/rewritten.md and docs/second.md",
    );
    act(() => {
      rendered.rerender(view(edited, false));
    });
    const full = render(view(edited, false));
    const renderedHtml = rendered.container.innerHTML;
    const fullHtml = full.container.innerHTML;
    full.unmount();
    expect(renderedHtml).toBe(fullHtml);
    expectDecorated(rendered.container);
  } finally {
    rendered.unmount();
  }
});

test("a plain re-render of unchanged text equals the first render", () => {
  const first = render(view(SETTLED, false));
  try {
    const firstHtml = first.container.innerHTML;
    const firstCounts = decorationCounts(first.container);
    act(() => {
      first.rerender(view(SETTLED, false));
    });
    expect(first.container.innerHTML).toBe(firstHtml);
    expect(decorationCounts(first.container)).toEqual(firstCounts);
    expectDecorated(first.container);
  } finally {
    first.unmount();
  }
});

// Issue #6603: the cache earns its memory only across the passes of a stream.
// Settling remounts AgentMarkdown (AgentMessageItem wraps the live branch in
// .stream and the settled one without it), so a settled instance fills its
// cache once and then answers its own pass only - while holding every block's
// text and its span arrays for as long as the message stays mounted. A settled
// message therefore keeps no cache at all, and a live one keeps its single
// cache: the streaming path's reuse must survive the fix.
test("a settled message creates no analysis cache", () => {
  const create = vi.spyOn(cacheModule, "createAnalysisCache");
  const settled = render(view(SETTLED, false));
  try {
    expect(create).not.toHaveBeenCalled();
    // A cache that was never created must not have cost the pass its work.
    expectDecorated(settled.container);
  } finally {
    settled.unmount();
  }
});

test("a live message creates one cache and re-renders without another", () => {
  const create = vi.spyOn(cacheModule, "createAnalysisCache");
  const streamed = render(view(`${SETTLED}\n\n${TAIL.slice(0, 12)}`, true));
  try {
    expect(create).toHaveBeenCalledTimes(1);
    create.mockClear();
    act(() => {
      streamed.rerender(view(`${SETTLED}\n\n${TAIL.slice(0, 24)}`, true));
    });
    act(() => {
      streamed.rerender(view(`${SETTLED}\n\n${TAIL.slice(0, 36)}`, true));
    });
    expect(create).not.toHaveBeenCalled();
  } finally {
    streamed.unmount();
  }
});

// The other half of #6603: an instance that streamed and then settled must not
// carry its stream's cache into the rest of its settled life. Observable as
// work, not as a field: an edit that arrives after the settle finds nothing
// cached, so the pass over it scans every paragraph instead of answering the
// unchanged ones from the stream. The stream's own final pass is allowed to
// answer from the cache it filled - the release is what the edit sees.
test("settling releases the stream's cache, so a later settled edit retains nothing to reuse", () => {
  const streamed = render(view(`${SETTLED}\n\n${TAIL.slice(0, 12)}`, true));
  try {
    act(() => {
      streamed.rerender(view(`${SETTLED}\n\n${TAIL}`, true));
    });
    // Settle as a separate commit, then edit: two settled renders.
    act(() => {
      streamed.rerender(view(`${SETTLED}\n\n${TAIL}`, false));
    });
    const spy = spyOnAnalysis();
    const edited = SETTLED.replace("docs/report.md", "docs/report.md plus docs/added.md");
    act(() => {
      streamed.rerender(view(`${edited}\n\n${TAIL}`, false));
    });
    expect(spy.fileSpans.mock.calls.length).toBeGreaterThanOrEqual(PARAGRAPHS.length);
    expectDecorated(streamed.container);
  } finally {
    streamed.unmount();
  }
});

// The work-reduction seam: the shared analysis functions themselves, called
// through the modules the production code imports. Counting them counts
// re-derivation of what a block of text names, which is the per-delta scan,
// not wall-clock time.
function spyOnAnalysis() {
  return {
    fileSpans: vi.spyOn(docContent, "findFileReferences"),
    entityIds: vi.spyOn(appwire, "findEntityIds"),
  };
}

// Issue #6644: nulling analysisRef stops later REUSE, but the settle passes
// were still handed the stream's cache, and enhanceFileReferences /
// enhanceEntityText keep that argument in their cleanup closures. Measured
// with --expose-gc, holding a WeakRef to the cache across an in-place settle
// and forcing gc while the message stayed mounted: on the pre-change code it
// was collected 0/6 times after the settle and still 0/6 after two further
// settled renders, while a control that unmounted first collected 6/6. The fix
// hands the passes no cache on a non-live render, so no closure of the settle
// commit's effect captures it; it is then collected at the next commit. The
// price is re-deriving the settle pass's analysis once, exactly what a freshly
// mounted settled message does. Observable, like #6603 above, as work: the
// settle commit must scan every paragraph instead of answering the stream's
// cache.
test("an in-place settle hands the enhancement passes no cache, so nothing retains it", () => {
  const streamed = render(view(`${SETTLED}\n\n${TAIL.slice(0, 12)}`, true));
  try {
    act(() => {
      streamed.rerender(view(`${SETTLED}\n\n${TAIL}`, true));
    });
    const spy = spyOnAnalysis();
    act(() => {
      streamed.rerender(view(`${SETTLED}\n\n${TAIL}`, false));
    });
    // Every settled paragraph is re-derived on the settle commit: a pass that
    // answered one of them from the stream's cache would be below this.
    expect(spy.fileSpans.mock.calls.length).toBeGreaterThanOrEqual(PARAGRAPHS.length);
    expect(spy.entityIds.mock.calls.length).toBeGreaterThanOrEqual(PARAGRAPHS.length);
    expectDecorated(streamed.container);
  } finally {
    streamed.unmount();
  }
});

test("a delta re-derives analysis only for the text it changed", () => {
  const spy = spyOnAnalysis();
  const streamed = render(view(SETTLED, true));
  try {
    // The opening frame is a whole-document pass: every paragraph is new text,
    // so its analysis is expected. That count also pins that the fixture holds
    // a document-sized amount of work rather than one block's.
    expect(spy.fileSpans.mock.calls.length).toBeGreaterThanOrEqual(PARAGRAPHS.length);
    expect(spy.entityIds.mock.calls.length).toBeGreaterThanOrEqual(PARAGRAPHS.length);
    spy.fileSpans.mockClear();
    spy.entityIds.mockClear();

    let deltas = 0;
    for (let cut = 1; cut <= TAIL.length; cut += 9) {
      act(() => {
        streamed.rerender(view(`${SETTLED}\n\n${TAIL.slice(0, cut)}`, true));
      });
      deltas += 1;
      const files = spy.fileSpans.mock.calls.length;
      const entities = spy.entityIds.mock.calls.length;
      spy.fileSpans.mockClear();
      spy.entityIds.mockClear();
      // Each delta still decorates its frame - the equality tests pin that the
      // result is identical to a full pass - but the analysis behind it must
      // cover the one changed block, not all seven settled paragraphs.
      expect({ delta: deltas, files }).toMatchObject({ files: expect.any(Number) });
      expect(files).toBeLessThanOrEqual(2);
      expect(entities).toBeLessThanOrEqual(2);
      expect(files + entities).toBeGreaterThanOrEqual(1);
    }
    expect(deltas).toBeGreaterThan(5);
  } finally {
    streamed.unmount();
  }
});

// The reuse lives on the live path (issue #6603 kept it there and dropped it
// for settled messages), so the edit case is pinned on a live render.
test("an edit to a live message re-derives analysis only for the block it changed", () => {
  const rendered = render(view(`${SETTLED}\n\n${TAIL}`, true));
  try {
    const spy = spyOnAnalysis();
    const edited = SETTLED.replace("docs/report.md", "docs/report.md plus docs/added.md");
    act(() => {
      rendered.rerender(view(`${edited}\n\n${TAIL}`, true));
    });
    expect(spy.fileSpans.mock.calls.length).toBeLessThanOrEqual(2);
    expect(spy.entityIds.mock.calls.length).toBeLessThanOrEqual(2);
  } finally {
    rendered.unmount();
  }
});

// The trap this kind of fix usually hits: a delta that splits a token could
// let a reused analysis decorate half a reference. Every frame is compared
// against a fresh full pass over the same partial text, so a half-token
// link, a link from the previous frame that no longer applies, or a missing
// link at the frame that completes the token all fail here.
test.each([
  ["a prose filename", "see docs/rep", ["ort", ".md", " and more"]],
  ["an inline code path", "run `docs/co", ["de", ".md", "` now"]],
  ["a markdown link destination", "[spec](docs/sp", ["ec", ".md", ")", " tail"]],
  ["an entity id", "job_02wMz5TxvEMoJEDTDGOTil_00000", ["000", "0123", " done"]],
  ["a delegate id", "dlg_02wMz5TxvEMoJED", ["TDGOTil", " end"]],
])("a delta splitting %s decorates only the complete token, never half of it", (_label, start, chunks) => {
  let source = start;
  const streamed = render(view(source, true));
  try {
    for (const chunk of chunks) {
      source += chunk;
      act(() => {
        streamed.rerender(view(source, true));
      });
      const fresh = render(view(source, true));
      const streamedHtml = streamed.container.innerHTML;
      const freshHtml = fresh.container.innerHTML;
      fresh.unmount();
      expect(streamedHtml).toBe(freshHtml);
    }
    // The completed frame really did decorate: split handling that simply
    // stopped enhancing would also pass every equality above.
    const counts = decorationCounts(streamed.container);
    expect(counts.entityHosts + counts.docLinks).toBeGreaterThan(0);
  } finally {
    streamed.unmount();
  }
});

// Streaming is the point of the change, so the enhancement must not lag the
// final text when the stream ends: the last delta's own frame already carries
// the complete decoration, with no settle frame to catch up.
test("the frame that completes the stream is already fully decorated", () => {
  const cuts = [4, 11, 23, 34, 47, 58, 71, TAIL.length];
  const streamed = render(view(`${SETTLED}\n\n${TAIL.slice(0, cuts[0] ?? 0)}`, true));
  try {
    for (const cut of cuts.slice(1)) {
      act(() => {
        streamed.rerender(view(`${SETTLED}\n\n${TAIL.slice(0, cut)}`, true));
      });
    }
    // No settle render: compare the final live frame itself.
    const fresh = render(view(`${SETTLED}\n\n${TAIL}`, true));
    const streamedHtml = streamed.container.innerHTML;
    const freshHtml = fresh.container.innerHTML;
    fresh.unmount();
    expect(streamedHtml).toBe(freshHtml);
    // The final paragraph's filename is a link on that last live frame, not
    // one delta later.
    const streamedTail = [...streamed.container.querySelectorAll("a[href]")].map(
      (link) => link.getAttribute("href") ?? "",
    );
    expect(streamedTail.some((href) => href.includes(encodeURIComponent("docs/streamed.md")))).toBe(true);
  } finally {
    streamed.unmount();
  }
});
