export interface JobLogTail {
  tail: string;
  totalBytes: number;
  retainedStart: number;
  truncated: boolean;
  // The wire omits this field when no earlier retained page exists.
  hasEarlier: boolean;
}

// evener/jobs/output's data field crosses the wire untyped (unknown on the
// generated JobsOutputResponse); validate the appwire.JobOutputTail shape
// before trusting it rather than casting.
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
