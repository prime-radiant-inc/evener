// A system event's words (spec 8.2 "System event"), read from the structured
// detail the hub puts on the item's raw rather than from its prose, which
// carries engine terms ("Layer: summary") and raw numbers.
import { formatTokenCount } from "@evener/appwire-client";

function record(value: unknown): Record<string, unknown> | undefined {
	return typeof value === "object" && value !== null && !Array.isArray(value)
		? (value as Record<string, unknown>)
		: undefined;
}

function count(value: unknown): number {
	return typeof value === "number" && Number.isFinite(value) && value > 0 ? value : 0;
}

/** "Plugin superpowers loaded", or "Plugin loaded" when it has no name. The
 * counts stay out: they are the plugin's plumbing. */
export function pluginLoadedText(raw: unknown): string {
	const plugin = record(record(raw)?.pluginLoaded);
	const name = typeof plugin?.name === "string" ? plugin.name.trim() : "";
	return name ? `Plugin ${name} loaded` : "Plugin loaded";
}

/** "Context compacted · 412K → 38K tokens", else the turns ("40 → 5 turns"),
 * else a bare "Context compacted" (apptranscript's ContextCompactionAnnouncement
 * puts the numbers on raw.compaction). */
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

/** A compaction's summary folds under this label and opens to the summary. */
export const CONTEXT_SUMMARY_LABEL = "Context summary";
