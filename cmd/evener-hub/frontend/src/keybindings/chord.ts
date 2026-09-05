// Canonical chord AST for the keybinding module: a Chord is one key press
// (required modifiers + optional modifiers + key), a KeySequence is a list of
// presses (tinykeys' space-separated multi-press bindings). Parsing delegates
// to tinykeys' parseKeybinding, which also resolves the "$mod" alias to "Meta"
// on Apple platforms and "Control" everywhere else at parse time - so an AST
// captured at runtime is already platform-resolved.
//
// This module is deliberately React-free: the Mod ⌘/Ctrl platform-split
// CONCEPT comes from widgets/keyhint (see keyhint/index.tsx:24-37), but no
// widget code is imported here.

import { parseKeybinding } from "tinykeys";

export interface Chord {
  /** Required modifiers, canonical KeyboardEvent modifier names, in canonical order. */
  modifiers: string[];
  /** Optional modifiers (tinykeys' [Alt] bracket syntax): match with or without. */
  optionalModifiers: string[];
  /** KeyboardEvent.key (matched case-insensitively by tinykeys) or a regex against key/code. */
  key: string | RegExp;
}

export type KeySequence = Chord[];

// Fixed display/serialization order for modifiers, so a chord's canonical
// form never depends on the order its source string happened to list them in.
const MODIFIER_ORDER = ["Control", "Alt", "Shift", "Meta"];

function byCanonicalOrder(a: string, b: string): number {
  return MODIFIER_ORDER.indexOf(a) - MODIFIER_ORDER.indexOf(b);
}

/** Parses a tinykeys keybinding string ("$mod+K", "Shift+A b") into the canonical AST. */
export function parseChord(input: string): KeySequence {
  if (input.trim() === "") throw new Error("cannot parse an empty keybinding");
  return parseKeybinding(input).map(([required, optional, key]) => ({
    modifiers: [...required].sort(byCanonicalOrder),
    optionalModifiers: [...optional].sort(byCanonicalOrder),
    key,
  }));
}

/** Returns a copy of the sequence with `modifier` added as an OPTIONAL
 * modifier on every press that neither requires nor already optionally
 * lists it, preserving canonical modifier order. Used by the default map to
 * express the legacy "either or both of Meta/Ctrl" chords. */
export function withOptionalModifier(sequence: KeySequence, modifier: string): KeySequence {
  return sequence.map((press) => {
    if (press.modifiers.includes(modifier) || press.optionalModifiers.includes(modifier)) return press;
    return { ...press, optionalModifiers: [...press.optionalModifiers, modifier].sort(byCanonicalOrder) };
  });
}

/** Serializes the AST back to a tinykeys keybinding string: parseChord(serializeChord(ast)) deep-equals ast. */
export function serializeChord(sequence: KeySequence): string {
  return sequence.map(serializePress).join(" ");
}

/** Do two chords share a matching key event? The conflict predicate for
 * validation and rebind, built from tinykeys' actual dispatch loop:
 *
 * SINGLE-press chords overlap when their keys share an event
 * (keysShareAnEvent) AND each side's required-modifier set is a subset of the
 * other side's required ∪ optional set - the exact condition for one
 * KeyboardEvent's modifier state to satisfy both. (Serialization equality
 * alone misses e.g. Control+K vs a default's Control+[Meta]+K, which the same
 * event matches; the earlier-registered binding shadows the later one.)
 *
 * MULTI-press sequences: tinykeys advances every binding whose next expected
 * press matches each keydown, and a COMPLETING binding does not fire while
 * any other binding holds a pending partial (its loop's `conflicts` list).
 * So two sequences conflict exactly when every shared position overlaps:
 * equal length means one event stream completes both (map order decides), and
 * unequal length means the shorter is a prefix - when its last press lands,
 * the longer is pending, so the shorter is deferred and never fires
 * (roborev PR #884 round 12). A mismatch at any shared position ends any
 * shared stream, so those pairs never shadow.
 *
 * Regex keys (tinykeys' "(...)" syntax): two regexes ALWAYS overlap (regex
 * intersection is undecidable here, and a false non-overlap shadows one
 * binding at dispatch - fail closed); a regex vs a literal tests the regex
 * against the exact event spellings the literal's spelling class can match
 * (its key value and/or code-name forms). */
export function chordsOverlap(a: KeySequence, b: KeySequence): boolean {
  if (a.length !== 1 || b.length !== 1) {
    const shared = Math.min(a.length, b.length);
    for (let i = 0; i < shared; i++) {
      const pressA = a[i];
      const pressB = b[i];
      if (pressA === undefined || pressB === undefined || !pressesOverlap(pressA, pressB)) return false;
    }
    return true;
  }
  const pressA = a[0];
  const pressB = b[0];
  if (pressA === undefined || pressB === undefined) return false;
  return pressesOverlap(pressA, pressB);
}

