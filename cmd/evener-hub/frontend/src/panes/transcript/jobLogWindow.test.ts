// @vitest-environment node

import { parseJobOutputPage } from "@evener/appwire-client";
import { expect, test } from "vitest";
import type { JobLogRow, JobLogWindows } from "./jobLogWindow";
import {
  applyJobLogPage,
  emptyJobLogWindows,
  type JobLogDemandRow,
  jobLogDemandGapIndex,
  jobLogRows,
  jobLogSourceBytes,
  reconcileJobLogBounds,
  retainJobLogReadingWindow,
  selectJobLogDemand,
} from "./jobLogWindow";

function rawPage(offsetBytes: number, source: number[] | Uint8Array, totalBytes: number, retainedStartBytes = 0) {
  const bytes = new Uint8Array(source);
  const page = parseJobOutputPage({
    offsetBytes,
    bytesReturned: bytes.length,
    totalBytes,
    retainedStartBytes,
    encoding: "base64",
    data: btoa(Array.from(bytes, (byte) => String.fromCharCode(byte)).join("")),
  });
  if (!page) throw new Error("invalid literal byte fixture");
  return page;
}

function text(windows: JobLogWindows): string {
  return jobLogRows(windows)
    .flatMap((row) => row.line.map((run) => run.text))
    .join("");
}

test("repairs a split scalar by prepend at original source positions", () => {
  let windows = emptyJobLogWindows();
  windows = applyJobLogPage(windows, rawPage(2, [0xa9, 10], 4), "older", "start");
  expect(text(windows)).toBe("�");
  windows = applyJobLogPage(windows, rawPage(0, [97, 0xc3], 4), "older", "end");
  expect(windows.older?.bytes).toEqual(new Uint8Array([97, 0xc3, 0xa9, 10]));
  expect(text(windows)).toBe("aé");
  expect(jobLogRows(windows)).toMatchObject([{ offsetBytes: 0, endBytes: 4 }]);
});

test("repairs an incomplete scalar by append and ignores matching overlap", () => {
  let windows = emptyJobLogWindows();
  windows = applyJobLogPage(windows, rawPage(20, [65, 0xf0, 0x9f], 23), "live", "end");
  expect(text(windows)).toBe("A�");
  windows = applyJobLogPage(windows, rawPage(22, [0x9f, 0x98, 0x80, 66], 26), "live", "end");
  expect(windows.live?.bytes).toEqual(new Uint8Array([65, 0xf0, 0x9f, 0x98, 0x80, 66]));
  expect(text(windows)).toBe("A😀B");
  expect(jobLogRows(windows)).toMatchObject([{ offsetBytes: 20, endBytes: 26 }]);
});

test("rejects conflicting bytes without mutating useful cached output", () => {
  const windows = applyJobLogPage(emptyJobLogWindows(), rawPage(0, [65, 66], 2), "live", "end");
  expect(() => applyJobLogPage(windows, rawPage(1, [67], 2), "live", "end")).toThrow(/bytes changed/);
  expect(text(windows)).toBe("AB");
  expect(windows.totalBytes).toBe(2);
});

test("rejects a page conflicting with the other window before admission", () => {
  const windows = applyJobLogPage(emptyJobLogWindows(), rawPage(0, [65, 66], 2), "older", "end");
  expect(() => applyJobLogPage(windows, rawPage(1, [67], 2), "live", "end")).toThrow(/bytes changed/);
  expect(windows.live).toBeNull();
  expect(text(windows)).toBe("AB");
});

test("uses byte frontiers when control-only pages have no visible rows", () => {
  const windows = applyJobLogPage(emptyJobLogWindows(), rawPage(0, [27, 91, 51, 49, 109], 10), "older", "end");
  expect(windows.older?.rows).toEqual([]);
  expect(windows.older?.bytes.length).toBe(5);
  expect(selectJobLogDemand(windows, { direction: "forward", boundaryBytes: 5, limitBytes: 10 })).toEqual({
    beforeBytes: 10,
    maxBytes: 5,
  });
});

