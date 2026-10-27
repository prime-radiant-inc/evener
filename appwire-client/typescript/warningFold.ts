// Folds a warning notification's params into the one validated shape every
// warning row reads. Leaf module: depends only on the wire types and
// warningText, so the transcript reducer imports it and never the reverse.

import type { WarningParams } from "./types.gen";
import { hasWarningText } from "./warningText";

// Restates appwire.WarningParams.EffectiveMessage shape for shape: the
// generated types (WarningParams, `warning` typed `unknown`) are
// declarations only, no runtime logic is generated alongside them, so the
// rule cannot be derived from the types and has to be written out again
// here. `message` wins when non-blank; otherwise `warning` counts when it is
// itself a non-blank string, or an object (and not an array) whose own
// `message` is a non-blank string. Every other shape carries no message.
// Returned strings are bounded from their first non-whitespace content so the
// fold does not scan and bound the selected message a second time.
function warningMessage(params: WarningParams): string {
  const message = boundedWarningText(params.message);
  if (message !== undefined) return message;
  const warning = params.warning;
  const warningText = boundedWarningText(warning);
  if (warningText !== undefined) return warningText;
  if (typeof warning === "object" && warning !== null && !Array.isArray(warning)) {
    const nested = (warning as { message?: unknown }).message;
    const nestedMessage = boundedWarningText(nested);
    if (nestedMessage !== undefined) return nestedMessage;
  }
  return "";
}

// A frame with no message anywhere is surfaced as the frame itself
// (appwire/warning.go's DecodeWarningParams: "a malformed warning is visible
// instead of silent"). Bounded because params is unknown on the wire and
// can carry anything; this is package-level code feeding both hosts, and
// neither host's own display bound can be assumed to run before something
// else reads item.text.
export const RAW_WARNING_FRAME_MAX_CHARS = 2000;
const RAW_WARNING_FRAME_MAX_ARRAY_ITEMS = 50;
const RAW_WARNING_FRAME_MAX_OBJECT_KEYS = 50;
const RAW_WARNING_FRAME_MAX_DEPTH = 6;
// Total object/array entries the prune will walk across the WHOLE frame,
// regardless of how the size is spread across depth and breadth. The
// per-level array/key caps alone leave a gap: many small objects, each
// individually within the array/key/depth bounds, can still sum to a huge
// tree for JSON.stringify to walk.
const RAW_WARNING_FRAME_MAX_NODES = 500;

// Prunes a value to a small bound before it ever reaches JSON.stringify:
// every string truncated to RAW_WARNING_FRAME_MAX_CHARS code points,
// every array/object to its first 50 items/keys, nesting cut off at 6
// levels, and the whole walk cut off after RAW_WARNING_FRAME_MAX_NODES
// entries regardless of shape. Without this, JSON.stringify(params) itself
// walks the WHOLE frame — up to the transport's 128 MiB limit — before
// rawWarningFrame gets a chance to slice anything; bounding the input, not
// just the output, is what keeps that walk small regardless of how large
// or how shaped the wire frame actually is.
// Truncates a string to maxCodePoints code points, safely (a UTF-16 slice
// can otherwise cut a surrogate pair in half). The one primitive every
// string bound in this file goes through — prunedForStringify's per-field
// and per-key truncation, and boundedCodePoints below — so a wire string
// too big to reach ItemModel unbounded is always cut on a code-point
// boundary, never mid-pair. A fast path for the common (short) case:
// UTF-16 length is always >= code-point count, so no huge value means no
// work.
// `start` lets a caller bound a WINDOW rather than always the leading
// prefix: the string from `start` onward is what's kept, sliced in one
// already-bounded copy (at most maxCodePoints * 2 UTF-16 units), never the
// whole `start`-to-end remainder — boundedWarningText below relies on this to
// stay bounded even when `start` is itself deep into a multi-megabyte
// string.
function boundedPrefix(s: string, maxCodePoints: number, start = 0): string {
  const remaining = s.length - start;
  if (remaining <= maxCodePoints) return start === 0 ? s : s.slice(start);
  // Bound the allocation before expanding to code points: a UTF-16 window
  // twice the code-point limit always contains at least that many code
  // points (every code point is at most two UTF-16 units), so slicing the
  // string first — a cheap view, no per-character array — never drops real
  // content.
  let bounded = s.slice(start, start + maxCodePoints * 2);
  // A UTF-16 slice can end mid-surrogate-pair, leaving a lone high surrogate
  // as the last unit of `bounded`. Array.from would treat that lone unit as
  // its own broken "character" rather than dropping it; strip it before
  // expanding so the final bounded frame never ends on one.
  const lastUnit = bounded.charCodeAt(bounded.length - 1);
  if (lastUnit >= 0xd800 && lastUnit <= 0xdbff) bounded = bounded.slice(0, -1);
  // Array.from splits a string into code points, not UTF-16 units, so a
  // surrogate pair (an emoji, or anything outside the BMP) straddling the
  // bound is kept or dropped whole - a plain String#slice(0, N) can instead
  // cut the pair in half, leaving a lone, unpaired surrogate at the tail.
  return Array.from(bounded).slice(0, maxCodePoints).join("");
}

