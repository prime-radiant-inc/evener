// Pure view helpers for the rich model catalog: turning the /api/models
// entries into the searchable Combobox's options, provider grouping, and the
// per-row metadata (capability badges, cost, context window). No React, no
// wire - unit-tested in isolation (catalogView.test.ts). The widget
// (index.tsx) composes these into rows; the swap sites never see them.
import type { ComboboxOption } from "../combobox";
import type { ModelCatalogEntry } from "./index";

/** A Combobox option plus the catalog metadata its rich row renders. */
export interface CatalogOption extends ComboboxOption {
  qualified: string; // "provider/model", the value emitted on pick
  entry: ModelCatalogEntry;
  /** Provider name on the first option of each provider run (a group head);
   * undefined on the rest, so the rendered list reads as provider sections. */
  groupHead?: string;
}

/** Maps entries to options, preserving the server's provider-sorted order.
 * The label is the display name, falling back to the qualified id for a model
 * the embedded catalog doesn't know (its display_name still arrives, but an
 * empty one must not render a blank row). */
export function toCatalogOptions(entries: ModelCatalogEntry[]): CatalogOption[] {
  return entries.map((entry) => {
    const qualified = `${entry.provider}/${entry.model}`;
    return { id: qualified, label: entry.displayName || qualified, qualified, entry };
  });
}

/** Filters options by a free-text query against the display name, the raw
 * model id, and the provider - so a user can narrow by vendor, by the friendly
 * name, or by the exact id the prettifier reshaped. */
export function filterCatalog(options: CatalogOption[], query: string): CatalogOption[] {
  const q = query.trim().toLowerCase();
  if (!q) return options;
  return options.filter((opt) => {
    const { provider, model } = opt.entry;
    return opt.label.toLowerCase().includes(q) || model.toLowerCase().includes(q) || provider.toLowerCase().includes(q);
  });
}

/** Annotates each option that opens a new provider run with a groupHead, so
 * the flat listbox reads as provider-grouped sections. Runs on the filtered
 * list (the heads must reflect what's actually shown). */
export function withGroupHeads(options: CatalogOption[]): CatalogOption[] {
  let prev: string | undefined;
  return options.map((opt) => {
    const groupHead = opt.entry.provider !== prev ? opt.entry.provider : undefined;
    prev = opt.entry.provider;
    return { ...opt, groupHead };
  });
}

/** The true capability flags as short badge labels, in a fixed order. A
 * neutral metadata list (color-is-attention: capabilities never earn color). */
export function capabilityLabels(entry: ModelCatalogEntry): string[] {
  const labels: string[] = [];
  if (entry.supportsTools) labels.push("tools");
  if (entry.supportsVision) labels.push("vision");
  if (entry.supportsWebSearch) labels.push("web search");
  if (entry.supportsReasoning) labels.push("reasoning");
  return labels;
}

function trimCost(n: number): string {
  return n.toFixed(2).replace(/\.?0+$/, "");
}

/** Input/output dollar cost per million tokens, or null when the entry carries
 * no pricing (an unknown model). A free ($0) model is shown honestly, not
 * hidden - zero cost is information. */
export function formatCost(entry: ModelCatalogEntry): string | null {
  const parts: string[] = [];
  if (entry.inputCostPerMillion !== undefined) parts.push(`$${trimCost(entry.inputCostPerMillion)} in`);
  if (entry.outputCostPerMillion !== undefined) parts.push(`$${trimCost(entry.outputCostPerMillion)} out`);
  if (parts.length === 0) return null;
  return `${parts.join(" · ")} /Mtok`;
}

/** The context window abbreviated (200k, 1M), or null when unknown. */
export function contextWindowLabel(entry: ModelCatalogEntry): string | null {
  const cw = entry.contextWindow;
  if (cw === undefined) return null;
  if (cw >= 1_000_000) return `${cw / 1_000_000}M`;
  if (cw >= 1000) return `${Math.round(cw / 1000)}k`;
  return String(cw);
}
