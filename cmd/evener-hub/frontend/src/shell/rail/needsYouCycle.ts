import type { NavigationSessionSummary } from "../../protocol/types.gen";
import { navigate, paneToURL } from "../routing";

/** Refs in the bounded needs-you resource, retaining server order. */
export function needsYouRefs(rows: readonly NavigationSessionSummary[] | null | undefined | unknown): string[] {
  if (Array.isArray(rows)) return rows.map((row) => row.ref);
  if (rows && typeof rows === "object" && "needs_you" in rows) {
    const legacyRows = (rows as { needs_you?: unknown }).needs_you;
    if (Array.isArray(legacyRows)) return legacyRows.map((row) => (row as { ref: string }).ref);
  }
  return [];
}

export function nextNeedsYouRef(refs: readonly string[], currentRef: string | null): string | null {
  if (refs.length === 0) return null;
  if (currentRef === null) return refs[0] ?? null;
  const index = refs.indexOf(currentRef);
  const next = refs[(index < 0 ? 0 : index + 1) % refs.length];
  return next ?? null;
}

export function openNeedsYouSession(ref: string): void {
  const url = paneToURL("session", { ref });
  if (url) navigate(url);
}