test("keeps ANSI state independent across an unread gap", () => {
  let windows = emptyJobLogWindows();
  windows = applyJobLogPage(windows, rawPage(0, [27, 91, 51, 49, 59, 49, 109, 65, 10], 101), "older", "end");
  windows = applyJobLogPage(windows, rawPage(100, [66], 101), "live", "end");
  expect(jobLogRows(windows)).toMatchObject([
    { offsetBytes: 0, endBytes: 9, line: [{ text: "A", foreground: { kind: "named", name: "red" }, bold: true }] },
    { offsetBytes: 100, endBytes: 101, line: [{ text: "B", foreground: undefined, bold: false }] },
  ]);
  expect(jobLogSourceBytes(windows)).toBe(10);
});

test("joins overlapping windows once, repairing a split CSI and logical line", () => {
  let windows = emptyJobLogWindows();
  windows = applyJobLogPage(windows, rawPage(0, [27, 91, 51, 49], 9), "older", "end");
  windows = applyJobLogPage(windows, rawPage(3, [49, 109, 65, 66, 67, 10], 9), "live", "end");
  expect(jobLogRows(windows)).toMatchObject([
    { offsetBytes: 0, endBytes: 9, line: [{ text: "ABC", foreground: { kind: "named", name: "red" } }] },
  ]);
  expect(jobLogSourceBytes(windows)).toBe(9);
  expect(windows.older?.bytes.length).toBe(4);
  expect(windows.live?.bytes.length).toBe(6);
});

test("joins adjacent windows before decoding a split Unicode scalar", () => {
  let windows = applyJobLogPage(emptyJobLogWindows(), rawPage(10, [65, 0xc3], 14), "older", "end");
  windows = applyJobLogPage(windows, rawPage(12, [0xa9, 66], 14), "live", "end");
  expect(text(windows)).toBe("AéB");
  expect(jobLogRows(windows)).toMatchObject([{ offsetBytes: 10, endBytes: 14 }]);
});

test("suppresses an OSC payload split across appended pages", () => {
  let windows = applyJobLogPage(emptyJobLogWindows(), rawPage(0, [65, 27, 93, 120, 27], 5), "live", "end");
  windows = applyJobLogPage(windows, rawPage(5, [92, 66, 10], 8), "live", "end");
  expect(text(windows)).toBe("AB");
  expect(jobLogRows(windows)).toMatchObject([{ offsetBytes: 0, endBytes: 8 }]);
});

test("disconnected reads replace only their target and never fabricate a gap", () => {
  let windows = applyJobLogPage(emptyJobLogWindows(), rawPage(0, [65], 1), "older", "end");
  windows = applyJobLogPage(windows, rawPage(10, [66], 11), "live", "end");
  const older = windows.older;
  windows = applyJobLogPage(windows, rawPage(30, [67], 31), "live", "end");
  expect(windows.older).toBe(older);
  expect(windows.live?.offsetBytes).toBe(30);
  expect(text(windows)).toBe("AC");
  expect(jobLogSourceBytes(windows)).toBe(2);
  windows = applyJobLogPage(windows, rawPage(20, [68], 31), "older", "start");
  expect(text(windows)).toBe("DC");
});

test("empty EOF updates bounds without erasing successful output", () => {
  let windows = applyJobLogPage(emptyJobLogWindows(), rawPage(0, [65], 1), "live", "end");
  const live = windows.live;
  windows = applyJobLogPage(windows, rawPage(2, [], 2), "live", "end");
  expect(windows.live).toBe(live);
  expect(windows.totalBytes).toBe(2);
  expect(text(windows)).toBe("A");
});

test("preserves cached bytes below the floor while excluding pruned refetches", () => {
  let windows = applyJobLogPage(emptyJobLogWindows(), rawPage(0, [65, 66], 10), "older", "end");
  const older = windows.older;
  windows = reconcileJobLogBounds(windows, 5, 10);
  expect(windows.older).toBe(older);
  expect(text(windows)).toBe("AB");
  expect(selectJobLogDemand(windows, { direction: "backward", boundaryBytes: 2, limitBytes: 10 })).toBeNull();
  expect(selectJobLogDemand(windows, { direction: "forward", boundaryBytes: 2, limitBytes: 10 })).toEqual({
    beforeBytes: 10,
    maxBytes: 5,
  });
});

test.each([
  [-1, 10],
  [2, 9],
  [1, 11],
  [12, 11],
  [2.5, 11],
  [2, Number.NaN],
])("rejects inconsistent floor %s and total %s", (floor, total) => {
  const windows = reconcileJobLogBounds(emptyJobLogWindows(), 2, 10);
  expect(() => reconcileJobLogBounds(windows, floor, total)).toThrow(/bounds/);
});

