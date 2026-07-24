// The picker's list shape: one flat row array combining Recent, the
// provider-grouped models, and one dim in-place line per provider the hub
// couldn't reach. Pure (no React, no wire) and built on catalogView's
// filter/group helpers, so the panel component only maps rows to markup.
//
// Why rows and not nested groups: the panel is an ARIA listbox whose options
// must be linearly navigable by ArrowUp/Down. A flat array with a `kind`
// discriminant makes "skip the heads and the unavailable lines" a filter
// (pickableRows) instead of a tree walk.
import {
  type CatalogOption,
  capabilityLabels,
  contextWindowLabel,
  filterCatalog,
  formatCost,
  toCatalogOptions,
  withGroupHeads,
} from "./catalogView";
import type { ModelCatalog, ModelCatalogDiagnostic, ModelCatalogEntry } from "./index";

export type PickerRow =
  | { kind: "group"; key: string; label: string }
  | { kind: "model"; key: string; option: CatalogOption; meta: string }
  | { kind: "unavailable"; key: string; text: string };

/** The pickable row kind - the only one that becomes a listbox option. */
export type PickerModelRow = Extract<PickerRow, { kind: "model" }>;

/** The Recent group's head. Recent is a pseudo-provider: it mixes providers,
 * so its rows lead their meta with the provider name. */
const RECENT_GROUP = "Recent";

/** One row's small-text metadata: capabilities, cost, context window - and,
 * for the mixed-provider Recent group, the provider first. Empty when the
 * entry carries no metadata at all (a model the embedded catalog doesn't
 * know), so the row renders as just its name rather than a stray separator. */
export function rowMeta(entry: ModelCatalogEntry, withProvider: boolean): string {
  const parts: string[] = [];
  if (withProvider) parts.push(entry.provider);
  parts.push(...capabilityLabels(entry));
  const cost = formatCost(entry);
  if (cost !== null) parts.push(cost);
  const context = contextWindowLabel(entry);
  if (context !== null) parts.push(context);
  return parts.join(" · ");
}

/** A diagnostic as one line: who, what, and what to do about it. The label
 * prefers the provider name (that's what the user was looking for in the
 * list), falling back to the diagnostic's own title/source before a generic
 * word - a launch check that names neither still reads as a sentence. */
export function unavailableLine(diag: ModelCatalogDiagnostic): string {
  const label = diag.provider || diag.title || diag.source || "provider";
  const hint = diag.hint ? ` — ${diag.hint}` : "";
  return `${label} — ${diag.message}${hint}`;
}

/** An unavailable line is only about its provider, so it survives a query
 * that matches that provider's name and filters out like any other non-match
 * otherwise. */
function diagnosticMatches(diag: ModelCatalogDiagnostic, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return (diag.provider ?? "").toLowerCase().includes(q);
}

/**
 * The full list for a given query: Recent first (when non-empty), then the
 * provider groups in the server's own order, then the unavailable providers.
 * An empty query yields everything - the list is always expanded, never
 * gated behind a keystroke.
 *
 * Keys are prefixed by section and suffixed by position, so a model that
 * appears BOTH in Recent and in its provider group gets two distinct DOM
 * ids, and a provider that (unsorted) opens two runs doesn't collide.
 */
export function buildPickerRows(catalog: ModelCatalog | null, query: string): PickerRow[] {
  if (!catalog) return [];
  const rows: PickerRow[] = [];

  const recent = filterCatalog(toCatalogOptions(catalog.recent), query);
  if (recent.length > 0) {
    rows.push({ kind: "group", key: `group:${rows.length}:${RECENT_GROUP}`, label: RECENT_GROUP });
    for (const option of recent) {
      rows.push({
        kind: "model",
        key: `recent:${rows.length}:${option.qualified}`,
        option,
        meta: rowMeta(option.entry, true),
      });
    }
  }

  for (const option of withGroupHeads(filterCatalog(toCatalogOptions(catalog.models), query))) {
    if (option.groupHead !== undefined) {
      rows.push({ kind: "group", key: `group:${rows.length}:${option.groupHead}`, label: option.groupHead });
    }
    rows.push({
      kind: "model",
      key: `model:${rows.length}:${option.qualified}`,
      option,
      meta: rowMeta(option.entry, false),
    });
  }

  for (const diag of catalog.diagnostics ?? []) {
    if (!diagnosticMatches(diag, query)) continue;
    rows.push({
      kind: "unavailable",
      key: `unavailable:${rows.length}:${diag.provider ?? ""}`,
      text: unavailableLine(diag),
    });
  }

  return rows;
}

/** The rows the keyboard walks and a click can pick: models only. Group heads
 * and unavailable lines are text, not options. */
export function pickableRows(rows: PickerRow[]): PickerModelRow[] {
  return rows.filter((row): row is PickerModelRow => row.kind === "model");
}
