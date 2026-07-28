import { expect, test } from "vitest";
import type { AnsiLine } from "./ansi";
import { parseAnsiLines } from "./ansi";

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

test("a malformed escape byte cannot leak into rendered text", () => {
  const lines = parseAnsiLines("safe\u001bbroken");

  expect(plainText(lines)).toBe("safebroken");
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
