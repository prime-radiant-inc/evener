// @vitest-environment node

import { parseJobOutputPage } from "@evener/appwire-client";
import { expect, test } from "vitest";
import type { JobLogWindows } from "./jobLogWindow";
import {
  applyJobLogPage,
  emptyJobLogWindows,
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
