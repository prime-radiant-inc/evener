// Pure helpers for the reasoning ("think block") item type. See
// ThinkBlock.tsx for the component that consumes these.

import { Marked, type Token, type Tokens } from "marked";

// Reuse the app's existing Markdown tokenizer so nested links, block prefixes,
// and emphasis are handled as syntax rather than accumulated regex cases.
const markdownLexer = new Marked({ gfm: true });

const ISO_TIMESTAMP = /^(\d{4}|[+-]\d{6})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:\d{2})$/;

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
  const start = parseTimestamp(startedAt);
  const end = parseTimestamp(completedAt);
  if (start === undefined || end === undefined) return undefined;
  const elapsed = end - start;
  return elapsed >= 0 ? elapsed : undefined;
}

function parseTimestamp(value: string): number | undefined {
  const match = ISO_TIMESTAMP.exec(value);
  if (!match) return undefined;

  const [, yearText, monthText, dayText, hourText, minuteText, secondText, fractionText, zone] = match;
  const timestamp = Date.parse(value);
  if (!Number.isFinite(timestamp)) return undefined;

  const year = Number(yearText);
  const month = Number(monthText);
  const day = Number(dayText);
  const hour = Number(hourText);
  const minute = Number(minuteText);
  const second = Number(secondText);
  const milliseconds = Number((fractionText ?? "").padEnd(3, "0").slice(0, 3));
  if (month < 1 || month > 12 || day < 1 || day > daysInMonth(year, month)) return undefined;

  const offsetMinutes = zone === "Z" ? 0 : zone ? parseOffsetMinutes(zone) : undefined;
  if (offsetMinutes === undefined) return undefined;

  // Date.parse converts the local wall-clock components to an instant. Add
  // the explicit offset back before comparing those components, which catches
  // calendar normalization such as February 30 without rejecting offsets.
  const local = new Date(timestamp + offsetMinutes * 60_000);
  if (
    local.getUTCFullYear() !== year ||
    local.getUTCMonth() !== month - 1 ||
    local.getUTCDate() !== day ||
    local.getUTCHours() !== hour ||
    local.getUTCMinutes() !== minute ||
    local.getUTCSeconds() !== second ||
    local.getUTCMilliseconds() !== milliseconds
  ) {
    return undefined;
  }
  return timestamp;
}

function daysInMonth(year: number, month: number): number {
  if (month === 2) return year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28;
  return month === 4 || month === 6 || month === 9 || month === 11 ? 30 : 31;
}

function parseOffsetMinutes(zone: string): number | undefined {
  const sign = zone[0] === "-" ? -1 : 1;
  const hours = Number(zone.slice(1, 3));
  const minutes = Number(zone.slice(4, 6));
  if (hours > 23 || minutes > 59) return undefined;
  return sign * (hours * 60 + minutes);
}

export function formatThoughtDuration(durationMs: number): string {
  if (durationMs === 0) return "0ms";
  const rounded = Math.max(1, Math.round(durationMs));
  if (durationMs < 1_000) return `${rounded}ms`;
  if (durationMs < 10_000) {
    const seconds = (rounded / 1_000).toFixed(1);
    return `${seconds === "10.0" ? seconds : seconds.replace(/\.0$/, "")}s`;
  }
  return `${Math.round(rounded / 1_000)}s`;
}

function plainTextFromToken(token: Token): string {
  switch (token.type) {
    case "image":
      return token.text;
    case "link":
      return token.tokens?.map(plainTextFromToken).join("") ?? token.text;
    case "code":
    case "codespan":
    case "escape":
    case "html":
      return token.text;
    case "checkbox":
      return "";
    case "br":
    case "space":
      return " ";
    case "list":
      return token.items.map((item: Tokens.ListItem) => item.tokens.map(plainTextFromToken).join("")).join(" ");
    case "table":
      return [...token.header, ...token.rows.flat()]
        .map((cell) => cell.tokens.map(plainTextFromToken).join(""))
        .join(" ");
    default:
      if ("tokens" in token && token.tokens) return token.tokens.map(plainTextFromToken).join("");
      if ("text" in token && typeof token.text === "string") return token.text;
      return "";
  }
}

function plainThoughtLine(line: string): string {
  return markdownLexer.lexer(line).map(plainTextFromToken).join(" ").replace(/\s+/g, " ").trim();
}

export function lastMeaningfulThoughtLine(paragraphs: string[], maxLength: number): string {
  for (let paragraphIndex = paragraphs.length - 1; paragraphIndex >= 0; paragraphIndex--) {
    const lines = paragraphs[paragraphIndex]?.split(/\r?\n/) ?? [];
    for (let lineIndex = lines.length - 1; lineIndex >= 0; lineIndex--) {
      const line = plainThoughtLine(lines[lineIndex] ?? "");
      if (line.length === 0) continue;
      const codePoints = Array.from(line);
      if (codePoints.length <= maxLength) return line;
      return `${codePoints.slice(0, maxLength).join("").trimEnd()}…`;
    }
  }
  return "";
}
