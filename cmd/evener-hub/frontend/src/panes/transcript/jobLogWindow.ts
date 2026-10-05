import type { DecodedJobOutputPage } from "@evener/appwire-client";
import type { AnsiByteRow, AnsiByteRows, AnsiByteState } from "../../widgets/codeblock/ansi";
import { ANSI_BYTE_FRAGMENT_BYTES, parseAnsiByteRows } from "../../widgets/codeblock/ansi";

export const JOB_LOG_PAGE_BYTES = 64 * 1024;
export const JOB_LOG_WINDOW_BYTES = 512 * 1024;
export const JOB_LOG_FRAGMENT_BYTES = ANSI_BYTE_FRAGMENT_BYTES;

export type JobLogRow = AnsiByteRow;
export type JobLogByteWindow = {
  offsetBytes: number;
  bytes: Uint8Array;
  initialState?: AnsiByteState;
  rows: JobLogRow[];
  boundaries: AnsiByteRows["boundaries"];
};
export type JobLogWindows = {
  older: JobLogByteWindow | null;
  live: JobLogByteWindow | null;
  retainedStartBytes: number;
  totalBytes: number;
};
export type JobLogDemand = { direction: "backward" | "forward"; boundaryBytes: number; limitBytes: number };
export type JobLogPageSelection = { beforeBytes: number; maxBytes: number };

export function emptyJobLogWindows(): JobLogWindows {
  return { older: null, live: null, retainedStartBytes: 0, totalBytes: 0 };
}

export function reconcileJobLogBounds(
  windows: JobLogWindows,
  retainedStartBytes: number,
  totalBytes: number,
): JobLogWindows {
  if (
    !Number.isSafeInteger(retainedStartBytes) ||
    !Number.isSafeInteger(totalBytes) ||
    retainedStartBytes < windows.retainedStartBytes ||
    totalBytes < windows.totalBytes ||
    retainedStartBytes > totalBytes
  ) {
    throw new Error("job output bounds are inconsistent");
  }
  return { ...windows, retainedStartBytes, totalBytes };
}

function verifyOverlap(window: JobLogByteWindow | null, page: Pick<DecodedJobOutputPage, "offsetBytes" | "bytes">) {
  if (window === null) return;
  const end = Math.min(window.offsetBytes + window.bytes.length, page.offsetBytes + page.bytes.length);
  for (let offset = Math.max(window.offsetBytes, page.offsetBytes); offset < end; offset++) {
    if (window.bytes[offset - window.offsetBytes] !== page.bytes[offset - page.offsetBytes]) {
      throw new Error("job output bytes changed at an existing position");
    }
  }
}

function mergeRawWindow(
  window: JobLogByteWindow | null,
  page: Pick<DecodedJobOutputPage, "offsetBytes" | "bytes">,
): Pick<JobLogByteWindow, "offsetBytes" | "bytes" | "initialState"> {
  if (window === null) return { offsetBytes: page.offsetBytes, bytes: page.bytes.slice() };
  const oldEnd = window.offsetBytes + window.bytes.length;
  const pageEnd = page.offsetBytes + page.bytes.length;
  if (page.offsetBytes > oldEnd || pageEnd < window.offsetBytes)
    return { offsetBytes: page.offsetBytes, bytes: page.bytes.slice() };
  verifyOverlap(window, page);
  const start = Math.min(window.offsetBytes, page.offsetBytes);
  const end = Math.max(oldEnd, pageEnd);
  const bytes = new Uint8Array(end - start);
  bytes.set(window.bytes, window.offsetBytes - start);
  bytes.set(page.bytes, page.offsetBytes - start);
  return { offsetBytes: start, bytes, initialState: start === window.offsetBytes ? window.initialState : undefined };
}

