import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import { toolRendererFor } from "../toolRenderers";
import "./shellTool";
import type { ItemModel } from "../../../../protocol/model";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

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

// A2: the exit code stops being the summary's headline. The failure glyph
// (failed()) announces a nonzero exit; the number itself moves to detail().

test("summary: a nonzero exit is NOT in the summary text - the glyph carries that signal", () => {
  const d = toolRendererFor("shell");
  const out = "some output\n[exit 1]";
  expect(d.summary(withCommand("false", { output: out }))).toBe("Ran false");
});

test("failed: true for a nonzero exit parsed from the output footer", () => {
  const d = toolRendererFor("shell");
  expect(d.failed?.(withCommand("false", { output: "some output\n[exit 1]" }))).toBe(true);
});

test("failed: false on a clean exit - success reserves no glyph", () => {
  const d = toolRendererFor("shell");
  expect(d.failed?.(withCommand("true", { output: "x\n[exit 0]" }))).toBe(false);
});

test("failed: false when no exit code is detectable at all (still running/backgrounded)", () => {
  const d = toolRendererFor("shell");
  expect(d.failed?.(withCommand("sleep 10", { output: "still going" }))).toBe(false);
});

test("detail: the exit code stays reachable as the row's hover title", () => {
  const d = toolRendererFor("shell");
  expect(d.detail?.(withCommand("false", { output: "x\n[exit 1]" }))).toBe("exit 1");
});

test("detail: a clean exit still reports its code (0 is a fact, not an absence)", () => {
  const d = toolRendererFor("shell");
  expect(d.detail?.(withCommand("true", { exitCode: 0 }))).toBe("exit 0");
});

test("detail: undefined when no exit code exists at all", () => {
  const d = toolRendererFor("shell");
  expect(d.detail?.(withCommand("sleep 10", { output: "still going" }))).toBeUndefined();
});

test("summary: no footer at all (still running, or backgrounded) shows no exit suffix", () => {
  const d = toolRendererFor("shell");
  expect(d.summary(withCommand("sleep 10", { output: "partial output so far" }))).toBe("Ran sleep 10");
});

test("detail: also recognizes the buffered-execenv fallback's differently-shaped trailer (no brackets)", () => {
  // agent/session_tools_shell.go's runBufferedShell path (used when the
  // execution environment doesn't support streaming) has no StateResult/
  // bracketed footer at all - it ends in a bare
  // "exit_code=N duration_ms=N timed_out=bool" line instead.
  const d = toolRendererFor("shell");
  const out = "stdout here\nexit_code=2 duration_ms=15 timed_out=false";
  expect(d.detail?.(withCommand("false", { output: out }))).toBe("exit 2");
  expect(d.failed?.(withCommand("false", { output: out }))).toBe(true);
});

