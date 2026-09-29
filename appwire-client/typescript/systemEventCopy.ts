// A system event's words (spec 8.2 "System event"), shared by the web and the
// phone. Each reads the structured detail the hub puts on the item's raw
// rather than its prose, which carries engine terms ("Layer: summary") and raw
// numbers, or nothing at all (a plugin load keeps its summary in the
// description).

import { formatTokenCount } from "./displayFormat";

/** The systemMessage event kind of an error: a turn's failure, or a session
 * error the live overlay shows. */
export const ERROR_EVENT_KIND = "error";

/** A compaction's summary or checkpoint folds under this label and opens to
 * the summary. */
export const CONTEXT_SUMMARY_LABEL = "Context summary";

function record(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function count(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : 0;
}

/** "Plugin superpowers loaded", or "Plugin loaded" when it has no name
 * (apptranscript's PluginLoadedAnnouncement puts it on raw.pluginLoaded). The
 * counts stay out: they are the plugin's plumbing. */
export function pluginLoadedText(raw: unknown): string {
  const plugin = record(record(raw)?.pluginLoaded);
  const name = typeof plugin?.name === "string" ? plugin.name.trim() : "";
  return name ? `Plugin ${name} loaded` : "Plugin loaded";
}

/** "Context compacted · 412K → 38K tokens", else the turns ("40 → 5 turns"),
 * else a bare "Context compacted" (apptranscript's
 * ContextCompactionAnnouncement puts the numbers on raw.compaction). */
export function contextCompactedText(raw: unknown): string {
  const pass = record(record(raw)?.compaction);
  const tokensBefore = count(pass?.est_tokens_before);
  const tokensAfter = count(pass?.est_tokens_after);
  if (tokensBefore > 0 || tokensAfter > 0) {
    return `Context compacted · ${formatTokenCount(tokensBefore)} → ${formatTokenCount(tokensAfter)} tokens`;
  }
  const turnsBefore = count(pass?.turns_before);
  const turnsAfter = count(pass?.turns_after);
  if (turnsBefore > 0 || turnsAfter > 0) return `Context compacted · ${turnsBefore} → ${turnsAfter} turns`;
  return "Context compacted";
}

/** True for the error systemMessage that says what its turn's error says: a
 * reload carries a failed turn's failure twice, as turn.error and as
 * apptranscript's TurnFailure item, whose text is the failure's message (both
 * fall back to "The turn failed." when it has none). Another error in the same
 * turn is news of its own. Matched by text, so an earlier error with word for
 * word the same message goes with it. */
export function echoesTurnError(
  item: { type: string; eventKind?: string | null; text?: string | null },
  error: { message?: string | null } | null | undefined,
): boolean {
  if (!error || item.type !== "systemMessage" || item.eventKind !== ERROR_EVENT_KIND) return false;
  return (item.text ?? "").trim() === (error.message ?? "").trim();
}
