/**
 * Deterministic sentence chunker for text-to-speech.
 *
 * Splits assistant prose Markdown into speakable sentence chunks. Code fences
 * are skipped entirely; Markdown link text is extracted (not the URL); list
 * items are spoken individually; incomplete trailing sentences are held until
 * more text arrives or the turn is flushed.
 *
 * Pure function — no side effects, no wall clock. Given the same text and
 * options it always produces the same chunks with the same IDs.
 */

export interface VoiceChunk {
  /** Deterministic ID: {voiceSessionId}:{turnId}:{itemIndex}:{rangeStart}-{rangeEnd} */
  readonly id: string;
  /** Speakable text (link text extracted, whitespace collapsed). */
  readonly text: string;
  /** Start offset in the original assistant Markdown. */
  readonly rangeStart: number;
  /** End offset in the original assistant Markdown (exclusive). */
  readonly rangeEnd: number;
}

export interface ChunkOptions {
  readonly voiceSessionId: string;
  readonly turnId: string;
  readonly itemIndex: number;
  /** Offset in the original Markdown where `text` begins. */
  readonly baseOffset: number;
  /** When true, emit incomplete trailing text (turn finished). */
  readonly flush?: boolean;
}

export interface ChunkResult {
  readonly chunks: VoiceChunk[];
  /** Offset in the original Markdown up to which content is consumed. */
  readonly consumedTo: number;
}

const CODE_FENCE = "```";
const FALLBACK_LIMIT = 180;

// Safe string char access — returns "" for out-of-range (never undefined).
function charAt(text: string, i: number): string {
  return i >= 0 && i < text.length ? (text[i] ?? "") : "";
}

const ABBREVIATIONS = new Set<string>([
  "Mr",
  "Mrs",
  "Ms",
  "Dr",
  "Prof",
  "Sr",
  "Jr",
  "St",
  "vs",
  "etc",
  "Inc",
  "Ltd",
  "Co",
  "Corp",
  "No",
  "Fig",
  "Vol",
  "Ch",
  "pp",
  "Rev",
  "Hon",
  "Pres",
  "Rep",
  "Sen",
  "Gov",
  "Gen",
  "Col",
  "Capt",
  "Lt",
  "Sgt",
  "Cpl",
  "Pvt",
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
]);

const SENTENCE_PUNCT = new Set([".", "!", "?", "。", "！", "？"]);

// ---------------------------------------------------------------------------
// Markdown link matching: [text](url) → extract "text"
// ---------------------------------------------------------------------------

interface LinkMatch {
  linkText: string;
  end: number;
}

function matchLink(text: string, start: number): LinkMatch | null {
  if (charAt(text, start) !== "[") return null;
  let i = start + 1;
  let depth = 1;
  while (i < text.length && depth > 0) {
    if (charAt(text, i) === "[") depth++;
    else if (charAt(text, i) === "]") {
      depth--;
      if (depth === 0) break;
    }
    i++;
  }
  if (depth !== 0 || i >= text.length) return null;
  const closeBracket = i;
  if (charAt(text, closeBracket + 1) !== "(") return null;
  let j = closeBracket + 2;
  let pdepth = 1;
  while (j < text.length && pdepth > 0) {
    if (charAt(text, j) === "(") pdepth++;
    else if (charAt(text, j) === ")") {
      pdepth--;
      if (pdepth === 0) break;
    }
    j++;
  }
  if (pdepth !== 0) return null;
  const linkText = text.slice(start + 1, closeBracket);
  return { linkText, end: j + 1 };
}

// ---------------------------------------------------------------------------
// Block splitting: speakable text vs code fences
// ---------------------------------------------------------------------------

type BlockType = "speakable" | "codefence";

interface Block {
  readonly type: BlockType;
  readonly start: number;
  readonly end: number;
  readonly complete: boolean;
}

function splitBlocks(text: string): Block[] {
  const blocks: Block[] = [];
  let i = 0;
  let speakStart = 0;

  while (i < text.length) {
    if (text.startsWith(CODE_FENCE, i)) {
      if (i > speakStart) {
        blocks.push({
          type: "speakable",
          start: speakStart,
          end: i,
          complete: true,
        });
      }
      const fenceStart = i;
      i += CODE_FENCE.length;
      // Skip language identifier line
      while (i < text.length && text[i] !== "\n") i++;
      if (i < text.length) i++;
      // Find closing fence
      let closed = false;
      while (i < text.length) {
        if (text.startsWith(CODE_FENCE, i)) {
          i += CODE_FENCE.length;
          while (i < text.length && text[i] !== "\n") i++;
          if (i < text.length) i++;
          closed = true;
          break;
        }
        i++;
      }
      blocks.push({
        type: "codefence",
        start: fenceStart,
        end: i,
        complete: closed,
      });
      speakStart = i;
    } else {
      i++;
    }
  }
  if (i > speakStart) {
    blocks.push({
      type: "speakable",
      start: speakStart,
      end: i,
      complete: true,
    });
  }
  return blocks;
}

