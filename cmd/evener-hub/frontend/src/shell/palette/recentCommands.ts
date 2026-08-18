// Recently-run palette commands, remembered most-recent-first and capped at
// five (search.js:16-17,619-633). The storage key is the legacy
// "serf.search.recentCommands" - deliberately OUTSIDE the serf.prefs.*
// namespace (stores/prefs.ts), matching the legacy exactly so a recency list
// written by either UI reads back in the other. Every read/write is
// best-effort: a private-browsing mode that throws on storage access must
// never break the palette, mirroring stores/prefs.ts's own readRaw/writeRaw.

export const RECENT_COMMANDS_KEY = "serf.search.recentCommands";
const RECENT_COMMANDS_LIMIT = 5;

export function readRecentCommandIds(): string[] {
  try {
    const raw = localStorage.getItem(RECENT_COMMANDS_KEY);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? parsed.filter((id): id is string => typeof id === "string") : [];
  } catch {
    return [];
  }
}

export function rememberCommand(id: string): void {
  if (!id) return;
  const next = [id, ...readRecentCommandIds().filter((existing) => existing !== id)].slice(0, RECENT_COMMANDS_LIMIT);
  try {
    localStorage.setItem(RECENT_COMMANDS_KEY, JSON.stringify(next));
  } catch {
    // Best-effort, same rationale as stores/prefs.ts writeRaw.
  }
}
