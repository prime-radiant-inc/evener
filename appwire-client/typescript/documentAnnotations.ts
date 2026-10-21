import { hashAnnotationText } from "./annotationFingerprint";
import type { DocFileContent } from "./docContent";

export type AnnotationNamespace = { kind: "browser"; origin: string } | { kind: "native"; hubId: string };

export interface DocumentKey {
  namespace: AnnotationNamespace;
  sessionRef: string;
  path: string;
}

export interface SourceRange {
  start: number;
  end: number;
}

export interface SourcePosition {
  line: number;
  column: number;
}

export interface DisplayFingerprint {
  algorithm: "cyrb53";
  value: string;
  coverage: SourceRange;
  scope: "displayed-full" | "displayed-prefix";
}

export interface CapturedEvidence {
  wholeRevision?: string;
  fingerprint: DisplayFingerprint;
}

export interface AcceptedSourceLease {
  key: DocumentKey;
  namespaceIncarnation: string;
  epoch: string;
  headRevision: number;
  displayedSnapshotId: string;
}

export interface CapturedPassage {
  evidence: CapturedEvidence;
  sourceRange: SourceRange;
  start: SourcePosition;
  endInclusive: SourcePosition;
  selectedRenderedText: string;
  contextBefore: string;
  contextAfter: string;
  mappingId: string;
  displayedSnapshotId: string;
}

export type DestinationProvenance =
  | { kind: "browser-origin"; paneId: string; compositionOwnerId: string }
  | { kind: "browser-readonly-session"; sourcePaneId: string }
  | { kind: "browser-restored-session" }
  | { kind: "native-session" };

export interface PreparedAnnotation {
  operationId: string;
  lease: AcceptedSourceLease;
  capture: CapturedPassage;
  note: string;
  destination: DestinationProvenance;
}

export interface SaveReceipt {
  operationId: string;
  epoch: string;
  sessionSequence: number;
}

export type HandoffResult =
  | { state: "applied"; draftRevision: number }
  | { state: "already-applied"; draftRevision: number }
  | { state: "pending"; reason: "owner-unavailable" | "dirty-draft" | "revision-conflict" }
  | { state: "cancelled" };

export interface ExpiryReceipt {
  key: DocumentKey;
  namespaceIncarnation: string;
  expiredEpoch: string;
  replacementEpoch: string;
  phase: "fenced" | "deleted";
}

export interface ReadTicket {
  key: DocumentKey;
  namespaceIncarnation: string;
  headRevision: number;
  requestId: string;
}

export type AcceptedReadResult =
  | { state: "accepted"; lease: AcceptedSourceLease }
  | { state: "stale-ticket" }
  | { state: "expiry-pending"; receipt?: ExpiryReceipt };

export type AnnotationRetryReason = "reader-open" | "composer-open" | "foreground" | "storage-recovered";

export interface AnnotationView {
  operation: PreparedAnnotation;
  receipt: SaveReceipt;
  handoff: HandoffResult;
}

export interface AnnotationCommentWork {
  note: string;
  capture?: CapturedPassage;
  captureFrozen: boolean;
}

export interface DocumentAnnotationRepository {
  beginRead(key: DocumentKey): Promise<ReadTicket>;
  acceptRead(ticket: ReadTicket, content: DocFileContent): Promise<AcceptedReadResult>;
  saveAnnotation(operation: PreparedAnnotation): Promise<SaveReceipt>;
  listAnnotations(key: DocumentKey): Promise<AnnotationView[]>;
  finishExpiry(receipt: ExpiryReceipt): Promise<void>;
  retryPending(reason: AnnotationRetryReason): Promise<void>;
}

export function displayedFingerprint(source: string, truncated: boolean): DisplayFingerprint {
  return {
    algorithm: "cyrb53",
    value: hashAnnotationText(source),
    coverage: { start: 0, end: source.length },
    scope: truncated ? "displayed-prefix" : "displayed-full",
  };
}

export function validateSourceRange(source: string, range: SourceRange): void {
  if (
    !Number.isSafeInteger(range.start) ||
    !Number.isSafeInteger(range.end) ||
    range.start < 0 ||
    range.end > source.length ||
    range.start >= range.end ||
    splitsSurrogatePair(source, range.start) ||
    splitsSurrogatePair(source, range.end)
  ) {
    throw new RangeError("source range must be non-empty, in bounds, and preserve UTF-16 surrogate pairs");
  }
}

function splitsSurrogatePair(source: string, boundary: number): boolean {
  if (boundary <= 0 || boundary >= source.length) return false;
  const before = source.charCodeAt(boundary - 1);
  const after = source.charCodeAt(boundary);
  return before >= 0xd800 && before <= 0xdbff && after >= 0xdc00 && after <= 0xdfff;
}

function sourcePositionAt(source: string, index: number): SourcePosition {
  let line = 1;
  let column = 1;
  for (let offset = 0; offset < index; offset += 1) {
    const unit = source[offset];
    if (unit === "\r") {
      if (source[offset + 1] !== "\n") {
        line += 1;
        column = 1;
      }
    } else if (unit === "\n") {
      line += 1;
      column = 1;
    } else {
      column += 1;
    }
  }
  return { line, column };
}