test.each([
  ["backward", 70000, 150000, { beforeBytes: 70000, maxBytes: 65536 }],
  ["backward", 500, 150000, { beforeBytes: 500, maxBytes: 400 }],
  ["backward", 200000, 150000, { beforeBytes: 150000, maxBytes: 65536 }],
  ["backward", 100, 150000, null],
  ["forward", 0, 200, { beforeBytes: 200, maxBytes: 100 }],
  ["forward", 1000, 150000, { beforeBytes: 66536, maxBytes: 65536 }],
  ["forward", 149999, 200000, { beforeBytes: 150000, maxBytes: 1 }],
  ["forward", 150000, 150000, null],
] as const)("selects a bounded %s demand from %s to %s", (direction, boundaryBytes, limitBytes, selection) => {
  const windows = reconcileJobLogBounds(emptyJobLogWindows(), 100, 150000);
  expect(selectJobLogDemand(windows, { direction, boundaryBytes, limitBytes })).toEqual(selection);
});

function loadBytes(
  source: Uint8Array,
  offset: number,
  target: "older" | "live",
  keep: "start" | "end",
  windows = emptyJobLogWindows(),
) {
  for (let start = 0; start < source.length; start += 65536) {
    windows = applyJobLogPage(
      windows,
      rawPage(offset + start, source.slice(start, start + 65536), offset + source.length),
      target,
      keep,
    );
  }
  return windows;
}

test("caps both raw windows and preserves ANSI style across known front eviction", () => {
  const source = new Uint8Array(524288 + 65536).fill(65);
  source.set([27, 91, 51, 49, 59, 49, 109]);
  let windows = loadBytes(source, 0, "older", "end");
  expect(windows.older?.bytes.length).toBeLessThanOrEqual(524288);
  expect(windows.older?.offsetBytes).toBeGreaterThan(0);
  expect(text(windows)).toBe("A".repeat(windows.older?.bytes.length ?? 0));
  expect(
    windows.older?.rows.every((row) =>
      row.line.every((run) => run.foreground?.kind === "named" && run.foreground.name === "red" && run.bold),
    ),
  ).toBe(true);
  windows = loadBytes(new Uint8Array(524288 + 65536).fill(66), 2000000, "live", "end", windows);
  expect(windows.live?.bytes.length).toBeLessThanOrEqual(524288);
  expect(jobLogSourceBytes(windows)).toBeLessThanOrEqual(1048576);
  expect(windows.live?.rows.every((row) => row.line.every((run) => run.foreground === undefined && !run.bold))).toBe(
    true,
  );
});

test("backward trimming preserves the start and leaves the discarded end refetchable", () => {
  let windows = loadBytes(new Uint8Array(524288).fill(65), 0, "older", "end");
  windows = applyJobLogPage(windows, rawPage(524288, new Uint8Array(65536).fill(66), 589824), "older", "start");
  expect(windows.older?.offsetBytes).toBe(0);
  expect(windows.older?.bytes.length).toBe(524288);
  expect(text(windows)).toBe("A".repeat(524288));
  expect(selectJobLogDemand(windows, { direction: "forward", boundaryBytes: 524288, limitBytes: 589824 })).toEqual({
    beforeBytes: 589824,
    maxBytes: 65536,
  });
  windows = applyJobLogPage(windows, rawPage(524288, new Uint8Array(65536).fill(66), 589824), "older", "end");
  expect(windows.older?.offsetBytes).toBe(65536);
  expect(text(windows).endsWith("B".repeat(65536))).toBe(true);
  expect(selectJobLogDemand(windows, { direction: "backward", boundaryBytes: 65536, limitBytes: 589824 })).toEqual({
    beforeBytes: 65536,
    maxBytes: 65536,
  });
});

