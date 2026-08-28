import { expect, test } from "vitest";
import { formatShellCommand, tokenizeShellCommand } from "./shellCommand";

test("an unclosed braced variable consumes the rest of a double-quoted word", () => {
  const lines = formatShellCommand('echo "${UNCLOSED"');

  expect(tokenizeShellCommand(lines)[0]).toEqual([
    { kind: "command", text: "echo" },
    { kind: "plain", text: " " },
    { kind: "string", text: '"' },
    { kind: "variable", text: '${UNCLOSED"' },
  ]);
});

test("escaped characters remain inside one double-quoted string token", () => {
  const lines = formatShellCommand('echo "hello\\nworld"');

  expect(tokenizeShellCommand(lines)[0]).toEqual([
    { kind: "command", text: "echo" },
    { kind: "plain", text: " " },
    { kind: "string", text: '"hello\\nworld"' },
  ]);
});

test("an escaped quote does not end a double-quoted string token", () => {
  const lines = formatShellCommand('echo "test\\"escaped"');

  expect(tokenizeShellCommand(lines)[0]).toEqual([
    { kind: "command", text: "echo" },
    { kind: "plain", text: " " },
    { kind: "string", text: '"test\\"escaped"' },
  ]);
});

test("variable scanning recognizes braced, special, and lone-dollar forms", () => {
  const variables = [`echo "\${VAR}"`, "echo $?", "echo $"]
    .flatMap((command) => tokenizeShellCommand(formatShellCommand(command)).flat())
    .filter((token) => token.kind === "variable")
    .map((token) => token.text);

  expect(variables).toEqual([`\${VAR}`, "$?", "$"]);
});

test("operators inside a subshell do not split until the outer pipeline", () => {
  expect(formatShellCommand("(cmd1 && cmd2) | grep result")).toEqual([
    { text: "(cmd1 && cmd2) | ", indent: 0 },
    { text: "grep result", indent: 2 },
  ]);
  expect(formatShellCommand("(cmd1; (cmd2; cmd3))")).toEqual([{ text: "(cmd1; (cmd2; cmd3))", indent: 0 }]);
});

test("single quotes suppress variable tokenization", () => {
  const tokens = tokenizeShellCommand(formatShellCommand("echo '$HOME'"))[0];

  expect(tokens).toEqual([
    { kind: "command", text: "echo" },
    { kind: "plain", text: " " },
    { kind: "string", text: "'$HOME'" },
  ]);
});

test("backticks form one string token", () => {
  expect(tokenizeShellCommand(formatShellCommand("echo `date`"))[0]).toEqual([
    { kind: "command", text: "echo" },
    { kind: "plain", text: " " },
    { kind: "string", text: "`date`" },
  ]);
});

test("a continued double-quoted string preserves quote state across source lines", () => {
  const tokens = tokenizeShellCommand(formatShellCommand('echo "hello \\\nworld"'));

  expect(tokens).toEqual([
    [
      { kind: "command", text: "echo" },
      { kind: "plain", text: " " },
      { kind: "string", text: '"hello \\' },
    ],
    [{ kind: "string", text: 'world"' }],
  ]);
});

test("a here-string is tokenized as redirection rather than a command boundary", () => {
  const lines = formatShellCommand("cat <<< input");

  expect(lines).toEqual([{ text: "cat <<< input", indent: 0 }]);
  expect(tokenizeShellCommand(lines)[0]).toEqual([
    { kind: "command", text: "cat" },
    { kind: "plain", text: " " },
    { kind: "operator", text: "<<<" },
    { kind: "plain", text: " " },
    { kind: "plain", text: "input" },
  ]);
});