function pressesOverlap(pressA: Chord, pressB: Chord): boolean {
  if (!keysShareAnEvent(pressA.key, pressB.key)) return false;
  const allowedA = [...pressA.modifiers, ...pressA.optionalModifiers];
  const allowedB = [...pressB.modifiers, ...pressB.optionalModifiers];
  return pressA.modifiers.every((m) => allowedB.includes(m)) && pressB.modifiers.every((m) => allowedA.includes(m));
}

/** Does a regex press key match any event spelling carrying this key value
 * (the value itself, or the code name of any physical key producing it)?
 * Shared by chordsOverlap's literal-vs-regex branch and validation.ts's
 * reserved-chord check (roborev PR #884 round 14). */
export function regexMatchesKeyValue(regex: RegExp, keyValue: string): boolean {
  const candidates = [keyValue, ...(KEY_TO_CODE_NAMES[keyValue] ?? [])];
  return candidates.some((candidate) => regex.test(candidate));
}

/** Do two spellings share even ONE matching event? A string spelling matches
 * via event.key (case-insensitive) OR event.code (exact). Same-physical-key
 * aliases fold through keyComparisonIdentity ("KeyW" and "W", "Slash" and
 * "/"). The case a pure fold cannot express is ASYMMETRIC containment
 * (roborev PR #884 round 9): a NumLock-on numpad event is {key: "7", code:
 * "Numpad7"}, so the plain "7" spelling matches it via key AND the "Numpad7"
 * spelling matches it via code - they collide - while "Digit7" and "Numpad7"
 * never share an event (disjoint code domains, and neither spelling matches
 * anything via key). */
function keysShareAnEvent(a: string | RegExp, b: string | RegExp): boolean {
  if (a instanceof RegExp || b instanceof RegExp) {
    // Regex vs regex: regex intersection is not decidable here, and a false
    // NON-overlap lets dispatch order silently shadow one of them - fail
    // closed instead (roborev PR #884 round 14). Only hand-authored hub
    // payloads can carry regex chords (nothing shipped uses one, capture
    // records literals), so the conservative block has no shipped cost.
    if (a instanceof RegExp && b instanceof RegExp) return true;
    // Literal vs regex: tinykeys tests the regex against event.key AND
    // event.code. Which event spellings the LITERAL can produce depends on
    // its spelling class (round 14): a code-spelled literal ("Numpad7")
    // matches only its own code's events, so its candidates are the code
    // name and that event's key value; a key-spelled literal ("7") matches
    // every event carrying the key regardless of code, so its candidates are
    // the key value and every code name for it (top-row AND numpad).
    const regex = (a instanceof RegExp ? a : b) as RegExp;
    const literal = (a instanceof RegExp ? b : a) as string;
    const literalLower = literal.toLowerCase();
    const asCodeKeyValue = CODE_PHYSICAL_KEY[literalLower];
    const candidates =
      asCodeKeyValue !== undefined
        ? [literal, asCodeKeyValue]
        : [literal, literalLower, ...(KEY_TO_CODE_NAMES[literalLower] ?? [])];
    return candidates.some((candidate) => regex.test(candidate));
  }
  if (keyComparisonIdentity(a) === keyComparisonIdentity(b)) return true;
  const ka = a.toLowerCase();
  const kb = b.toLowerCase();
  return CODE_PHYSICAL_KEY[ka] === kb || CODE_PHYSICAL_KEY[kb] === ka;
}

// tinykeys matches a string key against KeyboardEvent.key (case-insensitive)
// OR KeyboardEvent.code (exact) - so a chord spelled with a CODE name
// ("Control+Slash") and one spelled with the key ("Control+/") match the same
// physical keypress, while a literal-string comparison calls them distinct
// and lets one silently shadow the other at dispatch (roborev PR #884 round
// 8). This table is the single source of truth: canonical KeyboardEvent.code
// name -> the physical key's (unmodified, lowercased) key value.
const CODE_TO_KEY_CANONICAL: Record<string, string> = {
  ...Object.fromEntries(
    Array.from({ length: 26 }, (_, i) => [`Key${String.fromCharCode(65 + i)}`, String.fromCharCode(97 + i)]),
  ),
  ...Object.fromEntries(Array.from({ length: 10 }, (_, i) => [`Digit${i}`, String(i)])),
  Minus: "-",
  Equal: "=",
  BracketLeft: "[",
  BracketRight: "]",
  Backslash: "\\",
  Semicolon: ";",
  Quote: "'",
  Comma: ",",
  Period: ".",
  Slash: "/",
  Backquote: "`",
  ...Object.fromEntries(Array.from({ length: 10 }, (_, i) => [`Numpad${i}`, String(i)])),
  NumpadDecimal: ".",
  NumpadAdd: "+",
  NumpadSubtract: "-",
  NumpadMultiply: "*",
  NumpadDivide: "/",
  NumpadEnter: "enter",
};

