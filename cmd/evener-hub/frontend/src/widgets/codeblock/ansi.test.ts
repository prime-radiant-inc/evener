// @vitest-environment node

import { expect, test } from "vitest";
import type { AnsiLine } from "./ansi";
import { AnsiTailBuffer, parseAnsiByteRows, parseAnsiLines } from "./ansi";

test("byte rows map visible UTF16 positions to original bytes across controls and malformed scalars", () => {
  const parsed = parseAnsiByteRows(
    new Uint8Array([65, 27, 91, 51, 49, 109, 0xf0, 0x9f, 0x98, 0x80, 0xff, 0xc3, 40, 10]),
    70,
  );
  expect(parsed.rows[0]?.line.map((run) => run.text).join("")).toBe("A😀��(");
  expect(parsed.rows[0]?.textByteOffsets).toEqual(new Uint16Array([0, 6, 6, 10, 11, 12]));
});

function plainText(lines: AnsiLine[]): string {
  return lines.map((line) => line.map((run) => run.text).join("")).join("\n");
}

test("named color, bold, and selective resets produce semantic runs", () => {
  const lines = parseAnsiLines("\u001b[31;1mred\u001b[22;39m plain");

  expect(lines).toHaveLength(1);
  expect(lines[0]).toMatchObject([
    { text: "red", foreground: { kind: "named", name: "red" }, bold: true },
    { text: " plain", foreground: undefined, bold: false },
  ]);
});

test("bright foreground and background colors retain their terminal names", () => {
  const [line] = parseAnsiLines("\u001b[94;103mcontrast\u001b[0m");

  expect(line).toMatchObject([
    {
      text: "contrast",
      foreground: { kind: "named", name: "bright-blue" },
      background: { kind: "named", name: "bright-yellow" },
    },
  ]);
});

test("256-color values become validated xterm RGB values", () => {
  const [line] = parseAnsiLines("\u001b[38;5;196mindexed\u001b[39m");

  expect(line).toMatchObject([{ text: "indexed", foreground: { kind: "rgb", value: "255, 0, 0" } }]);
});

test("truecolor values become validated RGB values", () => {
  const [line] = parseAnsiLines("\u001b[38;2;12;34;56mtrue\u001b[39m");

  expect(line).toMatchObject([{ text: "true", foreground: { kind: "rgb", value: "12, 34, 56" } }]);
});

test("italic, underline, and strike-through survive until their selective resets", () => {
  const [line] = parseAnsiLines("\u001b[3;4;9mdecorated\u001b[23;24;29m plain");

  expect(line).toMatchObject([
    { text: "decorated", italic: true, underline: true, strikethrough: true },
    { text: " plain", italic: false, underline: false, strikethrough: false },
  ]);
});

test("inverse swaps the default terminal foreground and background", () => {
  const [line] = parseAnsiLines("\u001b[7mreverse\u001b[27m");

  expect(line).toMatchObject([
    {
      text: "reverse",
      foreground: { kind: "named", name: "black" },
      background: { kind: "named", name: "white" },
    },
  ]);
});

test("style state continues across logical lines", () => {
  const lines = parseAnsiLines("\u001b[32mone\ntwo\u001b[39m");

  expect(lines).toHaveLength(2);
  expect(lines[0]).toMatchObject([{ text: "one", foreground: { kind: "named", name: "green" } }]);
  expect(lines[1]).toMatchObject([{ text: "two", foreground: { kind: "named", name: "green" } }]);
});

test("OSC hyperlinks and cursor controls are consumed without losing printable text", () => {
  const lines = parseAnsiLines("safe\u001b]8;;https://example.com\u0007link\u001b]8;;\u0007\u001b[2J after\u001b[H");

  expect(plainText(lines)).toBe("safelink after");
});

test("a two-byte escape sequence cannot leak into rendered text", () => {
  const lines = parseAnsiLines("safe\u001bbroken");

  expect(plainText(lines)).toBe("saferoken");
  expect(plainText(lines)).not.toContain("\u001b");
});

test("carriage return is consumed as a terminal control rather than retained in log text", () => {
  expect(plainText(parseAnsiLines("before\rafter"))).toBe("beforeafter");
});

test("DCS, APC, PM, and SOS control strings consume their payload through ST", () => {
  const input =
    "a\u001bPdevice payload\u001b\\b" +
    "\u001b_application payload\u001b\\c" +
    "\u001b^privacy payload\u001b\\d" +
    "\u001bXservice payload\u001b\\e";

  expect(plainText(parseAnsiLines(input))).toBe("abcde");
});