// ---------------------------------------------------------------------------
// Speakable char mapping: extract link text, track original offsets
// ---------------------------------------------------------------------------

interface MappedChar {
  readonly char: string;
  readonly orig: number;
}

// Safe mapped char access — returns "" for out-of-range.
function mappedChar(mapped: MappedChar[], i: number): string {
  return i >= 0 && i < mapped.length ? (mapped[i]?.char ?? "") : "";
}

function buildMapping(text: string, start: number, end: number): MappedChar[] {
  const result: MappedChar[] = [];
  let i = start;
  while (i < end) {
    // Image syntax ![alt](url) — skip entirely, emit nothing.
    if (charAt(text, i) === "!" && charAt(text, i + 1) === "[") {
      const link = matchLink(text, i + 1);
      if (link !== null && link.end <= end) {
        i = link.end;
        continue;
      }
    }
    if (charAt(text, i) === "[") {
      const link = matchLink(text, i);
      if (link !== null && link.end <= end) {
        for (const ch of link.linkText) {
          result.push({ char: ch, orig: i });
        }
        i = link.end;
        continue;
      }
    }
    result.push({ char: charAt(text, i), orig: i });
    i++;
  }
  return result;
}

// ---------------------------------------------------------------------------
// Sentence splitting
// ---------------------------------------------------------------------------

interface SentenceRange {
  readonly mapped: MappedChar[];
  readonly complete: boolean;
}

function isLineStart(mapped: MappedChar[], pos: number): boolean {
  if (pos === 0) return true;
  return mappedChar(mapped, pos - 1) === "\n";
}

function isListMarker(mapped: MappedChar[], pos: number): boolean {
  if (!isLineStart(mapped, pos)) return false;
  const ch = mappedChar(mapped, pos);
  if (
    (ch === "-" || ch === "*" || ch === "+") &&
    pos + 1 < mapped.length &&
    mappedChar(mapped, pos + 1) === " "
  ) {
    return true;
  }
  if (ch >= "0" && ch <= "9") {
    let i = pos + 1;
    while (
      i < mapped.length &&
      mappedChar(mapped, i) >= "0" &&
      mappedChar(mapped, i) <= "9"
    )
      i++;
    if (
      i < mapped.length &&
      mappedChar(mapped, i) === "." &&
      i + 1 < mapped.length &&
      mappedChar(mapped, i + 1) === " "
    ) {
      return true;
    }
  }
  return false;
}

function skipListMarker(mapped: MappedChar[], pos: number): number {
  const ch = mappedChar(mapped, pos);
  if (ch === "-" || ch === "*" || ch === "+") {
    return pos + 2;
  }
  let i = pos + 1;
  while (
    i < mapped.length &&
    mappedChar(mapped, i) >= "0" &&
    mappedChar(mapped, i) <= "9"
  )
    i++;
  return i + 2;
}

function isAbbreviationAt(mapped: MappedChar[], pos: number): boolean {
  let i = pos - 1;
  while (i >= 0 && /[a-zA-Z]/.test(mappedChar(mapped, i))) i--;
  const word = mapped
    .slice(i + 1, pos)
    .map((m) => m.char)
    .join("");
  return ABBREVIATIONS.has(word);
}

function isDecimalAt(mapped: MappedChar[], pos: number): boolean {
  if (pos === 0 || pos + 1 >= mapped.length) return false;
  const before = mappedChar(mapped, pos - 1);
  const after = mappedChar(mapped, pos + 1);
  return before >= "0" && before <= "9" && after >= "0" && after <= "9";
}

function trimTrailingWhitespace(
  mapped: MappedChar[],
  start: number,
  end: number,
): number {
  let e = end;
  while (e >= start && /\s/.test(mappedChar(mapped, e))) e--;
  return e;
}

function splitSentences(mapped: MappedChar[]): SentenceRange[] {
  if (mapped.length === 0) return [];
  const sentences: SentenceRange[] = [];
  let start = 0;

  for (let i = 0; i < mapped.length; i++) {
    if (isListMarker(mapped, i)) {
      if (i > start) {
        const end = trimTrailingWhitespace(mapped, start, i - 1);
        if (end >= start) {
          sentences.push({
            mapped: mapped.slice(start, end + 1),
            complete: true,
          });
        }
      }
      i = skipListMarker(mapped, i) - 1;
      start = i + 1;
      continue;
    }
    const ch = mappedChar(mapped, i);
    if (SENTENCE_PUNCT.has(ch)) {
      if (ch === ".") {
        if (isAbbreviationAt(mapped, i)) continue;
        // Skip if part of a run of dots (ellipsis ...) — look ahead and behind.
        if (i + 1 < mapped.length && mappedChar(mapped, i + 1) === ".")
          continue;
        if (i > 0 && mappedChar(mapped, i - 1) === ".") continue;
        if (isDecimalAt(mapped, i)) continue;
      }
      sentences.push({ mapped: mapped.slice(start, i + 1), complete: true });
      start = i + 1;
    }
  }
  if (start < mapped.length) {
    const end = trimTrailingWhitespace(mapped, start, mapped.length - 1);
    if (end >= start) {
      sentences.push({ mapped: mapped.slice(start, end + 1), complete: false });
    }
  }
  return sentences;
}

