import { describe, expect, test } from "vitest";

import { formatShellCommand, type ShellCommandLine } from "./shellCommand";

function sourceWithoutNewlines(lines: readonly ShellCommandLine[]): string {
  return lines.map((line) => line.text).join("");
}

describe("formatShellCommand", () => {
  const cases = [
    {
      name: "chains and pipelines",
      raw: "cd /tmp && echo ok; printf '%s\\n' \"$HOME\" | tee out",
      want: [
        { text: "cd /tmp && ", indent: 0 },
        { text: "echo ok; ", indent: 2 },
        { text: "printf '%s\\n' \"$HOME\" | ", indent: 2 },
        { text: "tee out", indent: 2 },
      ],
    },
    {
      name: "longest operators",
      raw: "a || b |& c || d",
      want: [
        { text: "a || ", indent: 0 },
        { text: "b |& ", indent: 2 },
        { text: "c || ", indent: 2 },
        { text: "d", indent: 2 },
      ],
    },
    {
      name: "protected operators",
      raw:
        "printf '%s' \"a;b && c\" " +
        String.fromCharCode(96) +
        "echo x;y" +
        String.fromCharCode(96) +
        " foo\\;bar && done",
      want: [
        {
          text:
            "printf '%s' \"a;b && c\" " +
            String.fromCharCode(96) +
            "echo x;y" +
            String.fromCharCode(96) +
            " foo\\;bar && ",
          indent: 0,
        },
        { text: "done", indent: 2 },
      ],
    },
    {
      name: "comments stop operator scanning",
      raw: "echo hi # && hidden; text\nprintf done",
      want: [
        { text: "echo hi # && hidden; text", indent: 0 },
        { text: "printf done", indent: 0 },
      ],
    },
    {
      name: "nested substitutions stay opaque",
      raw: "echo $(printf 'a;b' && printf c) && echo done",
      want: [
        { text: "echo $(printf 'a;b' && printf c) && ", indent: 0 },
        { text: "echo done", indent: 2 },
      ],
    },
    {
      name: "source continuation is retained",
      // This raw fixture contains two runtime backslashes; both must survive the line split.
      raw: 'printf "left\\\\\nright" && echo done',
      want: [
        { text: 'printf "left\\\\', indent: 0 },
        { text: 'right" && ', indent: 0 },
        { text: "echo done", indent: 2 },
      ],
    },
    {
      name: "malformed and trailing input stays intact",
      raw: 'echo "unterminated &&',
      want: [{ text: 'echo "unterminated &&', indent: 0 }],
    },
    {
      name: "empty input has one line",
      raw: "",
      want: [{ text: "", indent: 0 }],
    },
    {
      name: "trailing operator has no empty line",
      raw: "echo done &&",
      want: [{ text: "echo done &&", indent: 0 }],
    },
  ] as const;

  test.each(cases)("$name", ({ raw, want }) => {
    const got = formatShellCommand(raw);
    expect(got).toEqual(want);
    expect(sourceWithoutNewlines(got)).toBe(raw.replaceAll("\n", ""));
  });

  test("a source newline creates a line without synthetic indentation", () => {
    expect(formatShellCommand("one\ntwo")).toEqual([
      { text: "one", indent: 0 },
      { text: "two", indent: 0 },
    ]);
  });

  test("an operator before an existing newline does not create a blank line", () => {
    expect(formatShellCommand("one &&\ntwo")).toEqual([
      { text: "one &&", indent: 0 },
      { text: "two", indent: 0 },
    ]);
  });

  test("a backslash in single quotes does not protect the closing quote", () => {
    const raw = "printf '%s' 'a\\' ; echo done";
    const got = formatShellCommand(raw);

    expect(got).toEqual([
      { text: "printf '%s' 'a\\' ; ", indent: 0 },
      { text: "echo done", indent: 2 },
    ]);
    expect(sourceWithoutNewlines(got)).toBe(raw);
  });
});
