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

/** Serializes the AST back to a tinykeys keybinding string: parseChord(serializeChord(ast)) deep-equals ast. */
export function serializeChord(sequence: KeySequence): string {
  return sequence.map(serializePress).join(" ");
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

/** One press as speakable words: "⌘+K" on Apple platforms, "Ctrl+K" elsewhere. */
export function formatChord(chord: Chord): string {
  const mods = chord.modifiers.map((m) => MODIFIER_DISPLAY[m] ?? m);
  const key = chord.key instanceof RegExp ? chord.key.source : chord.key;
  return [...mods, key].join("+");
}

/** A whole sequence as words, presses space-separated: "Shift+A b". */
export function formatSequence(sequence: KeySequence): string {
  return sequence.map(formatChord).join(" ");
}
