// Merges a later model catalog snapshot into the entries already visible to a
// pane. The model/list response now carries the complete descriptor, so this
// helper only protects a visible entry from a less-informed refresh.
import type { ModelCatalog, ModelCatalogEntry } from "./index";

function key(provider: string, model: string): string {
  return `${provider}/${model}`;
}

// A refresh can be less informed than the snapshot already visible to the
// pane. Entries are only mergeable when their provider/model identities match;
// a same-identity refresh must not erase metadata already supplied by an
// earlier response.
export function mergeCatalogEntry(
  existing: ModelCatalogEntry | undefined,
  incoming: ModelCatalogEntry,
): ModelCatalogEntry {
  if (existing === undefined) return incoming;
  if (existing.provider !== incoming.provider || existing.model !== incoming.model) return incoming;

  const merged: ModelCatalogEntry = { ...existing };
  if (incoming.displayName !== "") merged.displayName = incoming.displayName;

  const optionalFields = [
    "contextWindow",
    "maxInputTokens",
    "supportsTools",
    "supportsVision",
    "maxOutputTokens",
    "supportsWebSearch",
    "supportsReasoning",
    "inputCostPerMillion",
    "outputCostPerMillion",
    "reasoningEffortLevels",
  ] as const;
  for (const field of optionalFields) {
    const value = incoming[field];
    if (value !== undefined) Object.assign(merged, { [field]: value });
  }
  if (incoming.supportsReasoning === false && incoming.reasoningEffortLevels === undefined) {
    merged.reasoningEffortLevels = [];
  }

  return merged;
}

// Apply a later catalog snapshot without allowing a less-informed response to
// downgrade entries already visible to the pane. The incoming model set still
// owns membership and ordering, so changing cwd/harness cannot retain stale
// models from the previous scope.
export function mergeCatalogSnapshot(previous: ModelCatalog | null, incoming: ModelCatalog): ModelCatalog {
  if (previous === null) return incoming;
  const existing = new Map(previous.models.map((entry) => [key(entry.provider, entry.model), entry]));
  return {
    ...incoming,
    models: incoming.models.map((entry) => mergeCatalogEntry(existing.get(key(entry.provider, entry.model)), entry)),
  };
}