function prunedForStringify(value: unknown, depth: number, budget: { remaining: number }): unknown {
  if (budget.remaining <= 0) return typeof value === "string" ? "" : "…";
  budget.remaining -= 1;
  if (typeof value === "string") {
    const bounded = boundedPrefix(value, RAW_WARNING_FRAME_MAX_CHARS);
    return bounded === value ? value : `${bounded}…`;
  }
  if (depth >= RAW_WARNING_FRAME_MAX_DEPTH) {
    return typeof value === "object" && value !== null ? "…" : value;
  }
  if (Array.isArray(value)) {
    return value.slice(0, RAW_WARNING_FRAME_MAX_ARRAY_ITEMS).map((item) => prunedForStringify(item, depth + 1, budget));
  }
  if (typeof value === "object" && value !== null) {
    // A wire key literally named "__proto__" is a real, own, enumerable
    // property on the parsed object (JSON.parse never invokes a setter) -
    // assigning into a plain `{}` here would invoke Object.prototype's
    // __proto__ setter instead of creating an own property, silently
    // dropping that field from the pruned result. Object.create(null) has
    // no such setter, so every assignment below is a genuine own property.
    const pruned: Record<string, unknown> = Object.create(null);
    // for...in still needs one full enumeration of value's own keys (so
    // does Object.keys/Object.entries) — that step is O(keys), the same
    // order as the JSON.parse that produced this object in the first
    // place, so it adds no NEW asymptotic cost on top of what parsing the
    // wire frame already paid. What for...in avoids is allocating a
    // [key, value] PAIR per key and READING more values than survive the
    // cap: Object.entries reads and copies every value up front, while this
    // loop counts and breaks, reading (and copying) at most
    // RAW_WARNING_FRAME_MAX_OBJECT_KEYS + 1 property values regardless of
    // how many keys the object has.
    let taken = 0;
    for (const key in value) {
      if (!Object.hasOwn(value, key)) continue;
      if (taken >= RAW_WARNING_FRAME_MAX_OBJECT_KEYS || budget.remaining <= 0) break;
      taken++;
      // The key-count cap above bounds how many properties survive, but
      // says nothing about how long any one property NAME is — an
      // oversized key would otherwise ride through verbatim, the same
      // vector the value-length bound above closes for string values.
      const boundedKeyPrefix = boundedPrefix(key, RAW_WARNING_FRAME_MAX_CHARS);
      let boundedKey = boundedKeyPrefix === key ? key : `${boundedKeyPrefix}…`;
      // Two distinct keys can share their first RAW_WARNING_FRAME_MAX_CHARS
      // code points and truncate to the identical boundedKey - assigning
      // straight into `pruned` would then have the second key's value
      // silently overwrite the first's. Suffix a collision with a counter
      // until it lands on a key `pruned` doesn't already own, so both
      // survive (as two visibly-truncated keys) instead of one vanishing.
      for (let collision = 2; Object.hasOwn(pruned, boundedKey); collision++) {
        boundedKey = `${boundedKeyPrefix}…#${collision}`;
      }
      pruned[boundedKey] = prunedForStringify((value as Record<string, unknown>)[key], depth + 1, budget);
    }
    return pruned;
  }
  return value;
}