export function applyJobLogPage(
  windows: JobLogWindows,
  page: DecodedJobOutputPage,
  target: "older" | "live",
  keep: "start" | "end",
): JobLogWindows {
  const next = reconcileJobLogBounds(windows, page.retainedStartBytes, page.totalBytes);
  if (page.bytes.length === 0 && next[target] !== null) return next;
  verifyOverlap(next[target === "older" ? "live" : "older"], page);
  const merged = mergeRawWindow(next[target], page);
  let parsed = parseAnsiByteRows(merged.bytes, merged.offsetBytes, merged.initialState);
  if (merged.bytes.length > JOB_LOG_WINDOW_BYTES) {
    if (keep === "end") {
      const minimumCut = merged.offsetBytes + merged.bytes.length - JOB_LOG_WINDOW_BYTES;
      const boundary = parsed.boundaries.find((entry) => entry.offsetBytes >= minimumCut);
      if (boundary === undefined) throw new Error("job output trim has no parsed boundary");
      merged.bytes = merged.bytes.slice(boundary.offsetBytes - merged.offsetBytes);
      merged.offsetBytes = boundary.offsetBytes;
      merged.initialState = boundary.state;
    } else {
      const maximumEnd = merged.offsetBytes + JOB_LOG_WINDOW_BYTES;
      let boundary = parsed.boundaries[0];
      for (const entry of parsed.boundaries) {
        if (entry.offsetBytes > maximumEnd) break;
        boundary = entry;
      }
      if (boundary === undefined) throw new Error("job output trim has no parsed boundary");
      merged.bytes = merged.bytes.slice(0, boundary.offsetBytes - merged.offsetBytes);
    }
    parsed = parseAnsiByteRows(merged.bytes, merged.offsetBytes, merged.initialState);
  }
  return { ...next, [target]: { ...merged, rows: parsed.rows, boundaries: parsed.boundaries } };
}

export function retainJobLogReadingWindow(windows: JobLogWindows, byteOffset: number): JobLogWindows {
  const contains = (window: JobLogByteWindow | null) =>
    window !== null && byteOffset >= window.offsetBytes && byteOffset < window.offsetBytes + window.bytes.length;
  if (contains(windows.older) || !contains(windows.live)) return windows;
  return { ...windows, older: windows.live };
}

export function selectJobLogDemand(windows: JobLogWindows, demand: JobLogDemand): JobLogPageSelection | null {
  const floor = windows.retainedStartBytes;
  if (demand.direction === "backward") {
    const beforeBytes = Math.min(demand.boundaryBytes, windows.totalBytes);
    if (beforeBytes <= floor) return null;
    return { beforeBytes, maxBytes: Math.min(JOB_LOG_PAGE_BYTES, beforeBytes - floor) };
  }
  const start = Math.max(demand.boundaryBytes, floor);
  const limit = Math.min(demand.limitBytes, windows.totalBytes);
  if (start >= limit) return null;
  const count = Math.min(JOB_LOG_PAGE_BYTES, limit - start);
  return { beforeBytes: start + count, maxBytes: count };
}

export function jobLogSourceBytes({ older, live }: JobLogWindows): number {
  if (older === null) return live?.bytes.length ?? 0;
  if (live === null) return older.bytes.length;
  const overlap = Math.max(
    0,
    Math.min(older.offsetBytes + older.bytes.length, live.offsetBytes + live.bytes.length) -
      Math.max(older.offsetBytes, live.offsetBytes),
  );
  return older.bytes.length + live.bytes.length - overlap;
}

export function jobLogRows({ older, live }: JobLogWindows): JobLogRow[] {
  if (older === null) return live?.rows ?? [];
  if (live === null || live === older) return older.rows;
  const [first, second] = older.offsetBytes <= live.offsetBytes ? [older, live] : [live, older];
  if (first.offsetBytes + first.bytes.length < second.offsetBytes) return [...first.rows, ...second.rows];
  // This union is temporary presentation input, never a third retained window.
  const merged = mergeRawWindow(first, second);
  return parseAnsiByteRows(merged.bytes, merged.offsetBytes, merged.initialState).rows;
}
