// Pure text-formatting helpers shared by the web and native renderers.

const TOKEN_UNITS = [
  ["K", 1_000],
  ["M", 1_000_000],
  ["B", 1_000_000_000],
] as const;

// A token count the way both clients print it (spec 5): 999, 1.2K, 39.8K,
// 412K, 46M, 1.5B. One decimal below 100 of a unit, none from 100 up; a count
// that would round to 1000 of a unit reads as one of the next; B is the last
// unit. A negative or non-finite count reads 0.
export function formatTokenCount(n: number): string {
  const value = Number.isFinite(n) && n > 0 ? Math.round(n) : 0;
  if (value < 1000) return String(value);
  let text = "";
  for (const [unit, size] of TOKEN_UNITS) {
    const scaled = value / size;
    text = `${(scaled < 100 ? scaled.toFixed(1) : scaled.toFixed(0)).replace(/\.0$/, "")}${unit}`;
    if (Number.parseFloat(text) < 1000) break;
  }
  return text;
}

// Floors at 1ms, then uses decimal or whole seconds as durations grow.
// Non-finite input renders "" (no duration to show): unlike a token count,
// a duration has no honest zero to clamp to.
export function formatDurationMs(ms: number): string {
  if (!Number.isFinite(ms)) return "";
  const rounded = Math.max(1, Math.round(ms));
  if (rounded < 1000) return `${rounded}ms`;
  if (rounded < 10000) return `${(rounded / 1000).toFixed(1).replace(/\.0$/, "")}s`;
  return `${Math.round(rounded / 1000)}s`;
}

export type DurationUnit = "second" | "minute" | "hour" | "day";

export const DURATION_UNIT_MS: Record<DurationUnit, number> = {
  second: 1_000,
  minute: 60_000,
  hour: 3_600_000,
  day: 86_400_000,
};

// A duration in its one largest whole unit, rounded down. Negative (clock
// skew) and non-finite input read as zero seconds.
export function durationParts(ms: number): { count: number; unit: DurationUnit } {
  const seconds = Number.isFinite(ms) ? Math.max(0, Math.floor(ms / 1000)) : 0;
  if (seconds < 60) return { count: seconds, unit: "second" };
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return { count: minutes, unit: "minute" };
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return { count: hours, unit: "hour" };
  return { count: Math.floor(hours / 24), unit: "day" };
}

// The compact stamp for a duration or age: 40s, 12m, 3h, 2d.
export function compactDuration(ms: number): string {
  const { count, unit } = durationParts(ms);
  return `${count}${unit[0]}`;
}

// The same duration as a screen reader should say it: "3 minutes", "1 day".
export function spokenDuration(ms: number): string {
  const { count, unit } = durationParts(ms);
  return `${count} ${unit}${count === 1 ? "" : "s"}`;
}

// A caller-supplied span of seconds in the units a person would say it in.
// Sub-minute stays in seconds ("45s", fractional "1.5s" for sub-second
// producer precision such as progress_interval_ms 1500); whole minutes
// collapse ("5m"); leftover seconds are kept ("1m30s", never a lossy "1m");
// an hour or more names hours and leftover minutes ("1h05m"). Rounding
// happens FIRST, so a 60s carry can never surface ("1m60s"): the seconds path
// runs iff the rounded value is below 60, and the minute path divides the
// rounded value, whose remainder is always below 60.
export function formatSpan(totalSeconds: number): string {
  const rounded = Math.round(totalSeconds);
  if (rounded < 60) {
    if (Number.isInteger(totalSeconds)) return `${rounded}s`;
    return `${(Math.round(totalSeconds * 10) / 10).toString()}s`;
  }
  const totalMinutes = Math.floor(rounded / 60);
  const leftoverSeconds = rounded % 60;
  if (totalMinutes < 60) {
    return leftoverSeconds === 0 ? `${totalMinutes}m` : `${totalMinutes}m${String(leftoverSeconds).padStart(2, "0")}s`;
  }
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return minutes === 0 ? `${hours}h` : `${hours}h${String(minutes).padStart(2, "0")}m`;
}

// Epoch milliseconds of an ISO timestamp; a missing or malformed one stays
// absent.
export function parseTimestampMs(value: string | undefined): number | undefined {
  if (!value) return undefined;
  const parsed = Date.parse(value);
  return Number.isNaN(parsed) ? undefined : parsed;
}

// Returns a clipped first non-blank line.
export function firstLine(text: string, maxLen: number): string {
  const line = text
    .split("\n")
    .map((l) => l.trim())
    .find((l) => l.length > 0);
  if (!line) return "";
  if (line.length <= maxLen) return line;
  return `${line.slice(0, maxLen).trimEnd()}…`;
}

// Character counts retain one decimal of thousands, unlike token counts.
export function formatCharCount(n: number): string {
  const clamped = Number.isFinite(n) && n > 0 ? n : 0;
  if (clamped < 1000) return `${clamped} chars`;
  return `${(clamped / 1000).toFixed(1).replace(/\.0$/, "")}K chars`;
}

// Local 24-hour time; missing or invalid timestamps stay absent.
export function formatClockTime(iso: string | undefined): string | undefined {
  if (iso === undefined) return undefined;
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) return undefined;
  const hours = String(parsed.getHours()).padStart(2, "0");
  const minutes = String(parsed.getMinutes()).padStart(2, "0");
  return `${hours}:${minutes}`;
}

// Compact elapsed clock; negative skew clamps to zero.
export function formatElapsed(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const totalMinutes = Math.floor(totalSeconds / 60);
  if (totalMinutes < 60) return `${totalMinutes}m${String(totalSeconds % 60).padStart(2, "0")}s`;
  const hours = Math.floor(totalMinutes / 60);
  return `${hours}h${String(totalMinutes % 60).padStart(2, "0")}m`;
}

// Split a markdown mandate into the first paragraph (visible) and the rest
// (behind a disclosure). Shared by ActivityRowDetail and DelegateStatusBody
// so the paragraph-split rule lives in one place.
export function splitMandate(task: string | undefined): { first: string; rest: string } | undefined {
  if (!task || task.trim() === "") return undefined;
  const paragraphs = task.split(/\n\s*\n/);
  return {
    first: paragraphs[0] ?? "",
    rest: paragraphs.slice(1).join("\n\n"),
  };
}

// Returns the first substantive markdown line as plain text.
export function plainQuoteLine(text: string): string {
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "" || /^#+\s/.test(line)) continue;
    return line.replace(/\*\*|__|[`*]/g, "").trim();
  }
  return "";
}
