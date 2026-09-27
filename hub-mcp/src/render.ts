// Text rendering shared by every tool: the PM reads these outputs, so every
// helper here is about readable, honest, bounded text.

const RELATIVE_MINUTES = 60_000;
const RELATIVE_HOURS = 60 * RELATIVE_MINUTES;
const RELATIVE_DAYS = 24 * RELATIVE_HOURS;

/** fmtClock renders an epoch-ms timestamp as UTC ISO plus a relative age. */
export function fmtClock(atMs: number, now: number = Date.now()): string {
  if (!atMs) return "unknown time";
  return `${new Date(atMs).toISOString()} (${fmtAge(atMs, now)})`;
}

/** fmtAge renders a relative age like "12m ago", "3d ago". */
export function fmtAge(atMs: number, now: number = Date.now()): string {
  const delta = Math.max(0, now - atMs);
  if (delta < RELATIVE_MINUTES) return `${Math.max(1, Math.round(delta / 1000))}s ago`;
  if (delta < RELATIVE_HOURS) return `${Math.round(delta / RELATIVE_MINUTES)}m ago`;
  if (delta < RELATIVE_DAYS) return `${Math.round(delta / RELATIVE_HOURS)}h ago`;
  return `${Math.round(delta / RELATIVE_DAYS)}d ago`;
}

/** fmtDuration renders milliseconds as "1.2s", "4m05s", "2h03m". */
export function fmtDuration(ms: number | undefined): string {
  if (!ms || ms < 0) return "";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const seconds = Math.floor(ms / 1000);
  if (seconds < 60) return `${(ms / 1000).toFixed(1)}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 90) return `${minutes}m${String(seconds % 60).padStart(2, "0")}s`;
  const hours = Math.floor(minutes / 60);
  return `${hours}h${String(minutes % 60).padStart(2, "0")}m`;
}

/**
 * truncate cuts text to max characters on a line boundary when it can, and
 * always says how much was cut. Never silently elide: the reader must know
 * the display is shorter than the source.
 */
export function truncate(text: string, max: number): string {
  const trimmed = text.trim();
  if (trimmed.length <= max) return trimmed;
  const cut = trimmed.slice(0, max);
  const lastLine = cut.lastIndexOf("\n");
  const head = lastLine > max / 2 ? cut.slice(0, lastLine) : cut;
  const kept = head.trimEnd();
  return `${kept}… [+${trimmed.length - kept.length} chars]`;
}

/** firstLine reduces a block of text to its first non-empty line. */
export function firstLine(text: string | undefined, max = 72): string {
  if (!text) return "";
  const line = text
    .split("\n")
    .map((l) => l.trim())
    .find((l) => l.length > 0);
  return line ? truncate(line, max) : "";
}

/** refLabel renders a ref plus its name when one is set. */
export function refLabel(ref: string, name?: string): string {
  return name ? `${ref} "${name}"` : ref;
}

/** fmtTokens renders a token count with k/M units. */
export function fmtTokens(n: number | undefined): string {
  if (!n) return "";
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(0)}k`;
  return String(n);
}
