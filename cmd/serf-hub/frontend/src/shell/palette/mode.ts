// The palette hosts three modes over one shared input + results list, and
// recomputes the active mode from state on every keystroke (parity-m6-
// surfaces.md §2.2, search.js:123-127). Priority is fixed: an in-progress
// command (args mode) wins over a "/"-prefixed filter, which wins over free
// search.

export type PaletteMode = "search" | "command-filter" | "command-args";

export interface ModeInput {
  query: string;
  hasSelectedCommand: boolean;
}

export function computeMode({ query, hasSelectedCommand }: ModeInput): PaletteMode {
  if (hasSelectedCommand) return "command-args";
  if (query.startsWith("/")) return "command-filter";
  return "search";
}
