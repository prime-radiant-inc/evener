import { describe, expect, test } from "vitest";
import { type Chord, chordsOverlap, formatChord, formatSequence, parseChord, serializeChord } from "./chord";

function singleChord(input: string): Chord {
  const sequence = parseChord(input);
  if (sequence.length !== 1) throw new Error(`expected exactly one press in "${input}"`);
  return sequence[0]!;
}

// jsdom's navigator.platform is "" on every host, so tinykeys resolves the
// "$mod" alias to "Control" here regardless of the machine running the suite.
// Tests that want the Meta spelling parse "Meta+..." explicitly.
describe("parseChord", () => {
  test("parses a single press with a modifier", () => {
    expect(parseChord("$mod+K")).toEqual([{ modifiers: ["Control"], optionalModifiers: [], key: "K" }]);
  });

  test("parses a bare key with no modifiers", () => {
    expect(parseChord("Escape")).toEqual([{ modifiers: [], optionalModifiers: [], key: "Escape" }]);
  });

  test("parses a sequence of presses", () => {
    expect(parseChord("Shift+A b")).toEqual([
      { modifiers: ["Shift"], optionalModifiers: [], key: "A" },
      { modifiers: [], optionalModifiers: [], key: "b" },
    ]);
  });

  test("parses optional modifiers", () => {
    expect(parseChord("$mod+[Alt]+K")).toEqual([{ modifiers: ["Control"], optionalModifiers: ["Alt"], key: "K" }]);
  });

  test("canonicalizes modifier order", () => {
    expect(parseChord("Shift+Control+K")).toEqual([
      { modifiers: ["Control", "Shift"], optionalModifiers: [], key: "K" },
    ]);
  });

  test("parses a regex key", () => {
    const chord = singleChord("(a|b)");
    expect(chord.modifiers).toEqual([]);
    expect(chord.key).toBeInstanceOf(RegExp);
  });

  test("rejects a blank keybinding string", () => {
    expect(() => parseChord("")).toThrow();
    expect(() => parseChord("   ")).toThrow();
  });
});

describe("serializeChord round-trips", () => {
  const CASES = ["$mod+K", "$mod+B", "$mod+'", "Escape", "Shift+A b", "$mod+[Alt]+K", "Control+Shift+Home"];

  test.each(CASES)("parse -> serialize -> parse is stable: %s", (input) => {
    const ast = parseChord(input);
    expect(parseChord(serializeChord(ast))).toEqual(ast);
  });

  test.each(CASES)("serialization is idempotent: %s", (input) => {
    const once = serializeChord(parseChord(input));
    expect(serializeChord(parseChord(once))).toBe(once);
  });

  test("serializes modifiers in canonical order", () => {
    expect(serializeChord(parseChord("Shift+Control+K"))).toBe("Control+Shift+K");
  });

  test("serializes optional modifiers with bracket syntax", () => {
    expect(serializeChord(parseChord("$mod+[Alt]+K"))).toBe("Control+[Alt]+K");
  });

  test("serializes a sequence as space-separated presses", () => {
    expect(serializeChord(parseChord("Shift+A b"))).toBe("Shift+A b");
  });
});

describe("formatChord", () => {
  // The Mod platform-split concept from widgets/keyhint: the primary modifier
  // reads "Ctrl" off Apple platforms and "⌘" on them. tinykeys has already
  // resolved "$mod" by parse time, so formatting maps the resolved modifier.
  test("formats Control with the Ctrl label", () => {
    expect(formatChord(singleChord("Control+K"))).toBe("Ctrl+K");
  });

  test("formats Meta with the Apple command glyph", () => {
    expect(formatChord(singleChord("Meta+K"))).toBe("⌘+K");
  });

  test("leaves non-split keys verbatim", () => {
    expect(formatChord(singleChord("Shift+Enter"))).toBe("Shift+Enter");
  });

  test("formats a bare key", () => {
    expect(formatChord(singleChord("Escape"))).toBe("Escape");
  });
});

describe("formatSequence", () => {
  test("joins presses with a space", () => {
    expect(formatSequence(parseChord("Shift+A b"))).toBe("Shift+A b");
  });
});

