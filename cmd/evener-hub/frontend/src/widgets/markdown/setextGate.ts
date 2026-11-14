import { Marked, type MarkedExtension, type Token, type Tokens, type TokensList } from "marked";

// The setext gate (#5002): an exact per-frame "next possible setext
// underline" bound for Marked's `lheading` block rule.
//
// The pinned lheading regex starts with a lazy multi-line capture that must
// walk to the END of the frame to prove no setext underline follows a
// paragraph line. Inside list items Marked emits one text token per
// continuation line, so the lexer probes lheading once per line and every
// probe rescans everything still ahead: lexing a many-thousand-line list
// item is quadratic (measured 8.3 s for 16k lines through this exact
// lexer; the probes and their source suffixes are what the gate caches).
//
// The gate precomputes, once per blockTokens frame, the byte offset of
// every line that could possibly serve as a setext underline - a SUPERSET
// of the pinned rule's underline grammar, so a true match is never
// classified impossible. Each probe then answers "can lheading possibly
// match from here" with one binary search: when no candidate lies at or
// after the probe position, the rule cannot match and its scan is skipped;
// otherwise the PINNED rule runs unchanged and its result is returned
// as-is. Token output is identical by construction - the gate only ever
// suppresses probes that provably cannot match, and any surprise
// (frame-stack mismatch, a probe that grows) permanently degrades that
// frame to the pinned rule.

// marked ships its Lexer and Tokenizer classes as runtime exports without
// top-level type declarations; their typed surface is the constructor
// properties every Marked instance carries.
const markedClasses = new Marked();
const MarkedLexer = markedClasses.Lexer;
const MarkedTokenizer = markedClasses.Tokenizer;
type MarkedLexerInstance = InstanceType<typeof MarkedLexer>;
type MarkedTokenizerInstance = InstanceType<typeof MarkedTokenizer>;

// One blockTokens frame: the source it was entered with, the underline
// candidate offsets inside it (computed lazily on the first lheading
// probe), and the probe-progress tripwire. Probes inside a frame are
// suffixes of `src`, so they only get shorter; the first probe sees the
// whole frame.
interface GateFrame {
  readonly src: string;
  candidates?: number[];
  lastProbeLength: number;
  degraded: boolean;
}

const frames: GateFrame[] = [];
const pinnedLheading = MarkedTokenizer.prototype.lheading;

// A line that could be a setext underline: 0-3 spaces, one or more `=` or
// `-`, optional trailing spaces, then end of line. The pinned rule's
// underline grammar is ` {0,3}(=+|-+) *(?:\n+|$)` preceded by a newline; a
// false positive here just runs the pinned rule, never a skipped match.
const SETEXT_UNDERLINE_CANDIDATE = /(^|\n) {0,3}(=+|-+) *(?=\n|$)/g;

// Installed by createGatedMarked on the shared Lexer prototype, once. The
// marker lives on the prototype itself, not in this module's state, so a
// re-evaluated module copy (dev-server reload) reuses the first install;
// an override created by a stale module copy then finds no frames and
// falls back to the pinned rule - correct, merely unoptimized.
const GATE_MARKER = "__evenerSetextGate";
type GateMarkedLexerPrototype = typeof MarkedLexer.prototype & { [GATE_MARKER]?: boolean };

function ensureFrameStack(): void {
  const proto = MarkedLexer.prototype as GateMarkedLexerPrototype;
  if (proto[GATE_MARKER]) return;
  proto[GATE_MARKER] = true;
  const originalBlockTokens = MarkedLexer.prototype.blockTokens;
  const wrappedBlockTokens = function blockTokens(
    this: MarkedLexerInstance,
    src: string,
    tokens?: Token[] | TokensList,
    lastParagraphClipped?: boolean,
  ): Token[] | TokensList {
    frames.push({ src, lastProbeLength: src.length, degraded: false });
    try {
      // The pinned method's two overloads both pass through; .call cannot
      // discriminate them, so this one cast aligns the token-array shape.
      return originalBlockTokens.call(this, src, tokens as TokensList | undefined, lastParagraphClipped);
    } finally {
      frames.pop();
    }
  } as unknown as typeof originalBlockTokens;
  MarkedLexer.prototype.blockTokens = wrappedBlockTokens;
}

// True when no possible setext underline lies at or after this probe's
// position within its frame: the pinned rule cannot match from here and its
// scan can be skipped.
function gateSkipsProbe(src: string): boolean {
  const frame = frames[frames.length - 1];
  if (frame === undefined || frame.degraded) return false;
  // A probe longer than the last one in this frame breaks the
  // probes-are-shrinking-suffixes contract the cache relies on; from here
  // on the frame answers every probe with the pinned rule.
  if (src.length > frame.lastProbeLength) {
    frame.degraded = true;
    return false;
  }
  frame.lastProbeLength = src.length;
  if (frame.candidates === undefined) {
    const candidates: number[] = [];
    for (const match of frame.src.matchAll(SETEXT_UNDERLINE_CANDIDATE)) {
      // The candidate's line starts after the newline the pattern matched;
      // at the frame's very start there is no newline to skip.
      candidates.push((match.index ?? 0) + (match[1] ? 1 : 0));
    }
    frame.candidates = candidates;
  }
  const candidates = frame.candidates;
  const position = frame.src.length - src.length;
  // First candidate at or after the probe position, binary-searched over
  // the frame's precomputed candidates.
  let low = 0;
  let high = candidates.length;
  while (low < high) {
    const middle = Math.floor((low + high) / 2);
    if ((candidates[middle] ?? 0) < position) low = middle + 1;
    else high = middle;
  }
  return low === candidates.length;
}

// Test observability: how many probes the gate skipped versus let through to
// the pinned rule. Production reads nothing from this object.
export const gateStats = { pinnedProbes: 0, skippedProbes: 0 };

/** Resets the gate's test counters. */
export function resetGateStatsForTests(): void {
  gateStats.pinnedProbes = 0;
  gateStats.skippedProbes = 0;
}

const gatedLheading = function lheading(this: MarkedTokenizerInstance, src: string): Tokens.Heading | undefined {
  if (gateSkipsProbe(src)) {
    gateStats.skippedProbes += 1;
    return undefined;
  }
  gateStats.pinnedProbes += 1;
  return pinnedLheading.call(this, src);
};

/**
 * Constructs the app's Marked instances with the setext gate installed:
 * token output is identical to a plain `new Marked(options)`, with the
 * per-frame bound replacing Marked's per-probe whole-frame setext scan.
 */
export function createGatedMarked(options: MarkedExtension): Marked {
  ensureFrameStack();
  return new Marked(options).use({ tokenizer: { lheading: gatedLheading } });
}