test("C1 OSC consumes its payload through a C1 string terminator", () => {
  expect(plainText(parseAnsiLines("before\u009dtitle payload\u009cafter"))).toBe("beforeafter");
});

test("an unterminated control string consumes the rest of the input", () => {
  expect(plainText(parseAnsiLines("visible\u001b]unterminated protocol payload"))).toBe("visible");
});

test("non-CSI escape sequences consume their complete protocol bytes", () => {
  expect(plainText(parseAnsiLines("a\u001b7b\u001b8c\u001b(Bd"))).toBe("abcd");
});

test("repeated decoration enables remain idempotent after a selective reset", () => {
  const [line] = parseAnsiLines("\u001b[1m\u001b[1mbold\u001b[22mplain");

  expect(line).toMatchObject([
    { text: "bold", bold: true },
    { text: "plain", bold: false },
  ]);
});

test("an ESC interrupts an incomplete CSI sequence and starts the next style", () => {
  const lines = parseAnsiLines("\u001b[31\u001b[32mPASS");

  expect(plainText(lines)).toBe("PASS");
  expect(lines[0]).toMatchObject([{ text: "PASS", foreground: { kind: "named", name: "green" } }]);
});

test("empty extended-color fields do not replace active foreground or background colors", () => {
  const [line] = parseAnsiLines("\u001b[31;44mactive\u001b[38;5;m foreground\u001b[48;2;;2;3m background");

  expect(line).toMatchObject([
    { text: "active", foreground: { kind: "named", name: "red" }, background: { kind: "named", name: "blue" } },
    {
      text: " foreground",
      foreground: { kind: "named", name: "red" },
      background: { kind: "named", name: "blue" },
    },
    {
      text: " background",
      foreground: { kind: "named", name: "red" },
      background: { kind: "named", name: "blue" },
    },
  ]);
});

// Additional ansi.ts edge cases to close uncovered lines:
// - normalizedSgr: code 21 (double-underline reset for bold), code 22 (dim reset),
//   unknown codes that fall through to the default push, background resets,
//   256-color and truecolor with incomplete/invalid channels
// - scanTerminalText: C1 CSI (0x9b), C1 OSC (0x9d), C1 string terminators,
//   intermediate bytes, CAN/CAN(0x18)/SUB(0x1a) in CSI, overflow sequences,
//   non-text control codes
// - AnsiTailBuffer: reset on source shrink, truncation behavior
// - parseAnsiLines: multi-line content in a single bundle

// Line 124: code === 38 with 256-color (code 5) — background variant (code 48)
test("256-color background sets the background color", () => {
  const [line] = parseAnsiLines("\u001b[48;5;42mbg\u001b[49m");
  expect(line).toMatchObject([{ text: "bg", background: { kind: "rgb", value: "0, 215, 135" } }]);
});

// Lines 153-155: code 49 (default background reset)
test("background reset code 49 clears an active background", () => {
  const [line] = parseAnsiLines("\u001b[44mblue\u001b[49m plain");
  expect(line).toMatchObject([
    { text: "blue", background: { kind: "named", name: "blue" } },
    { text: " plain", background: undefined },
  ]);
});

// Line 181: unknown code that falls through to the default normalized.push(code)
test("an unknown SGR code passes through without affecting state", () => {
  const [line] = parseAnsiLines("\u001b[99mtext\u001b[0m");
  expect(plainText([line ?? []])).toBe("text");
  expect(line?.[0]).toMatchObject({
    text: "text",
    foreground: undefined,
    background: undefined,
    bold: false,
    dim: false,
  });
});

// Line 181: code 22 resets both bold and dim
test("code 22 resets bold and dim together", () => {
  const [line] = parseAnsiLines("\u001b[1;2mbold+dim\u001b[22mplain");
  expect(line).toMatchObject([
    { text: "bold+dim", bold: true, dim: true },
    { text: "plain", bold: false, dim: false },
  ]);
});

// Line 181: code 21 (doubly-underlined) resets bold (decoration 1)
test("code 21 resets bold decoration", () => {
  const [line] = parseAnsiLines("\u001b[1mbold\u001b[21mplain");
  expect(line).toMatchObject([
    { text: "bold", bold: true },
    { text: "plain", bold: false },
  ]);
});