// Truncation is the ROW's presentation job, not the descriptor's: the row
// middle-truncates when collapsed and wraps in full when expanded, so the
// summary hands over the whole command (Jesse's review call - an expanded
// row must show the full call, which an 80-char descriptor clip made
// impossible).
test("summary: a long command passes through UNCLIPPED - truncation is the row's job", () => {
  const d = toolRendererFor("shell");
  const longCmd = "x".repeat(100);
  expect(d.summary(withCommand(longCmd))).toBe(`Ran ${"x".repeat(100)}`);
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

// --- typed exitCode (wire-honesty spec Part A) ----------------------------
// The daemon now promotes a shell call's process exit code onto the settled
// item as ItemModel.exitCode. That typed number is the primary signal; the
// output-footer text heuristic above stays only as the old-daemon fallback
// (every test above carries no exitCode, so those now exercise that fallback).

test("detail: uses the typed exitCode directly, with no output footer to parse", () => {
  const d = toolRendererFor("shell");
  expect(d.detail?.(withCommand("make test", { exitCode: 2, output: "boom" }))).toBe("exit 2");
});

test("detail/failed: the typed exitCode wins over a conflicting output footer", () => {
  const d = toolRendererFor("shell");
  // Typed 0 (clean) must not be overridden by a stray bracketed "[exit 5]" in
  // the command's own output text — the structured field is authoritative.
  const clean = withCommand("make test", { exitCode: 0, output: "done\n[exit 5]" });
  expect(d.detail?.(clean)).toBe("exit 0");
  expect(d.failed?.(clean)).toBe(false);
});

test("autoExpand: true from a typed nonzero exitCode with no footer text", () => {
  const d = toolRendererFor("shell");
  expect(d.autoExpand?.(withCommand("make test", { exitCode: 2, output: "boom" }))).toBe(true);
});

test("autoExpand: a typed exit 0 wins over a conflicting nonzero footer (no false auto-expand)", () => {
  const d = toolRendererFor("shell");
  expect(d.autoExpand?.(withCommand("make test", { exitCode: 0, output: "x\n[exit 5]" }))).toBe(false);
});

// --- body -----------------------------------------------------------------

test("the command reads straight from a settled item's own argumentsJSON (the model preserves it through item/completed - see R2)", () => {
  const d = toolRendererFor("shell");
  const settled = item({
    toolName: "shell",
    argumentsJSON: JSON.stringify({ command: "echo settled" }),
    output: "settled\n[exit 0]",
  });
  expect(d.summary(settled)).toBe("Ran echo settled");
});

test("body renders the output only - it does NOT repeat the command the row already named", () => {
  const d = toolRendererFor("shell");
  const Body = d.body!;
  const { container } = render(<Body item={withCommand("echo hi", { output: "hi\n[exit 0]" })} live={false} />);
  expect(screen.getByText(/\[exit 0\]/)).toBeTruthy();
  expect(container.textContent).not.toContain("$ echo hi");
});

test("all bash-family tool bodies render ANSI SGR output as styled text", () => {
  for (const toolName of ["shell", "exec_command", "run_shell_command"]) {
    const Body = toolRendererFor(toolName).body!;
    const { container, unmount } = render(
      <Body item={item({ toolName, output: "\u001b[32mPASS\u001b[0m" })} live={false} />,
    );

    expect(container.querySelector("code")?.textContent).toBe("PASS");
    expect(container.querySelector('[data-ansi-fg="green"]')?.textContent).toBe("PASS");
    unmount();
  }
});

test("a live shell tail never starts inside an ANSI sequence", () => {
  const Body = toolRendererFor("shell").body!;
  // The naive 8,000-code-unit cut lands on the "3" in ESC[32m.
  const output = `${"p".repeat(50)}\u001b[32m${"K".repeat(7_997)}`;
  const { container } = render(<Body item={withCommand("long-running", { output })} live />);

  expect(container.querySelector("code")?.textContent).toBe("K".repeat(7_997));
  expect(container.querySelector('[data-ansi-fg="green"]')?.textContent).toBe("K".repeat(7_997));
  expect(container.textContent).not.toContain("[32m");
});

test("a settled shell tail restores styling that began before the retained output", () => {
  const Body = toolRendererFor("shell").body!;
  const output = `plain\u001b[32m${"x".repeat(8_100)}KEPT\u001b[0m`;
  const { container } = render(<Body item={withCommand("long-run", { output })} live={false} />);

  expect(container.querySelector("code")?.textContent).toBe(
    `earlier output not retained — showing the last 8,000 chars\n${"x".repeat(7_992)}KEPT`,
  );
  expect(container.querySelector('[data-ansi-fg="green"]')?.textContent).toBe(`${"x".repeat(7_992)}KEPT`);
});

test("a retained tail preserves inverse semantics for later color changes", () => {
  const Body = toolRendererFor("shell").body!;
  const output = `\u001b[7m${"x".repeat(8_100)}\u001b[31mAFTER`;
  render(<Body item={withCommand("long-run", { output })} live={false} />);

  expect(screen.getByText("AFTER").closest('[data-ansi-bg="red"]')).toBeTruthy();
});

test("rolling ANSI state is isolated when an item ID repeats in another session", () => {
  const Body = toolRendererFor("shell").body!;
  const view = render(<Body item={withCommand("first", { output: "ONE1" })} live sessionRef="local:session-one" />);

  view.rerender(<Body item={withCommand("second", { output: "TWO2" })} live sessionRef="local:session-two" />);
  expect(view.container.querySelector("code")?.textContent).toBe("TWO2");
});

test("authoritative completed output replaces a non-prefix live snapshot", () => {
  const Body = toolRendererFor("shell").body!;
  const view = render(<Body item={withCommand("run", { output: "live" })} live sessionRef="local:session-one" />);

  view.rerender(
    <Body item={withCommand("run", { output: "settled output" })} live={false} sessionRef="local:session-one" />,
  );
  expect(view.container.querySelector("code")?.textContent).toBe("settled output");
});

test("an ESC intermediate before the raw boundary makes a retained bracket a final byte", () => {
  const Body = toolRendererFor("shell").body!;
  const output = `${"p".repeat(50)}\u001b(${"["}${"V".repeat(7_999)}`;
  const { container } = render(<Body item={withCommand("long-run", { output })} live={false} />);

  expect(container.querySelector("code")?.textContent).toBe(
    `earlier output not retained — showing the last 8,000 chars\n${"V".repeat(7_999)}`,
  );
});

test("SGR 21 resets inherited bold without suppressing a later re-enable", () => {
  const Body = toolRendererFor("shell").body!;
  const output = `\u001b[1m${"x".repeat(8_100)}\u001b[21mplain\u001b[1mAFTER`;
  const { container } = render(<Body item={withCommand("long-run", { output })} live={false} />);

  expect(
    Array.from(container.querySelectorAll("[data-ansi-bold]")).some((node) => node.textContent?.endsWith("AFTER")),
  ).toBe(true);
});

test("a malformed extended color does not replace inherited valid color state", () => {
  const Body = toolRendererFor("shell").body!;
  const output = `\u001b[31m\u001b[38;2;999;0;0m${"x".repeat(8_100)}AFTER`;
  const { container } = render(<Body item={withCommand("long-run", { output })} live={false} />);

  expect(
    Array.from(container.querySelectorAll('[data-ansi-fg="red"]')).some((node) => node.textContent?.endsWith("AFTER")),
  ).toBe(true);
});

test("incomplete truecolor parameters retain Anser reset and bold semantics at the boundary", () => {
  const Body = toolRendererFor("shell").body!;
  const output = `\u001b[31m\u001b[38;2;0;1m${"x".repeat(8_100)}AFTER`;
  const { container } = render(<Body item={withCommand("long-run", { output })} live={false} />);

  expect(
    Array.from(container.querySelectorAll("[data-ansi-bold]")).some((node) => node.textContent?.endsWith("AFTER")),
  ).toBe(true);
  expect(container.querySelector('[data-ansi-fg="red"]')).toBeNull();
});

test("an empty palette field does not replace inherited color at the boundary", () => {
  const Body = toolRendererFor("shell").body!;
  const output = `\u001b[31m\u001b[38;5;m${"x".repeat(8_100)}AFTER`;
  const { container } = render(<Body item={withCommand("long-run", { output })} live={false} />);

  expect(
    Array.from(container.querySelectorAll('[data-ansi-fg="red"]')).some((node) => node.textContent?.endsWith("AFTER")),
  ).toBe(true);
});

test("display and copy share the same raw boundary across a large OSC payload", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  const Body = toolRendererFor("shell").body!;
  const output = `OLD\u001b]${"p".repeat(9_000)}\u0007NEW`;
  const { container } = render(<Body item={withCommand("long-run", { output })} live={false} />);

  expect(container.querySelector("code")?.textContent).toBe(
    "earlier output not retained — showing the last 8,000 chars\nNEW",
  );
  await user.click(screen.getByRole("button", { name: "Copy output" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith(output.slice(-8_000));
});

test("copying a bounded shell result preserves the original retained bytes", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  const Body = toolRendererFor("shell").body!;
  const output = `plain\u001b[32m${"x".repeat(8_100)}KEPT\u001b[0m`;
  render(<Body item={withCommand("long-run", { output })} live={false} />);

  await user.click(screen.getByRole("button", { name: "Copy output" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith(output.slice(-8_000));
});

test("a live shell tail carries presentation state forward using only appended output", () => {
  const Body = toolRendererFor("shell").body!;
  const firstOutput = `plain\u001b[32m${"x".repeat(8_100)}`;
  const view = render(<Body item={withCommand("long-run", { output: firstOutput })} live />);

  view.rerender(<Body item={withCommand("long-run", { output: `${firstOutput}KEPT` })} live />);
  expect(view.container.querySelector('[data-ansi-fg="green"]')?.textContent).toContain("KEPT");
});

test("a live shell tail carries an incomplete terminal control across output deltas", () => {
  const Body = toolRendererFor("shell").body!;
  const firstOutput = `${"x".repeat(8_100)}\u001b]split ti`;
  const view = render(<Body item={withCommand("long-run", { output: firstOutput })} live />);

  const completedOutput = `${firstOutput}tle\u0007after`;
  view.rerender(<Body item={withCommand("long-run", { output: completedOutput })} live />);
  expect(view.container.querySelector("code")?.textContent).toBe(`${"x".repeat(7_981)}after`);
  expect(view.container.textContent).not.toContain("title");
});

test("unterminated control payloads remain bounded across many live deltas", () => {
  const Body = toolRendererFor("shell").body!;
  let output = `${"x".repeat(8_100)}\u001b]`;
  const view = render(<Body item={withCommand("long-run", { output })} live />);

  for (let index = 0; index < 64; index += 1) {
    output += "payload".repeat(80);
    view.rerender(<Body item={withCommand("long-run", { output })} live />);
  }
  expect(view.container.querySelector("code")?.textContent).toBe("");

  output += "\u0007done";
  view.rerender(<Body item={withCommand("long-run", { output })} live />);
  expect(view.container.querySelector("code")?.textContent).toBe("done");
});

test("body renders nothing for a command with no output at all", () => {
  const d = toolRendererFor("shell");
  const Body = d.body!;
  const { container } = render(<Body item={withCommand("true", { output: "" })} live={false} />);
  expect(container.textContent).toBe("");
});

// detail() carries the exit code ONLY - deliberately not the untruncated
// command as well. detail() renders as real text in the expanded body (which is
// what makes it keyboard-reachable rather than a mouse-only title), so folding
// the command in would put a second copy of the call under the row: exactly the
// repetition A4 removes.
test("detail does NOT repeat the command - that is what A4 took out of the body", () => {
  const d = toolRendererFor("shell");
  const longCmd = "x".repeat(100);
  expect(d.summary(withCommand(longCmd))).toBe(`Ran ${"x".repeat(100)}`);
  expect(d.detail?.(withCommand(longCmd, { exitCode: 0 }))).toBe("exit 0");
});
