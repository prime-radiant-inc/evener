import type { NavigationSessionSummary } from "@evener/appwire-client";
import { openAttentionSession } from "../attentionHistory";

/** Refs in the bounded needs-you resource, retaining server order. */
export function needsYouRefs(rows: readonly NavigationSessionSummary[] | null | undefined): string[] {
  return rows ? rows.map((row) => row.ref) : [];
}

export function nextNeedsYouRef(refs: readonly string[], currentRef: string | null): string | null {
  const unique = [...new Set(refs)];
  if (unique.length === 0) return null;
  if (currentRef === null) return unique[0] ?? null;
  const index = unique.indexOf(currentRef);
  if (index < 0) return unique[0] ?? null;
  if (unique.length === 1) return null;
  return unique[(index + 1) % unique.length] ?? null;
}

export function openNeedsYouSession(ref: string): void {
  openAttentionSession(ref);
}