// Lines 316-317: escape state with code outside 0x20-0x2f and 0x30-0x7e
// falls through to the else branch (state.control = text, continue)
test("escape followed by an unrecognized control character returns to text", () => {
  expect(plainText(parseAnsiLines("a\u001b\u0000b"))).toBe("ab");
});

// Lines 330-332: CAN (0x18) and SUB (0x1a) inside a CSI sequence reset to text
test("CAN (0x18) inside a CSI sequence resets to text mode", () => {
  expect(plainText(parseAnsiLines("\u001b[31\u0018text"))).toBe("text");
});

test("SUB (0x1a) inside a CSI sequence resets to text mode", () => {
  expect(plainText(parseAnsiLines("\u001b[31\u001atext"))).toBe("text");
});

// Lines 345: CSI overflow — a sequence longer than MAX_CSI_SEQUENCE
// should not crash and should be dropped
test("an extremely long CSI sequence overflows and is dropped", () => {
  const longSeq = `\u001b[${"0".repeat(200)}mtext`;
  expect(plainText(parseAnsiLines(longSeq))).toBe("text");
});

// Line 351-356: C1 CSI (0x9b) starts a CSI directly
test("C1 CSI (0x9b) is recognized as a CSI sequence start", () => {
  const [line] = parseAnsiLines("\u009b31mred\u009b0m");
  expect(line).toMatchObject([{ text: "red", foreground: { kind: "named", name: "red" } }]);
});

// Line 353: C1 OSC (0x9d) starts an OSC
test("C1 OSC (0x9d) consumes its payload through BEL", () => {
  expect(plainText(parseAnsiLines("before\u009dtitle\u0007after"))).toBe("beforeafter");
});

// Line 354-355: C1 string introducers (0x90, 0x98, 0x9e, 0x9f)
test("C1 SOS (0x98) consumes its payload through ST", () => {
  expect(plainText(parseAnsiLines("a\u0098payload\u001b\\b"))).toBe("ab");
});

test("C1 PM (0x9e) consumes its payload through ST", () => {
  expect(plainText(parseAnsiLines("a\u009epayload\u001b\\b"))).toBe("ab");
});

test("C1 APC (0x9f) consumes its payload through ST", () => {
  expect(plainText(parseAnsiLines("a\u009fpayload\u001b\\b"))).toBe("ab");
});

test("C1 DCS (0x90) consumes its payload through ST", () => {
  expect(plainText(parseAnsiLines("a\u0090payload\u001b\\b"))).toBe("ab");
});

// Lines 310-313: escape intermediate bytes (0x20-0x2f)
test("escape with intermediate bytes followed by a final byte", () => {
  // ESC 20 40 = ESC SP @ — a valid sequence with intermediate byte
  expect(plainText(parseAnsiLines("a\u001b @b"))).toBe("ab");
});

// Lines 343-346: CSI with non-text control code inside the sequence
test("a BEL inside a CSI sequence is consumed as non-text control", () => {
  // BEL (0x07) is a non-text control (0x00-0x08), so it's consumed but
  // the CSI sequence continues. The 'text' after is emitted.
  expect(plainText(parseAnsiLines("\u001b[31\u0007text"))).toBe("ext");
});

// AnsiTailBuffer: reset on source shrink
test("AnsiTailBuffer resets when source shrinks", () => {
  const buf = new AnsiTailBuffer(100);
  const snap1 = buf.update("hello world");
  expect(snap1.renderedText).toBe("hello world");
  // Source shrinks — triggers reset
  const snap2 = buf.update("short");
  expect(snap2.renderedText).toBe("short");
  expect(snap2.truncated).toBe(false);
});

// AnsiTailBuffer: truncation flag
test("AnsiTailBuffer marks truncated when source exceeds max", () => {
  const buf = new AnsiTailBuffer(5);
  const snap = buf.update("a very long string that exceeds the max");
  expect(snap).toEqual({ renderedText: "e max", copyText: "e max", truncated: true });
});

// AnsiTailBuffer: incremental update
test("AnsiTailBuffer handles incremental appends", () => {
  const buf = new AnsiTailBuffer(50);
  buf.update("first ");
  const snap = buf.update("first second");
  expect(snap).toEqual({ renderedText: "first second", copyText: "first second", truncated: false });
});