describe("chordsOverlap", () => {
  test("identical single-press chords overlap", () => {
    expect(chordsOverlap(parseChord("Control+K"), parseChord("Control+K"))).toBe(true);
  });

  test("keys match case-insensitively", () => {
    expect(chordsOverlap(parseChord("Control+K"), parseChord("Control+k"))).toBe(true);
  });

  test("an exact-required chord overlaps a chord that lists the rest as OPTIONAL", () => {
    // The dispatch-time shadowing the whole-branch review flagged: Control+K
    // matches palette.open's Control+[Meta]+K default.
    expect(chordsOverlap(parseChord("Control+K"), parseChord("Control+[Meta]+K"))).toBe(true);
    // Symmetrically, the fully-loaded press also overlaps the plain one.
    expect(chordsOverlap(parseChord("Control+[Meta]+K"), parseChord("Control+K"))).toBe(true);
  });

  test("an extra REQUIRED modifier escapes the overlap when the other side does not allow it", () => {
    expect(chordsOverlap(parseChord("Control+Alt+B"), parseChord("Control+B"))).toBe(false);
    expect(chordsOverlap(parseChord("Control+B"), parseChord("Control+Alt+B"))).toBe(false);
  });

  test("an extra required modifier still overlaps when the other side lists it as optional", () => {
    expect(chordsOverlap(parseChord("Control+Alt+K"), parseChord("Control+[Alt]+K"))).toBe(true);
  });

  test("different keys never overlap", () => {
    expect(chordsOverlap(parseChord("Control+K"), parseChord("Control+J"))).toBe(false);
  });

  test("multi-press sequences conflict when one is a press-overlapping prefix of the other", () => {
    // tinykeys defers a completing binding while another holds a pending
    // partial: "Control+K" pressed with "Control+K Control+W" registered
    // leaves the sequence pending, so the single-press binding never fires
    // (roborev PR #884 round 12).
    expect(chordsOverlap(parseChord("Control+K"), parseChord("Control+K Control+W"))).toBe(true);
    expect(chordsOverlap(parseChord("Control+K Control+W"), parseChord("Control+K"))).toBe(true);
    expect(chordsOverlap(parseChord("Control+K Control+W"), parseChord("Control+K Control+W"))).toBe(true);
  });

  test("multi-press sequences that diverge at a shared position never shadow", () => {
    expect(chordsOverlap(parseChord("Control+K Control+W"), parseChord("Control+K Control+V"))).toBe(false);
    expect(chordsOverlap(parseChord("a b"), parseChord("x b"))).toBe(false);
    // Prefix positions must overlap with modifiers too, not just keys.
    expect(chordsOverlap(parseChord("Control+K"), parseChord("Shift+K Control+W"))).toBe(false);
  });

  test("a regex key overlaps a literal it matches, on key OR code spellings", () => {
    // tinykeys tests a regex against event.key AND event.code (roborev PR
    // #884 round 12).
    expect(chordsOverlap(parseChord("(K|X)"), parseChord("K"))).toBe(true);
    expect(chordsOverlap(parseChord("K"), parseChord("(K|X)"))).toBe(true);
    // The literal's code-name form matches too: "(KeyK)" shares the event
    // with the "K" spelling via event.code.
    expect(chordsOverlap(parseChord("(KeyK)"), parseChord("K"))).toBe(true);
    expect(chordsOverlap(parseChord("(K|X)"), parseChord("J"))).toBe(false);
  });

  test("regex-vs-literal honors the literal's spelling class (round 14)", () => {
    // A code-spelled literal matches only its own code's events: "(7)" and
    // "Numpad7" share the numpad event, but "(Numpad7)" and "Digit7" never
    // share one (the Digit7 event's code is not Numpad7).
    expect(chordsOverlap(parseChord("(7)"), parseChord("Numpad7"))).toBe(true);
    expect(chordsOverlap(parseChord("(Numpad7)"), parseChord("Digit7"))).toBe(false);
    expect(chordsOverlap(parseChord("(Numpad7)"), parseChord("Numpad7"))).toBe(true);
    // A key-spelled literal matches every event carrying the key, so "(7)"
    // overlaps "7" (whose events include the numpad one).
    expect(chordsOverlap(parseChord("(7)"), parseChord("7"))).toBe(true);
  });

  test("two regex chords ALWAYS overlap - fail closed (regex intersection is undecidable here)", () => {
    // A false non-overlap lets dispatch order silently shadow one binding
    // (roborev PR #884 round 14); only hand-authored hub payloads can carry
    // regex chords, so the conservative block has no shipped cost.
    expect(chordsOverlap(parseChord("(K|X)"), parseChord("(K|X)"))).toBe(true);
    expect(chordsOverlap(parseChord("(K|X)"), parseChord("(K|Y)"))).toBe(true);
    expect(chordsOverlap(parseChord("([A-Z])"), parseChord("([0-9])"))).toBe(true);
  });

  // tinykeys matches a string key against event.key OR event.code, so a
  // code-named chord and a key-named chord claim the same physical press.
  // Overlap must fold the aliases or validation passes a shadowing override
  // (roborev PR #884 round 8).
  test("code-name spellings overlap their key spellings", () => {
    expect(chordsOverlap(parseChord("Control+Slash"), parseChord("Control+/"))).toBe(true);
    expect(chordsOverlap(parseChord("Meta+KeyW"), parseChord("Meta+W"))).toBe(true);
    expect(chordsOverlap(parseChord("Control+Digit7"), parseChord("Control+7"))).toBe(true);
    expect(chordsOverlap(parseChord("Meta+Comma"), parseChord("Meta+,"))).toBe(true);
  });

  test("physically distinct numpad codes do NOT fold onto the top-row key", () => {
    // A numpad-7 event matches a "7" binding via event.key, but a top-row 7
    // never matches a Numpad7 binding - folding them would invent a conflict.
    expect(chordsOverlap(parseChord("Control+Numpad7"), parseChord("Control+Digit7"))).toBe(false);
  });

  // The containment the fold cannot express (roborev PR #884 round 9): a
  // NumLock-on numpad event is {key: "7", code: "Numpad7"}, so the plain "7"
  // spelling matches it via key AND the code-named spelling matches it via
  // code. Both bindings matching one event means registration order silently
  // shadows one - validation must call it a conflict.
  test("a plain key spelling overlaps the numpad code name for the same physical value", () => {
    expect(chordsOverlap(parseChord("Control+7"), parseChord("Control+Numpad7"))).toBe(true);
    expect(chordsOverlap(parseChord("Control+Numpad7"), parseChord("Control+7"))).toBe(true);
    expect(chordsOverlap(parseChord("Enter"), parseChord("NumpadEnter"))).toBe(true);
  });
});