// Lowercase-code lookup views of the canonical table. CODE_NAME_TO_KEY
// EXCLUDES the numpad: the reserved-chord check (the fold's other consumer)
// names top-row keys, and numpad-vs-top-row collision handling lives in the
// overlap predicate's own containment check (CODE_PHYSICAL_KEY), not in a
// shared identity. CODE_PHYSICAL_KEY includes it because the EVENTS are
// shared.
const CODE_NAME_TO_KEY: Record<string, string> = Object.fromEntries(
  Object.entries(CODE_TO_KEY_CANONICAL)
    .filter(([code]) => !code.startsWith("Numpad"))
    .map(([code, key]) => [code.toLowerCase(), key]),
);
const CODE_PHYSICAL_KEY: Record<string, string> = Object.fromEntries(
  Object.entries(CODE_TO_KEY_CANONICAL).map(([code, key]) => [code.toLowerCase(), key]),
);

// Key value -> the canonical code names of every physical key carrying it
// (top-row AND numpad): the candidate event.code spellings for the regex-vs-
// literal overlap check (tinykeys tests a regex against event.code too).
const KEY_TO_CODE_NAMES: Record<string, string[]> = {};
for (const [code, key] of Object.entries(CODE_TO_KEY_CANONICAL)) {
  const names = KEY_TO_CODE_NAMES[key];
  if (names === undefined) KEY_TO_CODE_NAMES[key] = [code];
  else names.push(code);
}

/** The comparison identity of one press's key: regex keys by source, string
 * keys lowercased with code-name aliases folded to their key value. Shared by
 * chord.ts's overlap predicate and validation.ts's reserved check. */
export function keyComparisonIdentity(key: string | RegExp): string {
  if (key instanceof RegExp) return key.source;
  const lowered = key.toLowerCase();
  return CODE_NAME_TO_KEY[lowered] ?? lowered;
}

function serializePress(chord: Chord): string {
  const mods = [...chord.modifiers, ...chord.optionalModifiers.map((m) => `[${m}]`)];
  return [...mods, serializeKey(chord.key)].join("+");
}

function serializeKey(key: string | RegExp): string {
  if (!(key instanceof RegExp)) return key;
  // parseKeybinding wraps a (regex) key as /^(?:regex)$/iv; unwrap that exact
  // anchor so a parsed regex serializes back to its source form and the
  // round-trip stays stable instead of nesting anchors on every pass.
  const anchored = key.source.match(/^\^\(\?:(.*)\)\$$/);
  return `(${anchored?.[1] ?? key.source})`;
}

// Display labels, reusing keyhint's platform-split concept: "$mod" has already
// been resolved by parse time, so Meta reads as the Apple command glyph and
// Control as the cross-platform "Ctrl". Every other name renders verbatim,
// exactly as keyhint treats non-Mod keys.
const MODIFIER_DISPLAY: Record<string, string> = {
  Control: "Ctrl",
  Meta: "⌘",
};

/** One press as display tokens, in KeyHint's `keys` shape: ["⌘","K"] on Apple
 * platforms (the AST was `$mod`-resolved at parse time), ["Ctrl","K"]
 * elsewhere. Optional modifiers are NOT included - they describe match
 * permissiveness, not the key the user presses. */
export function chordDisplayKeys(chord: Chord): string[] {
  const key = chord.key instanceof RegExp ? chord.key.source : chord.key;
  return [...chord.modifiers.map((m) => MODIFIER_DISPLAY[m] ?? m), key];
}

/** One modifier's display label (Ctrl / ⌘ / verbatim) - the chordDisplayKeys
 * mapping for the capture editor's modifier-only live preview, which has no
 * key yet. */
export function modifierDisplayKey(modifier: string): string {
  return MODIFIER_DISPLAY[modifier] ?? modifier;
}

/** One press as speakable words: "⌘+K" on Apple platforms, "Ctrl+K" elsewhere. */
export function formatChord(chord: Chord): string {
  return chordDisplayKeys(chord).join("+");
}

/** A whole sequence as words, presses space-separated: "Shift+A b". */
export function formatSequence(sequence: KeySequence): string {
  return sequence.map(formatChord).join(" ");
}
