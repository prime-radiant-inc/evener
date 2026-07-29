// Small, pure text-formatting helpers shared by the message renderers in
// this directory. Kept dependency-free (no React, no ItemModel/TurnModel)
// so each is trivially unit-testable in isolation - see format.test.ts.

// formatTokenCount mirrors legacy's own token formatter
// (renderer-format.js:582-587): plain integer under 1000, "Nk" (rounded, no
// decimal) at/above 1000 - deliberately never scales past "k" (1,000,000
// reads "1000k", not "1M").
export function formatTokenCount(n: number): string {
  const clamped = Number.isFinite(n) && n > 0 ? n : 0;
  if (clamped < 1000) return String(Math.round(clamped));
  return `${Math.round(clamped / 1000)}k`;
}

// formatDurationMs mirrors legacy's formatToolDuration
// (renderer-format.js:636): floors sub-1000ms at 1ms so a real (non-zero)
// duration never prints the dishonest "0ms"; 1000-9999ms shows one decimal
// of seconds with a stripped trailing ".0"; 10000ms+ shows whole seconds.
export function formatDurationMs(ms: number): string {
  const rounded = Math.max(1, Math.round(ms));
  if (rounded < 1000) return `${rounded}ms`;
  if (rounded < 10000) return `${(rounded / 1000).toFixed(1).replace(/\.0$/, "")}s`;
  return `${Math.round(rounded / 1000)}s`;
}

// firstLine finds the first non-blank line of `text`, trims it, and clips
// it to maxLen with a trailing ellipsis if it overflows. Deliberately
// simpler than legacy's reasoningGist (renderer-format.js:688-697, which
// prefers the LAST sentence and strips filler words) - the wave-4 scope
// calls for a first-line preview, not a reproduction of that heuristic.
export function firstLine(text: string, maxLen: number): string {
  const line = text
    .split("\n")
    .map((l) => l.trim())
    .find((l) => l.length > 0);
  if (!line) return "";
  if (line.length <= maxLen) return line;
  return `${line.slice(0, maxLen).trimEnd()}…`;
}

// formatCharCount renders a plain character count for a scaffolding
// disclosure's summary line (SystemNoticeItem's "System prompt · 8.2k
// chars"): the exact count under 1000, otherwise one decimal place of
// thousands with a stripped trailing ".0" (mirrors formatDurationMs's own
// stripping above). Deliberately its own helper rather than reusing
// formatTokenCount - that one rounds to a whole "k" with no decimal, fine
// for a token count but too coarse here, where the reader is deciding
// whether a many-thousand-character block is worth expanding.
export function formatCharCount(n: number): string {
  const clamped = Number.isFinite(n) && n > 0 ? n : 0;
  if (clamped < 1000) return `${clamped} chars`;
  return `${(clamped / 1000).toFixed(1).replace(/\.0$/, "")}k chars`;
}

// formatClockTime renders an item's wall-clock moment (ItemModel.startedAt)
// for the slack-lean speaker header's meta slot ("You · 12:41"): the local
// 24-hour "HH:MM", zero-padded so times align down the transcript. Local
// time, not UTC - the header answers "when did this happen" for the person
// reading their own session, and Date's getHours/getMinutes are the local
// projection of the parsed instant. undefined for a missing or unparseable
// timestamp: a header with no time shows no time rather than a guess.
export function formatClockTime(iso: string | undefined): string | undefined {
  if (iso === undefined) return undefined;
  const parsed = new Date(iso);
  if (Number.isNaN(parsed.getTime())) return undefined;
  const hours = String(parsed.getHours()).padStart(2, "0");
  const minutes = String(parsed.getMinutes()).padStart(2, "0");
  return `${hours}:${minutes}`;
}