// Applied to every string a warning frame can put into the model — message,
// title, hint, source, and the raw fallback — so a multi-megabyte value
// anywhere in the frame can never reach ItemModel unbounded, not just via
// the message-less fallback path.
function boundedCodePoints(s: string): string {
  return boundedPrefix(s, RAW_WARNING_FRAME_MAX_CHARS);
}

// boundedCodePoints alone always keeps the LEADING RAW_WARNING_FRAME_MAX_CHARS
// code points — a message, title, hint, or source with more than that many
// leading blank code points followed by real content would then be stored as
// nothing but the blank prefix, rendering as nothing to every consumer.
// boundedWarningText answers "is there content" (hasWarningText) and bounds
// it starting from that content: value.search finds the first non-whitespace
// index without copying anything, then boundedPrefix takes its own single,
// already-bounded slice starting there, so the window kept always contains
// the actual content instead of the padding in front of it. undefined when
// value isn't a non-blank string at all. The padding is only skipped when
// truncation is actually needed (matching boundedPrefix's own fast path): a
// short value already within the bound is returned unchanged, leading
// whitespace included, since nothing about it needs to be bounded away from
// at all.
function boundedWarningText(value: unknown): string | undefined {
  if (!hasWarningText(value)) return undefined;
  if (value.length <= RAW_WARNING_FRAME_MAX_CHARS) return value;
  return boundedPrefix(value, RAW_WARNING_FRAME_MAX_CHARS, value.search(/\S/));
}

function rawWarningFrame(params: WarningParams): string {
  const json = JSON.stringify(prunedForStringify(params, 0, { remaining: RAW_WARNING_FRAME_MAX_NODES }));
  // prunedForStringify above already keeps `json` itself small; this bound
  // is defense-in-depth for the code-point expansion specifically.
  return boundedCodePoints(json);
}

// The one validated shape every warning row — live with a turn, live
// without one, and (via the item this produces) a canonical reread — reads
// title/hint/source from. params is unknown on the wire (WarningParams'
// `warning` field, and title/hint despite their declared string type), so
// this is the single place that turns it into string-or-absent fields; every
// consumer reads the result, never params directly.
export interface WarningFold {
  text: string;
  title?: string;
  hint?: string;
  source?: string;
  code?: string;
}

export function foldWarningParams(params: WarningParams): WarningFold {
  const text = warningMessage(params);
  const title = boundedWarningText(params.title);
  const hint = boundedWarningText(params.hint);
  const source = boundedWarningText(params.source);
  const code = boundedWarningText(params.code);
  const foldedText = text || (title !== undefined || hint !== undefined ? "" : rawWarningFrame(params));
  return {
    // warningMessage and rawWarningFrame already bound the selected text;
    // keeping that result avoids rescanning it during the fold.
    text: foldedText,
    // Blank is absent too, not just "not a string" — hasWarningText's own
    // reading, which every consumer must apply anyway. Normalizing it here
    // means a future reader is never one missed hasWarningText call away
    // from rendering blank content. Bounded for the same reason as text:
    // an oversized title/hint/source reaching ItemModel.warning verbatim is
    // the same class of vector rawWarningFrame closes for the fallback.
    // These values are reused for the title/hint presence check above, so
    // each field is scanned and bounded once.
    title,
    hint,
    source,
    code,
  };
}

// Joins whichever WarningFold parts a caller has (title/text/hint, in
// whatever order it passes them) into one display string, filtering out
// blanks - the one composition rule every surface that renders a fold as a
// single string shares, so mobile's canonical projector and its live row
// (both pass text and hint, keeping title as their own field) never drift
// into two different join implementations.
export function joinWarningParts(parts: readonly (string | undefined)[]): string {
  return parts.filter(hasWarningText).join(" — ");
}
