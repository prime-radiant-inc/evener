import type { JobOutputTail } from "./types.gen";

export interface JobLogTail extends JobOutputTail {
  // Normalize the optional wire field for consumers.
  hasEarlier: boolean;
}

// Validate untrusted input and normalize omitted flags before presenting a log.
export function parseJobLogTail(data: unknown): JobLogTail | null {
  if (typeof data !== "object" || data === null || Array.isArray(data)) return null;
  const raw = data as Record<string, unknown>;
  if (typeof raw.tail !== "string") return null;
  if (typeof raw.totalBytes !== "number" || typeof raw.retainedStart !== "number") return null;
  if (
    !Number.isSafeInteger(raw.totalBytes) ||
    raw.totalBytes < 0 ||
    !Number.isSafeInteger(raw.retainedStart) ||
    raw.retainedStart < 0 ||
    raw.retainedStart > raw.totalBytes ||
    (raw.truncated !== undefined && typeof raw.truncated !== "boolean") ||
    (raw.hasEarlier !== undefined && typeof raw.hasEarlier !== "boolean")
  )
    return null;
  return {
    tail: raw.tail,
    totalBytes: raw.totalBytes,
    retainedStart: raw.retainedStart,
    truncated: raw.truncated === true,
    hasEarlier: raw.hasEarlier === true,
  };
}