// ---------------------------------------------------------------------------
// Whitespace collapse + 180-char fallback
// ---------------------------------------------------------------------------

function collapseWhitespace(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

interface SubChunk {
  readonly text: string;
  readonly firstOrig: number;
  readonly lastOrig: number;
}

function applyFallback(mapped: MappedChar[]): SubChunk[] {
  if (mapped.length === 0) return [];
  if (mapped.length <= FALLBACK_LIMIT) {
    const text = collapseWhitespace(mapped.map((m) => m.char).join(""));
    if (text.length === 0) return [];
    return [
      {
        text,
        firstOrig: mapped[0]?.orig ?? 0,
        lastOrig: mapped[mapped.length - 1]?.orig ?? 0,
      },
    ];
  }

  const subChunks: SubChunk[] = [];
  let chunkStart = 0;
  while (chunkStart < mapped.length) {
    let chunkEnd = Math.min(chunkStart + FALLBACK_LIMIT - 1, mapped.length - 1);
    if (chunkEnd < mapped.length - 1) {
      let boundary = chunkEnd;
      while (boundary > chunkStart && !/\s/.test(mappedChar(mapped, boundary)))
        boundary--;
      if (boundary > chunkStart) {
        chunkEnd = boundary - 1;
      }
    }
    const chars = mapped.slice(chunkStart, chunkEnd + 1);
    const text = collapseWhitespace(chars.map((m) => m.char).join(""));
    if (text.length > 0) {
      subChunks.push({
        text,
        firstOrig: chars[0]?.orig ?? 0,
        lastOrig: chars[chars.length - 1]?.orig ?? 0,
      });
    }
    chunkStart = chunkEnd + 1;
    while (
      chunkStart < mapped.length &&
      /\s/.test(mappedChar(mapped, chunkStart))
    )
      chunkStart++;
  }
  return subChunks;
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

function makeChunkId(
  voiceSessionId: string,
  turnId: string,
  itemIndex: number,
  rangeStart: number,
  rangeEnd: number,
): string {
  return `${voiceSessionId}:${turnId}:${itemIndex}:${rangeStart}-${rangeEnd}`;
}

export function chunkAssistantProse(
  text: string,
  opts: ChunkOptions,
): ChunkResult {
  const { voiceSessionId, turnId, itemIndex, baseOffset, flush = false } = opts;
  const blocks = splitBlocks(text);
  const chunks: VoiceChunk[] = [];
  let consumedRelative = 0;

  for (const block of blocks) {
    if (block.type === "codefence") {
      if (block.complete) {
        consumedRelative = block.end;
      } else {
        break;
      }
      continue;
    }

    const mapped = buildMapping(text, block.start, block.end);
    const sentences = splitSentences(mapped);

    let lastCompleteEnd = consumedRelative;
    let brokeOnIncomplete = false;

    for (const sentence of sentences) {
      if (sentence.complete) {
        const subChunks = applyFallback(sentence.mapped);
        for (const sub of subChunks) {
          const rangeStart = baseOffset + sub.firstOrig;
          const rangeEnd = baseOffset + sub.lastOrig + 1;
          chunks.push({
            id: makeChunkId(
              voiceSessionId,
              turnId,
              itemIndex,
              rangeStart,
              rangeEnd,
            ),
            text: sub.text,
            rangeStart,
            rangeEnd,
          });
        }
        lastCompleteEnd =
          (sentence.mapped[sentence.mapped.length - 1]?.orig ?? 0) + 1;
      } else {
        if (flush) {
          const subChunks = applyFallback(sentence.mapped);
          for (const sub of subChunks) {
            const rangeStart = baseOffset + sub.firstOrig;
            const rangeEnd = baseOffset + sub.lastOrig + 1;
            chunks.push({
              id: makeChunkId(
                voiceSessionId,
                turnId,
                itemIndex,
                rangeStart,
                rangeEnd,
              ),
              text: sub.text,
              rangeStart,
              rangeEnd,
            });
          }
          lastCompleteEnd =
            (sentence.mapped[sentence.mapped.length - 1]?.orig ?? 0) + 1;
        } else {
          brokeOnIncomplete = true;
        }
        break;
      }
    }
    consumedRelative = lastCompleteEnd;
    if (brokeOnIncomplete) break;
  }

  return { chunks, consumedTo: baseOffset + consumedRelative };
}
