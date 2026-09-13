import { expect, test } from "vitest";
import { parseAnsiLines } from "../../cmd/evener-hub/frontend/src/widgets/codeblock/ansi";
import { ansiRunTextStyle } from "./ansiOutputStyles";

function parsedRun(lines: ReturnType<typeof parseAnsiLines>, index: number) {
  const run = lines[index]?.[0];
  if (run === undefined) throw new Error(`expected a run on line ${index}`);
  return run;
}

test("maps parsed named and RGB colors plus all text decorations", () => {
  const [line] = parseAnsiLines(
    "\u001b[1;2;3;4;9;94;48;2;12;34;56mstyled\u001b[0m",
  );
  expect(ansiRunTextStyle(parsedRun([line ?? []], 0), false)).toEqual({
    color: "#2E5FD0",
    backgroundColor: "rgb(12, 34, 56)",
    fontWeight: "600",
    opacity: 0.65,
    fontStyle: "italic",
    textDecorationLine: "underline line-through",
  });
});

test("inherits outer color when parsed ANSI foreground is absent", () => {
  const [line] = parseAnsiLines("plain");

  expect(ansiRunTextStyle(parsedRun([line ?? []], 0), true)).toEqual({});
});

test("preserves parser style across lines and selective resets", () => {
  const lines = parseAnsiLines("\u001b[31;1mone\ntwo\u001b[22;39m\nthree");

  expect(lines).toHaveLength(3);
  expect(ansiRunTextStyle(parsedRun(lines, 0), false)).toMatchObject({
    color: "#A33226",
    fontWeight: "600",
  });
  expect(ansiRunTextStyle(parsedRun(lines, 1), false)).toMatchObject({
    color: "#A33226",
    fontWeight: "600",
  });
  expect(ansiRunTextStyle(parsedRun(lines, 2), false)).toEqual({});
});

test("maps parser reverse output to foreground and background colors", () => {
  const [line] = parseAnsiLines("\u001b[7mreverse\u001b[27m");

  expect(ansiRunTextStyle(parsedRun([line ?? []], 0), false)).toEqual({
    color: "#1F2124",
    backgroundColor: "#62656B",
  });
});

test("keeps blank lines as separate parsed lines", () => {
  const lines = parseAnsiLines("first\n\nthird");

  expect(lines).toHaveLength(3);
  expect(lines[1]).toEqual([]);
});

test("concealed runs preserve their layout but override all visible opacity", () => {
  const run = parsedRun(
    parseAnsiLines("\u001b[2;8;31;44mconcealed\u001b[28m"),
    0,
  );
  expect(run.hidden).toBe(true);
  expect(ansiRunTextStyle(run, true)).toMatchObject({ opacity: 0 });
});