export function sourcePositions(
  source: string,
  range: SourceRange,
): { start: SourcePosition; endInclusive: SourcePosition } {
  validateSourceRange(source, range);
  return {
    start: sourcePositionAt(source, range.start),
    endInclusive: sourcePositionAt(source, range.end - 1),
  };
}

export function compareDisplayedSources(
  captured: { source: string; evidence: CapturedEvidence },
  current: { source: string; evidence: CapturedEvidence },
): { changed: boolean; detection: "whole-revision" | "comparable-source" | "insufficient-coverage" } {
  if (
    captured.evidence.wholeRevision !== undefined &&
    current.evidence.wholeRevision !== undefined &&
    captured.evidence.wholeRevision !== current.evidence.wholeRevision
  ) {
    return { changed: true, detection: "whole-revision" };
  }

  const capturedFingerprint = captured.evidence.fingerprint;
  const currentFingerprint = current.evidence.fingerprint;
  if (capturedFingerprint.scope === "displayed-full" && currentFingerprint.scope === "displayed-full") {
    return { changed: captured.source !== current.source, detection: "comparable-source" };
  }

  const capturedCoverage = capturedFingerprint.coverage;
  const currentCoverage = currentFingerprint.coverage;
  // A complete observation pins the document's true end, and a truncated
  // observation proves content existed beyond its coverage (the doc read
  // marks truncation only when the file exceeded the cap). In a mixed
  // comparison those facts make a definite change the overlap comparison
  // cannot see: truncated coverage at or beyond the complete end means
  // content appeared past a known EOF, vanished while the complete end is
  // now earlier, or the truncated side's proven continuation no longer
  // fits within the complete observation. Only a complete end past the
  // truncated coverage keeps real uncertainty about the unread middle.
  if (capturedFingerprint.scope !== currentFingerprint.scope) {
    const completeCoverage = capturedFingerprint.scope === "displayed-full" ? capturedCoverage : currentCoverage;
    const truncatedCoverage = capturedFingerprint.scope === "displayed-prefix" ? capturedCoverage : currentCoverage;
    if (truncatedCoverage.end >= completeCoverage.end) {
      return { changed: true, detection: "comparable-source" };
    }
  }
  const overlapStart = Math.max(capturedCoverage.start, currentCoverage.start, 0);
  const overlapEnd = Math.min(capturedCoverage.end, currentCoverage.end, captured.source.length, current.source.length);
  if (overlapStart >= overlapEnd) return { changed: false, detection: "insufficient-coverage" };

  if (captured.source.slice(overlapStart, overlapEnd) !== current.source.slice(overlapStart, overlapEnd)) {
    return { changed: true, detection: "comparable-source" };
  }

  const sameCoverage =
    capturedCoverage.start === currentCoverage.start &&
    capturedCoverage.end === currentCoverage.end &&
    captured.source.length === current.source.length;
  return sameCoverage
    ? { changed: false, detection: "comparable-source" }
    : { changed: false, detection: "insufficient-coverage" };
}

export function contextForRange(source: string, range: SourceRange): { before: string; after: string } {
  validateSourceRange(source, range);
  const paragraphs = paragraphRanges(source);
  let first = paragraphs.findIndex((paragraph) => range.start >= paragraph.start && range.start <= paragraph.end);
  let last = paragraphs.findIndex((paragraph) => range.end - 1 >= paragraph.start && range.end - 1 < paragraph.end);
  if (first < 0) first = 0;
  if (last < 0) last = paragraphs.length - 1;

  const firstSelected = first;
  const lastSelected = last;
  // The window after `step` expansions of each expandable side. Uniqueness is
  // monotone in the step - a window that contains a unique window is unique,
  // because any second occurrence of the larger window would contain a second
  // occurrence of the smaller one - so binary search finds the same first
  // unique window the one-at-a-time expansion stopped at, without scanning
  // every intermediate window: a document of repeated paragraphs stays a
  // handful of whole-source searches instead of one per paragraph.
  const windowEnd = (step: number): number =>
    paragraphs[Math.min(lastSelected + step, paragraphs.length - 1)]?.end ?? source.length;
  const windowStart = (step: number): number => paragraphs[Math.max(firstSelected - step, 0)]?.start ?? 0;
  const uniqueAt = (step: number): boolean => {
    const window = source.slice(windowStart(step), windowEnd(step));
    return !occursMoreThanOnce(source, window);
  };
  const maxStep = Math.max(firstSelected, paragraphs.length - 1 - lastSelected);
  let step: number;
  if (uniqueAt(0)) {
    step = 0;
  } else {
    let low = 1;
    let high = maxStep;
    let firstUnique = -1;
    while (low <= high) {
      const mid = low + ((high - low) >> 1);
      if (uniqueAt(mid)) {
        firstUnique = mid;
        high = mid - 1;
      } else {
        low = mid + 1;
      }
    }
    // No unique window exists (the whole document repeats): the expansion ran
    // to its limit, same as the one-at-a-time loop's terminal state.
    step = firstUnique >= 0 ? firstUnique : maxStep;
  }
  const contextStart = windowStart(step);
  const contextEnd = windowEnd(step);
  return { before: source.slice(contextStart, range.start), after: source.slice(range.end, contextEnd) };
}

