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
    // matches palette.open's Control+[Meta]+[Shift]+[Alt]+K default.
    expect(chordsOverlap(parseChord("Control+K"), parseChord("Control+[Meta]+[Shift]+[Alt]+K"))).toBe(true);
    // Symmetrically, the fully-loaded press also overlaps the plain one.
    expect(chordsOverlap(parseChord("Control+[Meta]+[Shift]+[Alt]+K"), parseChord("Control+K"))).toBe(true);
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

  test("multi-press sequences fall back to serialization equality", () => {
    expect(chordsOverlap(parseChord("Control+K Control+W"), parseChord("Control+K Control+W"))).toBe(true);
    expect(chordsOverlap(parseChord("Control+K Control+W"), parseChord("Control+K Control+V"))).toBe(false);
    expect(chordsOverlap(parseChord("Control+K Control+W"), parseChord("Control+K"))).toBe(false);
  });
});
