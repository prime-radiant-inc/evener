import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { afterEach, describe, expect, test, vi } from "vitest";

import { ShellCommandBlock } from "./index";
import { formatShellCommand, type ShellCommandLine, tokenizeShellCommand } from "./shellCommand";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
});

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

test("tokenizes shell constructs without changing token text", () => {
  const lines = formatShellCommand("printf '%s' \"$HOME\" && echo --name # note");
  const tokens = tokenizeShellCommand(lines);

  expect(
    tokens
      .flat()
      .map((part) => part.text)
      .join(""),
  ).toBe(lines.map((line) => line.text).join(""));
  expect(tokens.flat().map((part) => part.kind)).toEqual(
    expect.arrayContaining(["command", "string", "variable", "operator", "flag", "comment"]),
  );
});

test("tokenizes a quoted variable as disjoint source slices", () => {
  const lines = formatShellCommand('echo "prefix$HOME"');
  const tokens = tokenizeShellCommand(lines);

  expect(
    tokens
      .flat()
      .map((part) => part.text)
      .join(""),
  ).toBe(lines.map((line) => line.text).join(""));
  expect(tokens[0]).toEqual([
    { kind: "command", text: "echo" },
    { kind: "plain", text: " " },
    { kind: "string", text: '"prefix' },
    { kind: "variable", text: "$HOME" },
    { kind: "string", text: '"' },
  ]);
});

test("tokenizes an embedded unquoted variable as adjacent source slices", () => {
  const lines = formatShellCommand("echo prefix$HOME");
  const tokens = tokenizeShellCommand(lines);

  expect(
    tokens
      .flat()
      .map((part) => part.text)
      .join(""),
  ).toBe(lines.map((line) => line.text).join(""));
  expect(tokens[0]).toEqual([
    { kind: "command", text: "echo" },
    { kind: "plain", text: " " },
    { kind: "plain", text: "prefix" },
    { kind: "variable", text: "$HOME" },
  ]);
});

test("renders formatted shell lines, token kinds, and copies the raw command", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  const command = 'printf "two  spaces" \\ path && echo done';

  const { container } = render(createElement(ShellCommandBlock, { command }));

  expect(container.querySelector("code")?.textContent).toContain("\n");
  expect(container.querySelector('[data-shell-token-kind="command"]')).toBeTruthy();
  expect(container.querySelector('[data-shell-token-kind="operator"]')).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Copy command" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith(command);
});