test("scrolling backward prepends bytes and evicts the opposite end", () => {
  let windows = loadBytes(new Uint8Array(524288).fill(66), 65536, "older", "end");
  windows = applyJobLogPage(windows, rawPage(0, new Uint8Array(65536).fill(65), 589824), "older", "start");
  expect(windows.older?.offsetBytes).toBe(0);
  expect(windows.older?.bytes.length).toBe(524288);
  expect(text(windows)).toBe(`${"A".repeat(65536)}${"B".repeat(458752)}`);
  expect(selectJobLogDemand(windows, { direction: "forward", boundaryBytes: 524288, limitBytes: 589824 })).toEqual({
    beforeBytes: 589824,
    maxBytes: 65536,
  });
});

test("counts four-byte scalars against the raw-byte cap", () => {
  const bytes = new Uint8Array(589824);
  for (let offset = 0; offset < bytes.length; offset += 4) bytes.set([0xf0, 0x9f, 0x98, 0x80], offset);
  const windows = loadBytes(bytes, 0, "live", "end");
  expect(windows.live?.offsetBytes).toBe(65536);
  expect(windows.live?.bytes.length).toBe(524288);
  expect(jobLogSourceBytes(windows)).toBe(524288);
  expect(Array.from(text(windows))).toHaveLength(131072);
  expect(text(windows).includes("�")).toBe(false);
});

test("live eviction never mutates the retained older reading copy", () => {
  const initial = new Uint8Array(65536).fill(65);
  initial.set([77, 65, 82, 75, 69, 82, 10]);
  let windows = applyJobLogPage(emptyJobLogWindows(), rawPage(0, initial, initial.length), "live", "end");
  windows = retainJobLogReadingWindow(windows, 0);
  const reading = windows.older;
  const firstRow = reading?.rows[0];
  windows = loadBytes(new Uint8Array(524288 + 65536).fill(66), 65536, "live", "end", windows);
  expect(windows.older).toBe(reading);
  expect(windows.older?.bytes.slice(0, 7)).toEqual(new Uint8Array([77, 65, 82, 75, 69, 82, 10]));
  expect(windows.older?.rows[0]).toBe(firstRow);
  expect(firstRow?.line.map((run) => run.text).join("")).toBe("MARKER");
  expect(windows.live?.offsetBytes).toBeGreaterThan(65536);
});

function lines(count: number, first = 0): Uint8Array {
  return new TextEncoder().encode(Array.from({ length: count }, (_, index) => `line ${first + index}\n`).join(""));
}

test("an appended page keeps the window's settled rows instead of reparsing them", () => {
  const first = lines(100);
  const before = applyJobLogPage(emptyJobLogWindows(), rawPage(0, first, first.length), "live", "end");
  const next = lines(100, 100);
  const after = applyJobLogPage(before, rawPage(first.length, next, first.length + next.length), "live", "end");
  expect(after.live?.rows).toHaveLength(200);
  expect(after.live?.rows.slice(0, 99).every((row, index) => row === before.live?.rows[index])).toBe(true);
});

test("evicting the front keeps the surviving rows instead of reparsing them", () => {
  const source = lines(60000);
  const before = loadBytes(source.slice(0, 524288), 0, "live", "end");
  const after = applyJobLogPage(
    before,
    rawPage(524288, source.slice(524288, 524288 + 65536), 524288 + 65536),
    "live",
    "end",
  );
  const survivor = after.live?.rows[0];
  expect(after.live?.offsetBytes).toBeGreaterThan(0);
  expect(before.live?.rows.includes(survivor as JobLogRow)).toBe(true);
});

test("the joined rows of overlapping windows reuse each window's parse", () => {
  const source = lines(200);
  let windows = applyJobLogPage(emptyJobLogWindows(), rawPage(0, source.slice(0, 1000), source.length), "older", "end");
  windows = applyJobLogPage(windows, rawPage(900, source.slice(900), source.length), "live", "end");
  const joined = jobLogRows(windows);
  expect(joined.map((row) => row.line.map((run) => run.text).join(""))).toEqual(
    Array.from({ length: 200 }, (_, index) => `line ${index}`),
  );
  expect(joined[0]).toBe(windows.older?.rows[0]);
  expect(joined.at(-2)).toBe(windows.live?.rows.at(-2));
});

// Each blank line is its own boundary; reuse carries far more of them than a
// call's argument list can hold.
test("a window of blank lines accepts another page", () => {
  const blank = new Uint8Array(524288).fill(10);
  const windows = loadBytes(blank, 0, "live", "end");
  const after = applyJobLogPage(windows, rawPage(524288, new Uint8Array(65536).fill(10), 589824), "live", "end");
  expect(after.live?.rows).toHaveLength(524288);
  expect(after.live?.offsetBytes).toBe(65536);
});

