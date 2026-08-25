// Merges a harness/cwd-scoped model list (the spawn form's model/list result,
// authoritative for what is launchable) with the global /api/models enrichment
// (display names, capability badges, cost, Recent, diagnostics). The scoped
// list drives the SET - so a non-default harness never shows the wrong models -
// while the enrichment supplies the metadata model/list doesn't carry. A scoped
// model the enrichment doesn't know degrades to a label-only entry; Recent is
// filtered to what's actually offered in scope. This is how the spawn site
// keeps its scoping AND gains the rich catalog without the injecting Spawn form
// having to change.
import type { ModelDescriptor } from "../../protocol/types.gen";
import type { ModelCatalog, ModelCatalogEntry } from "./index";

function key(provider: string, model: string): string {
  return `${provider}/${model}`;
}

// Catalog responses can arrive from more than one loader. Keep the stable
// provider/model identity from the existing entry, and only let the incoming
// response replace fields it actually knows. In particular, a scoped-list
// fallback has displayName === "" and no capability fields; it must not erase
// metadata a picker or an earlier enrichment already supplied.
export function mergeCatalogEntry(
  existing: ModelCatalogEntry | undefined,
  incoming: ModelCatalogEntry,
): ModelCatalogEntry {
  if (existing === undefined) return incoming;

  const merged: ModelCatalogEntry = { ...existing };
  if (incoming.displayName !== "") merged.displayName = incoming.displayName;

  const optionalFields = [
    "contextWindow",
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

  return { ...merged, provider: existing.provider, model: existing.model };
}

// Apply a later catalog snapshot without allowing a less-informed snapshot to
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

export function mergeScopedCatalog(scoped: ModelDescriptor[], enrichment: ModelCatalog | null): ModelCatalog {
  const byKey = new Map<string, ModelCatalogEntry>();
  for (const entry of enrichment?.models ?? []) byKey.set(key(entry.provider, entry.model), entry);

  const models = scoped
    .filter((d) => d.provider !== "" && d.model !== "")
    .map((d) => byKey.get(key(d.provider, d.model)) ?? { provider: d.provider, model: d.model, displayName: "" });

  const offered = new Set(models.map((m) => key(m.provider, m.model)));
  const recent = (enrichment?.recent ?? []).filter((r) => offered.has(key(r.provider, r.model)));

  return { models, recent, diagnostics: enrichment?.diagnostics ?? [] };
}
