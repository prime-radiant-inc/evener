// Tests-first checkpoint for highlight-fragment measurement (Task 5).
//
// `./documentHighlights` is the production module this checkpoint precedes, so
// the static import below deliberately does NOT resolve yet: the file is a
// collection/setup failure until the source grant lands, and `tsc` reports the
// missing module instead of this file hiding it behind a stub. The approved
// interfaces exercised here are `HighlightTarget`, `HighlightFragment` and
// `measureDocumentHighlights(map, targets, clip)`.
//
// jsdom has no layout, so the shared geometry seam supplies ONLY the Range
// rectangles; the document map, its verified fragments and the production
// clipping/intersection logic all run for real. Clip and fragment expectations
// are literal, independent of the seam's inputs.
import { render } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { DocumentMarkdown } from "../../widgets/markdown/DocumentMarkdown";
import type { BoundDocumentMap } from "../../widgets/markdown/documentSourceMap";
import { type HighlightFragment, measureDocumentHighlights } from "./documentHighlights";
import { FIRST, RICH_SOURCE, SECOND, SOURCE } from "./testing/annotationFixtures";
import { DocumentGeometrySeam, type TestRect, testDomRect } from "./testing/documentGeometryTestUtils";

interface MountedMap {
  view: ReturnType<typeof render>;
  map: BoundDocumentMap;
  root: HTMLElement;
}

function mountMap(source: string): MountedMap {
  let bound: BoundDocumentMap | null = null;
  const view = render(
    createElement(DocumentMarkdown, {
      source,
      identity: { displayedSnapshotId: "snap-1", mappingId: "map-1" },
      onMap: (value: BoundDocumentMap | null) => {
        bound = value;
      },
    }),
  );
  if (!bound) throw new Error("document not bound");
  const map = bound as BoundDocumentMap;
  const root = view.container.firstElementChild;
  if (!(root instanceof HTMLElement)) throw new Error("missing document root");
  return { view, map, root };
}

const clip = (rect: TestRect): DOMRectReadOnly => testDomRect(rect);

// A fresh seam per test: installed in beforeEach, restored exactly in afterEach.
const geometry = new DocumentGeometrySeam();

beforeEach(() => {
  geometry.install();
});
afterEach(() => {
  geometry.restore();
});

// Flatten fragments to plain edges for an exact, class-free comparison.
function edges(fragments: readonly HighlightFragment[]) {
  return fragments.map((fragment) => ({
    operationId: fragment.operationId,
    left: fragment.rect.left,
    top: fragment.rect.top,
    right: fragment.rect.right,
    bottom: fragment.rect.bottom,
  }));
}

test("keeps a partial-word range to its exact fragment, never the whole block", () => {
  const { view, map } = mountMap("before **partial** after");
  geometry.configure(map, { start: 10, end: 13 }, [[{ left: 12.5, top: 3, right: 24.25, bottom: 13.75 }]]);

  const fragments = measureDocumentHighlights(
    map,
    [{ operationId: "op-1", sourceRange: { start: 10, end: 13 } }],
    clip({ left: 0, top: 0, right: 100, bottom: 100 }),
  );

  expect(edges(fragments)).toEqual([{ operationId: "op-1", left: 12.5, top: 3, right: 24.25, bottom: 13.75 }]);
  view.unmount();
});

test("returns one fragment per verified range for fragmented multi-block text", () => {
  const { view, map } = mountMap(SOURCE);
  geometry.configure(map, { start: FIRST.start, end: SECOND.end }, [
    [{ left: 1, top: 1, right: 11, bottom: 9 }],
    [{ left: 1, top: 20, right: 11, bottom: 28 }],
  ]);

  const fragments = measureDocumentHighlights(
    map,
    [{ operationId: "op-multi", sourceRange: { start: FIRST.start, end: SECOND.end } }],
    clip({ left: 0, top: 0, right: 100, bottom: 100 }),
  );

  expect(edges(fragments)).toEqual([
    { operationId: "op-multi", left: 1, top: 1, right: 11, bottom: 9 },
    { operationId: "op-multi", left: 1, top: 20, right: 11, bottom: 28 },
  ]);
  view.unmount();
});

test("measures table and code text from their own verified ranges", () => {
  const { view, map } = mountMap(RICH_SOURCE);
  const table = { start: 135, end: 139 };
  const code = { start: 159, end: 163 };
  geometry.configure(map, table, [[{ left: 5, top: 40, right: 15, bottom: 50 }]]);
  geometry.configure(map, code, [[{ left: 5, top: 60, right: 15, bottom: 70 }]]);

  const fragments = measureDocumentHighlights(
    map,
    [
      { operationId: "op-table", sourceRange: table },
      { operationId: "op-code", sourceRange: code },
    ],
    clip({ left: 0, top: 0, right: 100, bottom: 100 }),
  );

  expect(edges(fragments)).toEqual([
    { operationId: "op-table", left: 5, top: 40, right: 15, bottom: 50 },
    { operationId: "op-code", left: 5, top: 60, right: 15, bottom: 70 },
  ]);
  view.unmount();
});