// A backward wheel that reaches the loaded window's start can lose the race
// against TanStack Virtual's measurement-time scroll adjustment: measuring the
// rows the wheel scrolled past replaces the wheel's target with the adjusted
// offset, so the unloaded marker ends up just above the viewport (#4770). The
// boundary is geometric, not visual: the first loaded row has been scrolled
// fully past the top, so the gap above it is where paging resumes.
const backwardRows: JobLogDemandRow[] = [
  { kind: "pruned", startBytes: 0, endBytes: 100 },
  { kind: "unloaded", startBytes: 100, endBytes: 200 },
  { kind: "output", row: { offsetBytes: 200, endBytes: 300 } },
  { kind: "output", row: { offsetBytes: 300, endBytes: 400 } },
];
const backwardGaps = backwardRows.flatMap((row, index) => (row.kind === "unloaded" ? [{ row, index }] : []));

// Viewport top 50, height 100. The first loaded row (index 2) ends at 48, two
// pixels above the viewport top.
const base = {
  rows: backwardRows,
  gaps: backwardGaps,
  markerStartBytes: undefined,
  previous: { byteOffset: 250, following: false },
  byteOffset: 210,
  following: false,
  geometry: { viewportTop: 50, viewportHeight: 100, rowBottom: (index: number) => (index === 2 ? 48 : undefined) },
};

test("pages backward at the geometric boundary when measurement hides the unloaded marker", () => {
  const { index, geometric } = jobLogDemandGapIndex(base);
  expect(index).toBe(1);
  expect(geometric).toBe(true);
  expect(backwardRows[index]).toMatchObject({ kind: "unloaded", startBytes: 100, endBytes: 200 });
});

test("does not page at the boundary while the first loaded row is still visible", () => {
  expect(
    jobLogDemandGapIndex({
      ...base,
      geometry: { ...base.geometry, rowBottom: (index) => (index === 2 ? 60 : undefined) },
    }),
  ).toEqual({ index: -1, geometric: false });
});

test("does not page at the backward boundary when the wheel advanced forward", () => {
  expect(jobLogDemandGapIndex({ ...base, previous: { byteOffset: 210, following: false }, byteOffset: 250 })).toEqual({
    index: -1,
    geometric: false,
  });
});

test("does not page from the gap while the reader is deeper inside the loaded window", () => {
  expect(
    jobLogDemandGapIndex({
      ...base,
      geometry: { ...base.geometry, rowBottom: (index) => (index === 2 ? -200 : undefined) },
    }),
  ).toEqual({ index: -1, geometric: false });
});

test("still pages from a visible marker and from a crossed gap", () => {
  expect(jobLogDemandGapIndex({ ...base, markerStartBytes: 100, previous: null })).toEqual({
    index: 1,
    geometric: false,
  });
  // The reading position was past the gap and is now before it.
  expect(jobLogDemandGapIndex({ ...base, byteOffset: 50 })).toEqual({ index: 1, geometric: false });
});

// #4825: the same measurement race runs the other way when the reader wheels
// forward toward the live window. The unloaded marker can end up just below the
// viewport while the last loaded row before it is still on screen, with its
// bottom below the viewport bottom. No marker is visible and no gap was
// crossed, so only a forward geometric boundary can page from the gap.
const forwardRows: JobLogDemandRow[] = [
  { kind: "pruned", startBytes: 0, endBytes: 100 },
  { kind: "output", row: { offsetBytes: 100, endBytes: 200 } },
  { kind: "unloaded", startBytes: 200, endBytes: 300 },
  { kind: "output", row: { offsetBytes: 300, endBytes: 400 } },
];
const forwardGaps = forwardRows.flatMap((row, index) => (row.kind === "unloaded" ? [{ row, index }] : []));
const forwardBase = {
  rows: forwardRows,
  gaps: forwardGaps,
  markerStartBytes: undefined,
  previous: { byteOffset: 150, following: false },
  byteOffset: 180,
  following: false,
  // Viewport top 50, bottom 150. The last loaded row before the gap ends at
  // 170, twenty pixels below the viewport bottom.
  geometry: { viewportTop: 50, viewportHeight: 100, rowBottom: (index: number) => (index === 1 ? 170 : undefined) },
};

