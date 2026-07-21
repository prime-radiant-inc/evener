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
  return line.slice(0, maxLen).trimEnd() + "…";
}
