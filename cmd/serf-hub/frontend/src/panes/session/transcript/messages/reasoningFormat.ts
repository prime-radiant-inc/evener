// Pure helpers for the reasoning ("think block") item type. See
// ThinkBlock.tsx for the component that consumes these.

import { formatDurationMs } from "./format";

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

// thoughtDurationMs computes elapsed milliseconds from two REAL ISO timestamps
// - never a client wall clock ("never synthesized from the client's wall
// clock" is this codebase's own standing rule, see parity-m4-transcript.md's
// tool-meta-timing entries). The wire pair (startedAt/completedAt) wins when
// it is valid; otherwise this falls back to the client-observed arrival pair
// (observedStartedAt/observedCompletedAt - see ItemModel's own comment in
// model.ts). Both absent (hydrated/historical items) yields no duration rather
// than inventing one. Backwards pairs are unavailable rather than turned into
// a positive-looking duration.
export function thoughtDurationMs(
  startedAt: string | undefined,
  completedAt: string | undefined,
  observedStartedAt?: string,
  observedCompletedAt?: string,
): number | undefined {
  return elapsedMilliseconds(startedAt, completedAt) ?? elapsedMilliseconds(observedStartedAt, observedCompletedAt);
}

function elapsedMilliseconds(startedAt: string | undefined, completedAt: string | undefined): number | undefined {
  if (!startedAt || !completedAt) return undefined;
  const start = Date.parse(startedAt);
  const end = Date.parse(completedAt);
  if (!Number.isFinite(start) || !Number.isFinite(end)) return undefined;
  const elapsed = end - start;
  return elapsed >= 0 ? elapsed : undefined;
}

export function formatThoughtDuration(durationMs: number): string {
  return durationMs === 0 ? "0ms" : formatDurationMs(durationMs);
}

// A collapsed summary is plain text, so remove the small set of Markdown
// decoration that would otherwise make the context look like source syntax.
// This is intentionally not a Markdown parser: the expanded body remains the
// only full rendering, and the summary only needs a readable final line.
function plainThoughtLine(line: string): string {
  return line
    .replace(/^\s{0,3}(?:#{1,6}\s+|[-+*]\s+|\d+[.)]\s+|>\s+)/, "")
    .replace(/!?(\[([^\]]+)\])\([^)]*\)/g, "$2")
    .replace(/(`{1,3}|\*{1,3}|~~)/g, "")
    .trim();
}

export function lastMeaningfulThoughtLine(paragraphs: string[], maxLength: number): string {
  for (let paragraphIndex = paragraphs.length - 1; paragraphIndex >= 0; paragraphIndex--) {
    const lines = paragraphs[paragraphIndex]?.split(/\r?\n/) ?? [];
    for (let lineIndex = lines.length - 1; lineIndex >= 0; lineIndex--) {
      const line = plainThoughtLine(lines[lineIndex] ?? "");
      if (line.length === 0) continue;
      if (line.length <= maxLength) return line;
      return `${line.slice(0, maxLength).trimEnd()}…`;
    }
  }
  return "";
}
