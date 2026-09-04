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

/** Do two chords share a matching key event? Two SINGLE-press chords overlap
 * when their keys match case-insensitively AND each side's required-modifier
 * set is a subset of the other side's required ∪ optional set - the exact
 * condition for one KeyboardEvent's modifier state to satisfy both. This is
 * the conflict predicate for validation and rebind: serialization equality
 * alone misses e.g. Control+K vs a default's Control+[Meta]+K, which the
 * same event matches (the earlier-registered binding shadows the later one
 * at dispatch).
 *
 * Multi-press sequences keep the old serialization-equality check: a
 * per-press overlap extension is out of scope (user multi-press overrides
 * are rare, and a false non-conflict there has the same shadowing symptom,
 * just far less likely). Regex keys (tinykeys' "(...)" syntax) compare by
 * source text, so only identical regexes overlap. */
export function chordsOverlap(a: KeySequence, b: KeySequence): boolean {
  if (a.length !== 1 || b.length !== 1) return serializeChord(a) === serializeChord(b);
  const pressA = a[0];
  const pressB = b[0];
  if (pressA === undefined || pressB === undefined) return false;
  if (keyIdentity(pressA) !== keyIdentity(pressB)) return false;
  const allowedA = [...pressA.modifiers, ...pressA.optionalModifiers];
  const allowedB = [...pressB.modifiers, ...pressB.optionalModifiers];
  return pressA.modifiers.every((m) => allowedB.includes(m)) && pressB.modifiers.every((m) => allowedA.includes(m));
}

function keyIdentity(chord: Chord): string {
  return chord.key instanceof RegExp ? chord.key.source : chord.key.toLowerCase();
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

/** One press as speakable words: "⌘+K" on Apple platforms, "Ctrl+K" elsewhere. */
export function formatChord(chord: Chord): string {
  return chordDisplayKeys(chord).join("+");
}

/** A whole sequence as words, presses space-separated: "Shift+A b". */
export function formatSequence(sequence: KeySequence): string {
  return sequence.map(formatChord).join(" ");
}
