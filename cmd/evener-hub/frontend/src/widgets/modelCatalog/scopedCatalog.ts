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
