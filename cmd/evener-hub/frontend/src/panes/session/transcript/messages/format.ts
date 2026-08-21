// Pure text-formatting helpers shared by transcript renderers.

// Deliberately never scales past "k" to match the legacy formatter.
export function formatTokenCount(n: number): string {
  const clamped = Number.isFinite(n) && n > 0 ? n : 0;
  if (clamped < 1000) return String(Math.round(clamped));
  return `${Math.round(clamped / 1000)}k`;
}

// Floors at 1ms, then uses decimal or whole seconds as durations grow.
export function formatDurationMs(ms: number): string {
  const rounded = Math.max(1, Math.round(ms));
  if (rounded < 1000) return `${rounded}ms`;
  if (rounded < 10000) return `${(rounded / 1000).toFixed(1).replace(/\.0$/, "")}s`;
  return `${Math.round(rounded / 1000)}s`;
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
  return `${(clamped / 1000).toFixed(1).replace(/\.0$/, "")}k chars`;
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

// Seconds-carrying local time for card activity.
export function formatClockTimeSeconds(iso: string | undefined): string | undefined {
  if (iso === undefined) return undefined;
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) return undefined;
  const hours = String(parsed.getHours()).padStart(2, "0");
  const minutes = String(parsed.getMinutes()).padStart(2, "0");
  const seconds = String(parsed.getSeconds()).padStart(2, "0");
  return `${hours}:${minutes}:${seconds}`;
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

// Returns the first substantive markdown line as plain text.
export function plainQuoteLine(text: string): string {
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "" || /^#+\s/.test(line)) continue;
    return line.replace(/\*\*|__|[`*]/g, "").trim();
  }
  return "";
}