// The raw SGR opener falls outside the retained tail, so the rendered tail must
// reconstruct that boundary state rather than losing the color.
test("AnsiTailBuffer carries SGR state across a truncation boundary", () => {
  const buf = new AnsiTailBuffer(5);
  buf.update("\u001b[31mabcdef");
  const snap = buf.update("\u001b[31mabcdefg");

  expect(snap.copyText).toBe("cdefg");
  expect(snap.truncated).toBe(true);
  expect(parseAnsiLines(snap.renderedText)[0]).toMatchObject([
    { text: "cdefg", foreground: { kind: "named", name: "red" } },
  ]);
});

// parseAnsiLines: empty string produces one empty line
test("parseAnsiLines with empty string produces one empty line", () => {
  const lines = parseAnsiLines("");
  expect(lines).toHaveLength(1);
  expect(lines[0]).toEqual([]);
});

// parseAnsiLines: a bundle with only newlines creates empty lines
test("parseAnsiLines handles content that is only newlines", () => {
  const lines = parseAnsiLines("\n\n");
  expect(lines).toEqual([[], [], []]);
});

test("byte rows preserve malformed source spans instead of re-encoding replacements", () => {
  const parsed = parseAnsiByteRows(new Uint8Array([0xff, 0xc3, 40, 10, 0xc3, 0xa9]), 70);
  expect(
    parsed.rows.map(({ offsetBytes, endBytes, line }) => ({ offsetBytes, endBytes, text: plainText([line]) })),
  ).toEqual([
    { offsetBytes: 70, endBytes: 74, text: "��(" },
    { offsetBytes: 74, endBytes: 76, text: "é" },
  ]);
});

test("byte fragments stay scalar-safe and keep style across long logical lines", () => {
  const bytes = new Uint8Array(8200).fill(65);
  bytes.set([27, 91, 51, 49, 59, 49, 109]);
  bytes.set([0xf0, 0x9f, 0x98, 0x80], 4095);
  const parsed = parseAnsiByteRows(bytes, 0);
  expect(parsed.rows.map((row) => [row.offsetBytes, row.endBytes])).toEqual([
    [0, 4095],
    [4095, 8191],
    [8191, 8200],
  ]);
  expect(parsed.rows.flatMap((row) => row.line.map((run) => run.text)).join("")).toBe(
    `${"A".repeat(4088)}😀${"A".repeat(4101)}`,
  );
  for (const row of parsed.rows)
    expect(row.line).toMatchObject([{ foreground: { kind: "named", name: "red" }, bold: true }]);
});

test("control-only byte fragments advance checkpoints without adding rows", () => {
  const bytes = new Uint8Array(8200).fill(120);
  bytes.set([27, 93]);
  const parsed = parseAnsiByteRows(bytes, 20);
  expect(parsed.rows).toEqual([]);
  expect(parsed.boundaries.map((boundary) => boundary.offsetBytes)).toEqual([20, 4116, 8212, 8220]);
  expect(parsed.endState.control.kind).toBe("osc");
});

test("byte checkpoints carry a split OSC and clone state before scanning", () => {
  const first = parseAnsiByteRows(new Uint8Array([27, 93, 120, 27]), 0);
  const second = parseAnsiByteRows(new Uint8Array([92, 65, 10]), 4, first.endState);
  expect(first.endState.control).toEqual({ kind: "osc", escape: true });
  expect(second.rows.map((row) => plainText([row.line]))).toEqual(["A"]);
  expect(second.endState.control).toEqual({ kind: "text" });
});

test("byte checkpoints carry a split CSI and actual decorations", () => {
  const first = parseAnsiByteRows(new Uint8Array([27, 91, 51, 49, 59, 49]), 0);
  const second = parseAnsiByteRows(new Uint8Array([109, 65, 10, 66]), 6, first.endState);
  expect(second.rows).toMatchObject([
    { offsetBytes: 6, endBytes: 9, line: [{ text: "A", foreground: { kind: "named", name: "red" }, bold: true }] },
    { offsetBytes: 9, endBytes: 10, line: [{ text: "B", foreground: { kind: "named", name: "red" }, bold: true }] },
  ]);
  expect(first.endState.control.kind).toBe("csi");
});

test("byte rows retain blank newline rows but do not invent an EOF row", () => {
  expect(parseAnsiByteRows(new Uint8Array([10, 10]), 0).rows).toEqual([
    { offsetBytes: 0, endBytes: 1, line: [], textByteOffsets: new Uint16Array() },
    { offsetBytes: 1, endBytes: 2, line: [], textByteOffsets: new Uint16Array() },
  ]);
  expect(parseAnsiByteRows(new Uint8Array(), 2).rows).toEqual([]);
});