interface ParagraphRange {
  start: number;
  end: number;
}

function paragraphRanges(source: string): ParagraphRange[] {
  const ranges: ParagraphRange[] = [];
  const separator = /\r?\n[\t ]*\r?\n/g;
  let start = 0;
  for (let match = separator.exec(source); match !== null; match = separator.exec(source)) {
    ranges.push({ start, end: match.index });
    start = match.index + match[0].length;
  }
  ranges.push({ start, end: source.length });
  return ranges;
}

function occursMoreThanOnce(source: string, needle: string): boolean {
  if (needle === "") return false;
  const first = source.indexOf(needle);
  return first >= 0 && source.indexOf(needle, first + 1) >= 0;
}

export function formatAnnotationReference(capture: CapturedPassage, path: string, note: string): string {
  const { start, endInclusive } = capture;
  // The locator sits inside a code span, but send-time marker translation
  // matches there too: a filename shaped like a staged marker would be
  // rewritten into an attachment reference and corrupt the locator.
  const locator = `${literalPath(encodeAttachmentTokens(path))}:${start.line}:${start.column}-${endInclusive.line}:${endInclusive.column}`;
  const selected = encodeAttachmentTokens(capture.selectedRenderedText);
  const selection = needsJSONSelection(selected) ? JSON.stringify(selected) : `“${selected}”`;
  const evidence = capture.evidence.wholeRevision
    ? `Captured whole-file revision: ${JSON.stringify(encodeAttachmentTokens(capture.evidence.wholeRevision))}.`
    : fingerprintReference(capture.evidence.fingerprint);
  return [
    `A note on ${markdownCode(locator)} (captured source positions), selecting ${selection}: ${JSON.stringify(encodeAttachmentTokens(note))}`,
    evidence,
    `Context before: ${JSON.stringify(encodeAttachmentTokens(capture.contextBefore))}; after: ${JSON.stringify(encodeAttachmentTokens(capture.contextAfter))}.`,
    "Verify the quotation and context against current content before relying on these captured coordinates.",
  ].join("\n");
}

// Breaks markerPattern()'s /\[image (\d+)\]/g match with an invisible U+200B
// after the bracket; the captured text still reads identically.
function encodeAttachmentTokens(value: string): string {
  return value.replace(/\[image (\d+)\]/g, "[\u200bimage $1]");
}

function needsJSONSelection(selection: string): boolean {
  for (const character of selection) {
    const code = character.codePointAt(0) ?? 0;
    if (character === "“" || character === "”" || code < 0x20 || code === 0x7f) return true;
  }
  return false;
}

function literalPath(path: string): string {
  let encoded = "";
  for (const character of path) {
    const code = character.codePointAt(0) ?? 0;
    switch (character) {
      case "\\":
        encoded += "\\\\";
        break;
      case "\b":
        encoded += "\\b";
        break;
      case "\t":
        encoded += "\\t";
        break;
      case "\n":
        encoded += "\\n";
        break;
      case "\f":
        encoded += "\\f";
        break;
      case "\r":
        encoded += "\\r";
        break;
      default:
        encoded += code < 0x20 || code === 0x7f ? `\\u${code.toString(16).padStart(4, "0")}` : character;
    }
  }
  return encoded;
}

function markdownCode(value: string): string {
  let longestRun = 0;
  for (const match of value.matchAll(/`+/g)) longestRun = Math.max(longestRun, match[0].length);
  const delimiter = "`".repeat(longestRun + 1);
  const pad = longestRun > 0 || value.startsWith(" ") || value.endsWith(" ");
  return `${delimiter}${pad ? " " : ""}${value}${pad ? " " : ""}${delimiter}`;
}

function fingerprintReference(fingerprint: DisplayFingerprint): string {
  const range = `[${fingerprint.coverage.start},${fingerprint.coverage.end})`;
  if (fingerprint.scope === "displayed-prefix") {
    return `Captured displayed-prefix reference: ${fingerprint.algorithm} ${JSON.stringify(fingerprint.value)}, covering UTF-16 source range ${range}; content after this displayed prefix was not captured.`;
  }
  return `Captured displayed-source reference: ${fingerprint.algorithm} ${JSON.stringify(fingerprint.value)}, covering UTF-16 source range ${range}.`;
}

export function selectCommentPassage(work: AnnotationCommentWork, capture: CapturedPassage): AnnotationCommentWork {
  if (work.captureFrozen && work.capture !== undefined) return work;
  return { ...work, capture, captureFrozen: work.note.length > 0 };
}

export function editCommentText(work: AnnotationCommentWork, note: string): AnnotationCommentWork {
  return { ...work, note, captureFrozen: work.capture !== undefined ? true : work.captureFrozen };
}

export function invalidateCommentCapture(work: AnnotationCommentWork): AnnotationCommentWork {
  return { note: work.note, captureFrozen: false };
}
