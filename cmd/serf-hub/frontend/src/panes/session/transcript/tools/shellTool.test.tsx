import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { toolRendererFor } from "../toolRenderers";
import "./shellTool";
import type { ItemModel } from "../../../../protocol/model";

afterEach(cleanup);

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

function withCommand(command: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return item({ toolName: "shell", argumentsJSON: JSON.stringify({ command }), ...overrides });
}

// --- summary ----------------------------------------------------------

test("summary: leads with the command, no result suffix on a clean run", () => {
  const d = toolRendererFor("shell");
  // agent/session_tools_shell.go formatShellResult's own footer shape.
  const out = "ok  \tagent/... 0.4s\n[exit 0]";
  expect(d.summary(withCommand("go test ./...", { output: out }))).toBe("Ran go test ./...");
});

test("summary: a nonzero exit parsed from the output footer is appended", () => {
  const d = toolRendererFor("shell");
  const out = "some output\n[exit 1]";
  expect(d.summary(withCommand("false", { output: out }))).toBe("Ran false · exit 1");
});

test("summary: no footer at all (still running, or backgrounded) shows no exit suffix", () => {
  const d = toolRendererFor("shell");
  expect(d.summary(withCommand("sleep 10", { output: "partial output so far" }))).toBe("Ran sleep 10");
});

test("summary: also recognizes the buffered-execenv fallback's differently-shaped trailer (no brackets)", () => {
  // agent/session_tools_shell.go's runBufferedShell path (used when the
  // execution environment doesn't support streaming) has no StateResult/
  // bracketed footer at all - it ends in a bare
  // "exit_code=N duration_ms=N timed_out=bool" line instead.
  const d = toolRendererFor("shell");
  const out = "stdout here\nexit_code=2 duration_ms=15 timed_out=false";
  expect(d.summary(withCommand("false", { output: out }))).toBe("Ran false · exit 2");
});

test("summary: a long command is clipped", () => {
  const d = toolRendererFor("shell");
  const longCmd = "x".repeat(100);
  expect(d.summary(withCommand(longCmd))).toBe(`Ran ${"x".repeat(80)}…`);
});

test("summary: falls back to the `cmd` arg key when `command` is absent", () => {
  const d = toolRendererFor("shell");
  const args = JSON.stringify({ cmd: "ls" });
  expect(d.summary(item({ toolName: "shell", argumentsJSON: args }))).toBe("Ran ls");
});

test("exec_command and run_shell_command alias to the same descriptor as shell", () => {
  const shell = toolRendererFor("shell");
  expect(toolRendererFor("exec_command")).toBe(shell);
  expect(toolRendererFor("run_shell_command")).toBe(shell);
});

// --- autoExpand ---------------------------------------------------------

test("autoExpand: true when the parsed exit code is nonzero", () => {
  const d = toolRendererFor("shell");
  expect(d.autoExpand?.(withCommand("false", { output: "x\n[exit 1]" }))).toBe(true);
});

test("autoExpand: false on a clean exit", () => {
  const d = toolRendererFor("shell");
  expect(d.autoExpand?.(withCommand("true", { output: "x\n[exit 0]" }))).toBe(false);
});

test("autoExpand: true for a nonzero exit reported via the buffered fallback's exit_code= trailer", () => {
  const d = toolRendererFor("shell");
  const out = "stdout\nexit_code=1 duration_ms=5 timed_out=false";
  expect(d.autoExpand?.(withCommand("false", { output: out }))).toBe(true);
});

test("autoExpand: false when no exit code is detectable at all (no false failure signal)", () => {
  const d = toolRendererFor("shell");
  expect(d.autoExpand?.(withCommand("sleep 10", { output: "still going" }))).toBe(false);
});

// --- body -----------------------------------------------------------------

test("the command survives once the item settles and argumentsJSON goes missing, via rememberedArgs", () => {
  const d = toolRendererFor("shell");
  const callId = "shell_settle_1";
  d.summary(item({ toolName: "shell", callId, argumentsJSON: JSON.stringify({ command: "echo settled" }) }));
  const settled = item({ toolName: "shell", callId, argumentsJSON: undefined, output: "settled\n[exit 0]" });
  expect(d.summary(settled)).toBe("Ran echo settled");
});

test("body renders the command header and the output", () => {
  const d = toolRendererFor("shell");
  const Body = d.body!;
  render(<Body item={withCommand("echo hi", { output: "hi\n[exit 0]" })} live={false} />);
  expect(screen.getByText("$ echo hi")).toBeTruthy();
  // "hi" alone would also match the header's own "echo hi" text - assert on
  // the output-specific footer text instead to target the output body.
  expect(screen.getByText(/\[exit 0\]/)).toBeTruthy();
});