test("pages forward at the geometric boundary when measurement hides the unloaded marker", () => {
  const { index, geometric } = jobLogDemandGapIndex(forwardBase);
  expect(index).toBe(2);
  expect(geometric).toBe(true);
  expect(forwardRows[index]).toMatchObject({ kind: "unloaded", startBytes: 200, endBytes: 300 });
});

const separatedForwardRows: JobLogDemandRow[] = [
  { kind: "output", row: { offsetBytes: 100, endBytes: 190 } },
  { kind: "unloaded", startBytes: 200, endBytes: 300 },
  { kind: "output", row: { offsetBytes: 300, endBytes: 400 } },
];
const forwardBoundaryMisses: Array<{
  label: string;
  overrides: Partial<Parameters<typeof jobLogDemandGapIndex>[0]>;
}> = [
  {
    label: "the last loaded row is above the viewport bottom",
    overrides: { geometry: { ...forwardBase.geometry, rowBottom: (index) => (index === 1 ? 140 : undefined) } },
  },
  {
    label: "the wheel moved backward",
    overrides: { previous: { byteOffset: 180, following: false }, byteOffset: 150 },
  },
  {
    label: "the gap is more than a viewport below the reader",
    overrides: { geometry: { ...forwardBase.geometry, rowBottom: (index) => (index === 1 ? 400 : undefined) } },
  },
  {
    label: "the gap is not adjacent to the loaded row",
    overrides: {
      rows: separatedForwardRows,
      gaps: separatedForwardRows.flatMap((row, index) => (row.kind === "unloaded" ? [{ row, index }] : [])),
    },
  },
  {
    label: "the reader is following the bottom",
    overrides: { following: true },
  },
  {
    label: "the gap has no loaded row before it",
    overrides: {
      rows: [
        { kind: "unloaded", startBytes: 200, endBytes: 300 },
        { kind: "output", row: { offsetBytes: 300, endBytes: 400 } },
      ],
      gaps: [{ row: { startBytes: 200, endBytes: 300 }, index: 0 }],
      byteOffset: 190,
    },
  },
  {
    label: "the row before the gap is pruned",
    overrides: {
      rows: [
        { kind: "pruned", startBytes: 0, endBytes: 200 },
        { kind: "unloaded", startBytes: 200, endBytes: 300 },
        { kind: "output", row: { offsetBytes: 300, endBytes: 400 } },
      ],
      gaps: [{ row: { startBytes: 200, endBytes: 300 }, index: 1 }],
      byteOffset: 190,
    },
  },
  {
    label: "the reader just left following",
    overrides: {
      previous: { byteOffset: 150, following: true },
      following: false,
    },
  },
];

test.each(forwardBoundaryMisses)("does not page at the forward boundary when $label", ({ overrides }) => {
  expect(jobLogDemandGapIndex({ ...forwardBase, ...overrides })).toEqual({ index: -1, geometric: false });
});

test("pages from the forward gap when an earlier gap is not at the boundary", () => {
  const rows: JobLogDemandRow[] = [
    { kind: "output", row: { offsetBytes: 0, endBytes: 100 } },
    { kind: "unloaded", startBytes: 100, endBytes: 200 },
    { kind: "output", row: { offsetBytes: 200, endBytes: 300 } },
    { kind: "unloaded", startBytes: 300, endBytes: 400 },
    { kind: "output", row: { offsetBytes: 400, endBytes: 500 } },
  ];
  const gaps = rows.flatMap((row, index) => (row.kind === "unloaded" ? [{ row, index }] : []));
  const decision = jobLogDemandGapIndex({
    ...forwardBase,
    rows,
    gaps,
    previous: { byteOffset: 250, following: false },
    byteOffset: 280,
    geometry: {
      ...forwardBase.geometry,
      // The first gap's predecessor ends above the viewport bottom; the second
      // precedes it by twenty pixels.
      rowBottom: (index) => (index === 0 ? 100 : index === 2 ? 170 : undefined),
    },
  });
  expect(decision).toEqual({ index: 3, geometric: true });
  expect(rows[decision.index]).toMatchObject({ kind: "unloaded", startBytes: 300, endBytes: 400 });
});
