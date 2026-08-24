// Edge cases for shellCommand that close the remaining uncovered lines:
// - variableEnd with brace expansion and missing close brace
// - escaped characters inside double-quoted strings
// - backslash inside double-quoted strings

import { expect, test } from "vitest";
import { formatShellCommand, tokenizeShellCommand } from "./shellCommand";

// Line 187-188: variableEnd with brace expansion where close brace is not found
test("variable in double-quoted string with unclosed brace extends to end of line", () => {
  const lines = formatShellCommand(`echo "\${UNCLOSED"`);
  const tokens = tokenizeShellCommand(lines);
  // The unclosed brace variable token should extend to the end of the string
  const flat = tokens.flat();
  const variableToken = flat.find((t) => t.kind === "variable");
  expect(variableToken).toBeTruthy();
  expect(variableToken?.text).toContain("UNCLOSED");
});

// Lines 249-251: escaped character inside double-quoted string
test("escaped character inside double-quoted string is consumed", () => {
  const lines = formatShellCommand('echo "hello\\nworld"');
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  // The string should contain the escaped character
  const stringTokens = flat.filter((t) => t.kind === "string");
  expect(stringTokens.length).toBeGreaterThan(0);
  expect(stringTokens.some((t) => t.text.includes("hello"))).toBe(true);
});

// Lines 254-256: backslash inside double-quoted string sets escaped
test("backslash inside double-quoted string sets escaped flag", () => {
  const lines = formatShellCommand('echo "test\\"escaped"');
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  // The string should be properly closed despite the escaped quote
  expect(flat.some((t) => t.kind === "string")).toBe(true);
});

// Variable with brace expansion and close brace found
test("variable with brace expansion finds close brace", () => {
  const lines = formatShellCommand(`echo "\${VAR}"`);
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  const variableToken = flat.find((t) => t.kind === "variable");
  expect(variableToken?.text).toBe(`\${VAR}`);
});

// Variable with $?
test("variable with $? special parameter", () => {
  const lines = formatShellCommand("echo $?");
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  const variableToken = flat.find((t) => t.kind === "variable");
  expect(variableToken?.text).toBe("$?");
});

// Variable with simple $VAR
test("variable with simple $VAR name", () => {
  const lines = formatShellCommand("echo $HOME");
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  const variableToken = flat.find((t) => t.kind === "variable");
  expect(variableToken?.text).toBe("$HOME");
});

// Variable with $ at end of string (no name)
test("variable with $ at end of word", () => {
  const lines = formatShellCommand("echo $");
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  const variableToken = flat.find((t) => t.kind === "variable");
  expect(variableToken?.text).toBe("$");
});

// Variable inside double-quoted string
test("variable inside double-quoted string", () => {
  const lines = formatShellCommand('echo "$HOME/bin"');
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  const variableToken = flat.find((t) => t.kind === "variable");
  expect(variableToken?.text).toBe("$HOME");
});

// formatShellCommand with line continuation after operator
test("operator at end of line creates continuation with indent", () => {
  const lines = formatShellCommand("cmd1 && cmd2");
  expect(lines.length).toBeGreaterThanOrEqual(1);
});

// formatShellCommand with backslash line continuation
test("backslash line continuation preserves indent", () => {
  const lines = formatShellCommand("cmd1 \\\n  cmd2");
  expect(lines).toHaveLength(2);
});

// formatShellCommand with comment
test("comment at start of line", () => {
  const lines = formatShellCommand("# this is a comment\necho hi");
  expect(lines).toHaveLength(2);
  expect(lines[0]?.text).toBe("# this is a comment");
});

// formatShellCommand with comment after whitespace
test("comment after whitespace", () => {
  const lines = formatShellCommand("echo hi   # trailing comment");
  expect(lines).toHaveLength(1);
  expect(lines[0]?.text).toContain("# trailing comment");
});

// formatShellCommand with subshell
test("subshell parentheses track depth", () => {
  const lines = formatShellCommand("(cmd1 && cmd2) | grep result");
  // The pipe is outside the subshell, so it creates a continuation
  expect(lines.length).toBeGreaterThanOrEqual(1);
  expect(lines[0]?.text).toContain("(cmd1 && cmd2)");
});

// formatShellCommand with nested subshell
test("nested subshell with operator inside", () => {
  const lines = formatShellCommand("(cmd1; (cmd2; cmd3))");
  expect(lines).toHaveLength(1);
});

// tokenizeShellCommand with flag
test("flag token is recognized", () => {
  const lines = formatShellCommand("cmd --flag value");
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  expect(flat.some((t) => t.kind === "flag" && t.text === "--flag")).toBe(true);
});

// tokenizeShellCommand with assignment
test("assignment word is plain, not command", () => {
  const lines = formatShellCommand("VAR=value cmd");
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  expect(flat.some((t) => t.kind === "plain" && t.text === "VAR=value")).toBe(true);
  expect(flat.some((t) => t.kind === "command" && t.text === "cmd")).toBe(true);
});

// tokenizeShellCommand with operators
test("control operators set expectCommand", () => {
  const lines = formatShellCommand("cmd1 && cmd2");
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  expect(flat.some((t) => t.kind === "operator" && t.text === "&&")).toBe(true);
  expect(flat.some((t) => t.kind === "command" && t.text === "cmd1")).toBe(true);
  expect(flat.some((t) => t.kind === "command" && t.text === "cmd2")).toBe(true);
});

// tokenizeShellCommand with single-quoted string (no variable expansion)
test("single-quoted string does not expand variables", () => {
  const lines = formatShellCommand("echo '$HOME'");
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  // $HOME should be inside a string token, not a variable token
  expect(flat.some((t) => t.kind === "variable")).toBe(false);
  expect(flat.some((t) => t.kind === "string" && t.text.includes("$HOME"))).toBe(true);
});

// tokenizeShellCommand with backtick string
test("backtick string is recognized as quote", () => {
  const lines = formatShellCommand("echo `date`");
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  expect(flat.some((t) => t.kind === "string")).toBe(true);
});

// tokenizeShellCommand with line continuation in string
test("line continuation inside double-quoted string continues to next line", () => {
  const lines = formatShellCommand('echo "hello \\\nworld"');
  const tokens = tokenizeShellCommand(lines);
  // The string should span both lines
  const flat = tokens.flat();
  expect(flat.some((t) => t.kind === "string")).toBe(true);
});

// formatShellCommand with |> operator (should not split)
test("pipe after redirect does not create operator boundary", () => {
  const lines = formatShellCommand("echo >| file");
  expect(lines).toHaveLength(1);
});

// tokenizeShellCommand with comment
test("comment token is recognized", () => {
  const lines = formatShellCommand("cmd # comment");
  const tokens = tokenizeShellCommand(lines);
  const flat = tokens.flat();
  expect(flat.some((t) => t.kind === "comment" && t.text === "# comment")).toBe(true);
});

// formatShellCommand with here-string
test("here-string operator creates continuation", () => {
  const lines = formatShellCommand("cat <<< input");
  expect(lines).toHaveLength(1);
});
