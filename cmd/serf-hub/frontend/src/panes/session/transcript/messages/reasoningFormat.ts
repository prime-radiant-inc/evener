// Pure helpers for the reasoning ("think block") item type. See
// ThinkBlock.tsx for the component that consumes these.
import { firstLine } from "./format";

// joinedReasoningParagraphs turns ItemModel.reasoningSummaries (string[][] -
// per-summaryIndex chunk lists, protocol/model.ts) into one string per
// summaryIndex, dropping any paragraph that joins to nothing (or
// whitespace-only) - "empty thoughts removed" per the wave-4 T2 scope, at
// both the whole-item level (settle finds zero paragraphs -> render
// nothing, see ThinkBlock) and the per-paragraph level.
export function joinedReasoningParagraphs(summaries: string[][] | undefined): string[] {
  if (!summaries) return [];
  return summaries.map((chunks) => chunks.join("")).filter((text) => text.trim() !== "");
}

// reasoningPreview is the first line of the first non-empty paragraph,
// clipped - the settled think block's collapsed-summary preview text.
export function reasoningPreview(summaries: string[][] | undefined, maxLen = 80): string {
  const paragraphs = joinedReasoningParagraphs(summaries);
  if (paragraphs.length === 0) return "";
  return firstLine(paragraphs[0]!, maxLen);
}

// thoughtSeconds computes whole elapsed seconds from two REAL ISO
// timestamps - never a client wall clock ("never synthesized from the
// client's wall clock" is this codebase's own standing rule, see
// parity-m4-transcript.md's tool-meta-timing entries). The wire pair
// (startedAt/completedAt) wins when present; otherwise falls back to the
// client-observed arrival pair (observedStartedAt/observedCompletedAt - see
// ItemModel's own comment in model.ts). Both absent (hydrated/historical
// items) yields no duration rather than inventing one. Floors at 1s so a
// genuine sub-second thought is never "0s".
export function thoughtSeconds(
  startedAt: string | undefined,
  completedAt: string | undefined,
  observedStartedAt?: string,
  observedCompletedAt?: string,
): number | undefined {
  return elapsedSeconds(startedAt, completedAt) ?? elapsedSeconds(observedStartedAt, observedCompletedAt);
}

function elapsedSeconds(startedAt: string | undefined, completedAt: string | undefined): number | undefined {
  if (!startedAt || !completedAt) return undefined;
  const start = Date.parse(startedAt);
  const end = Date.parse(completedAt);
  if (!Number.isFinite(start) || !Number.isFinite(end)) return undefined;
  return Math.max(1, Math.round((end - start) / 1000));
}
