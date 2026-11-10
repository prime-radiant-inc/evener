// Older-history frontier provenance (#5585): the cursor and the statement it
// makes about the frontier always travel together. Kept in its own module so
// both transcriptHistory.ts and turnHistoryMerge.ts can write the pair without
// importing each other.

import type { OlderFrontier, ThreadModel } from "./model";

/** The frontier provenance a source response names: its cursor, or a
 * source-named end of history when it carries none. */
export function frontierFromCursor(resp: { olderCursor?: string }): OlderFrontier {
  return resp.olderCursor ?? "none";
}

/** A model's frontier provenance. A model that never recorded one — a cache
 * seed, a hand-built model, or a lean read that never asked for history — is
 * explicitly UNKNOWN, never "none". */
export function olderFrontierOf(model: Pick<ThreadModel, "olderFrontier">): OlderFrontier {
  return model.olderFrontier ?? "unknown";
}

/** True only when a source read or page named the end of older history: no
 * cursor AND recorded provenance "none". A cache seed's unknown frontier is
 * never exhausted. */
export function olderHistoryExhausted(model: Pick<ThreadModel, "olderCursor" | "olderFrontier">): boolean {
  return model.olderCursor === undefined && olderFrontierOf(model) === "none";
}

/** The one place a source-named frontier is written: the opaque cursor and its
 * provenance always change together. */
export function withOlderFrontier<M extends ThreadModel>(model: M, cursor: string | undefined): M {
  return { ...model, olderCursor: cursor, olderFrontier: cursor === undefined ? "none" : cursor };
}
