// A system event's words (spec 8.2 "System event"), shared by the web and the
// phone. Each reads the structured detail the hub puts on the item's raw
// rather than its prose, which carries engine terms ("Layer: summary") and raw
// numbers, or nothing at all (a plugin load keeps its summary in the
// description).

import { formatTokenCount } from "./displayFormat";
import type { ItemModel } from "./model";

/** The systemMessage event kind of an error: a turn's failure, or a session
 * error the live overlay shows. */
export const ERROR_EVENT_KIND = "error";

/** A compaction's summary or checkpoint folds under this label and opens to
 * the summary. */
const CONTEXT_SUMMARY_LABEL = "Context summary";

// What a failure with no message reads as: apptranscript's
// FailedTurnFallbackText, the failure item's text and turn.error's message
// both.
const FAILED_TURN_FALLBACK_TEXT = "The turn failed.";

// What apptranscript's ContextCompactionAnnouncement says when a pass carries
// no numbers (and so no raw): the pass ran, which "Context compacted" says.
const COMPACTION_RAN_TEXT = "Context compaction ran";

function record(value: unknown): Record<string, unknown> | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function count(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

// "Plugin superpowers loaded", or "Plugin loaded" when it has no name
// (apptranscript's PluginLoadedAnnouncement puts it on raw.pluginLoaded). The
// counts stay out: they are the plugin's plumbing.
function pluginLoadedText(plugin: Record<string, unknown>): string {
  const name = typeof plugin.name === "string" ? plugin.name.trim() : "";
  return name ? `Plugin ${name} loaded` : "Plugin loaded";
}

// "Context compacted · 412K → 38K tokens", else the turns ("40 → 5 turns"),
// else a bare "Context compacted" (apptranscript's
// ContextCompactionAnnouncement puts the numbers on raw.compaction).
function contextCompactedText(pass: Record<string, unknown>): string {
  const tokensBefore = count(pass.est_tokens_before);
  const tokensAfter = count(pass.est_tokens_after);
  if (tokensBefore > 0 || tokensAfter > 0) {
    return `Context compacted · ${formatTokenCount(tokensBefore)} → ${formatTokenCount(tokensAfter)} tokens`;
  }
  const turnsBefore = count(pass.turns_before);
  const turnsAfter = count(pass.turns_after);
  if (turnsBefore > 0 || turnsAfter > 0) return `Context compacted · ${turnsBefore} → ${turnsAfter} turns`;
  return "Context compacted";
}

/** What a system event says, in both clients: its text, and for one that
 * folds, the label it folds under and whether it opens to markdown.
 * - A plugin load reads its name from raw.pluginLoaded, and a compaction pass
 *   its numbers from raw.compaction. A daemon that sent no such structure
 *   still said something in its text, which is kept.
 * - A compaction's summary or checkpoint folds under "Context summary".
 * - Every other event's text is its words. */
export function systemEventWords(item: Pick<ItemModel, "eventKind" | "text" | "raw">): {
  text: string;
  label?: string;
  rendersMarkdown?: boolean;
} {
  switch (item.eventKind) {
    case "plugin_loaded": {
      const plugin = record(record(item.raw)?.pluginLoaded);
      if (plugin) return { text: pluginLoadedText(plugin) };
      return { text: item.text.trim() || "Plugin loaded" };
    }
    case "context_compaction": {
      const pass = record(record(item.raw)?.compaction);
      if (pass) return { text: contextCompactedText(pass) };
      const text = item.text.trim();
      return { text: text && text !== COMPACTION_RAN_TEXT ? item.text : "Context compacted" };
    }
    case "compaction":
      return { text: item.text, label: CONTEXT_SUMMARY_LABEL, rendersMarkdown: true };
    case "notes-context":
      return { text: item.text, label: "Shared notes updated" };
    default:
      return { text: item.text };
  }
}

/** True for an error systemMessage: a turn's failure, or a session error. */
export function isErrorEvent(item: Pick<ItemModel, "type" | "eventKind">): boolean {
  return item.type === "systemMessage" && item.eventKind === ERROR_EVENT_KIND;
}

/** True for the error systemMessage that says what its turn's error says: a
 * reload carries a failed turn's failure twice, as turn.error and as
 * apptranscript's TurnFailure item, whose text is the failure's message (both
 * fall back to "The turn failed." when it has none). Another error in the same
 * turn is news of its own. Matched by text, so an earlier error with word for
 * word the same message goes with it. */
export function echoesTurnError(
  item: Pick<ItemModel, "type" | "eventKind" | "text">,
  error: { message?: string | null } | null | undefined,
): boolean {
  if (!error || !isErrorEvent(item)) return false;
  return item.text.trim() === ((error.message ?? "").trim() || FAILED_TURN_FALLBACK_TEXT);
}