test("keeps every overlapping operation id with its own exact geometry", () => {
  const { view, map } = mountMap(SOURCE);
  geometry.configure(map, FIRST, [[{ left: 1, top: 1, right: 11, bottom: 9 }]]);
  geometry.configure(map, { start: FIRST.start, end: SECOND.end }, [
    [{ left: 1, top: 1, right: 11, bottom: 9 }],
    [{ left: 1, top: 20, right: 11, bottom: 28 }],
  ]);

  const fragments = measureDocumentHighlights(
    map,
    [
      { operationId: "op-a", sourceRange: FIRST },
      { operationId: "op-b", sourceRange: { start: FIRST.start, end: SECOND.end } },
    ],
    clip({ left: 0, top: 0, right: 100, bottom: 100 }),
  );

  const byOperation = new Map<string, HighlightFragment[]>();
  for (const fragment of fragments) {
    const list = byOperation.get(fragment.operationId) ?? [];
    list.push(fragment);
    byOperation.set(fragment.operationId, list);
  }
  expect(byOperation.get("op-a")).toHaveLength(1);
  expect(byOperation.get("op-b")).toHaveLength(2);
  expect(edges(byOperation.get("op-a") ?? [])).toEqual([
    { operationId: "op-a", left: 1, top: 1, right: 11, bottom: 9 },
  ]);
  expect(edges(byOperation.get("op-b") ?? [])).toEqual([
    { operationId: "op-b", left: 1, top: 1, right: 11, bottom: 9 },
    { operationId: "op-b", left: 1, top: 20, right: 11, bottom: 28 },
  ]);
  view.unmount();
});

test("returns no fragments for stale, disposed or unavailable ranges", () => {
  const { view, map } = mountMap("![same](x)\n\n```ts\nsame\n```\n");
  const image = { start: 2, end: 6 };
  // No verified source fragment for the removed image label.
  expect(
    measureDocumentHighlights(
      map,
      [{ operationId: "op-image", sourceRange: image }],
      clip({ left: 0, top: 0, right: 100, bottom: 100 }),
    ),
  ).toEqual([]);
  // Out-of-bounds and empty ranges are rejected by the map itself.
  expect(
    measureDocumentHighlights(
      map,
      [{ operationId: "op-oob", sourceRange: { start: 100, end: 200 } }],
      clip({ left: 0, top: 0, right: 100, bottom: 100 }),
    ),
  ).toEqual([]);
  expect(
    measureDocumentHighlights(
      map,
      [{ operationId: "op-empty", sourceRange: { start: 5, end: 5 } }],
      clip({ left: 0, top: 0, right: 100, bottom: 100 }),
    ),
  ).toEqual([]);
  // A disposed map reports nothing, even for a range that was measurable.
  map.dispose();
  expect(
    measureDocumentHighlights(
      map,
      [{ operationId: "op-live", sourceRange: { start: 12, end: 16 } }],
      clip({ left: 0, top: 0, right: 100, bottom: 100 }),
    ),
  ).toEqual([]);
  view.unmount();
});

test("clips each rect to the scroll viewport, preserving exact fractional edges", () => {
  const { view, map } = mountMap("before **partial** after");
  const range = { start: 10, end: 13 };
  geometry.configure(map, range, [
    [
      { left: 12.5, top: 3, right: 24.25, bottom: 13.75 }, // fully inside
      { left: 16.5, top: 30, right: 30, bottom: 40 }, // left edge clipped to 17.5
      { left: 120, top: 0, right: 140, bottom: 10 }, // fully outside, dropped
      { left: 100, top: 0, right: 120, bottom: 10 }, // zero-width at the right edge, dropped
      { left: 90, top: 0, right: 110, bottom: 10 }, // positive right-edge sliver, kept
      { left: 0, top: 0, right: 5, bottom: 10 }, // zero-width at the left edge, dropped
    ],
  ]);
  const scrollClip = clip({ left: 17.5, top: 0, right: 100, bottom: 100 });

  const fragments = measureDocumentHighlights(map, [{ operationId: "op-1", sourceRange: range }], scrollClip);

  expect(edges(fragments)).toEqual([
    { operationId: "op-1", left: 17.5, top: 3, right: 24.25, bottom: 13.75 },
    { operationId: "op-1", left: 17.5, top: 30, right: 30, bottom: 40 },
    { operationId: "op-1", left: 90, top: 0, right: 100, bottom: 10 },
  ]);
  view.unmount();
});
