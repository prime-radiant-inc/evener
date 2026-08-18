import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { toolRendererFor } from "../toolRenderers";
import { stripRedundantCd } from "./shellTool";
import "./shellTool";
import type { ItemModel, ThreadModel } from "../../../../protocol/model";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

beforeEach(() => resetThreadsStoreForTests());

// Mirrors fileOpenBeside.test.tsx's own seedThreadCwd - the by-ref threads
// store selector ShellBody and ToolCallItem's summary() call site both read.
function seedThreadCwd(ref: string, cwd: string): void {
  const model = { ref, cwd, turns: [] } as unknown as ThreadModel;
  threadsStore.setState({ threads: new Map([[ref, model]]) });
}

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "commandExecution", text: "", ...overrides };
}

function withCommand(command: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return item({ toolName: "shell", argumentsJSON: JSON.stringify({ command }), ...overrides });
}

function outputCode(container: HTMLElement): HTMLElement | null {
  const codes = container.querySelectorAll("code");
  return codes.item(codes.length - 1);
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
// middle-truncates when collapsed, and expanded shell rows drop the summary
// entirely (the body renders the command pretty-printed), so the summary
// hands over the whole command (Jesse's review call - an 80-char descriptor
// clip made the full command unreachable).
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

test("summary: strips the redundant cd-cwd prefix when a ToolSummaryContext cwd is given", () => {
  const d = toolRendererFor("shell");
  expect(d.summary(withCommand("cd /Users/jesse/work && make test"), { cwd: "/Users/jesse/work" })).toBe(
    "Ran make test",
  );
});

test("summary: with no context (or no cwd), the prefix passes through unstripped", () => {
  const d = toolRendererFor("shell");
  expect(d.summary(withCommand("cd /Users/jesse/work && make test"))).toBe("Ran cd /Users/jesse/work && make test");
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

test("body renders the raw formatted command above the existing output block", () => {
  const Body = toolRendererFor("shell").body!;
  const raw = "cd /tmp && printf '%s\\n' \"$HOME\"";
  const { container } = render(<Body item={withCommand(raw, { output: "ok\n[exit 0]" })} live={false} />);
  const codes = Array.from(container.querySelectorAll("code"));
  expect(codes).toHaveLength(2);
  expect(codes[0]?.textContent).toContain("cd /tmp &&");
  expect(codes[0]?.textContent).toContain("printf '%s\\n' \"$HOME\"");
  expect(codes[1]?.textContent).toContain("ok");
  expect(container.textContent).not.toContain("$ ");
});

test("body copies the exact raw command", async () => {
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  const Body = toolRendererFor("shell").body!;
  const raw = "cd /tmp &&  printf '%s\\n' \"$HOME\"";
  render(<Body item={withCommand(raw, { output: "ok" })} live={false} />);

  await user.click(screen.getByRole("button", { name: "Copy command" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith(raw);
});

test("body strips the redundant cd-cwd prefix from the rendered command block", () => {
  seedThreadCwd("ref_a", "/Users/jesse/work");
  const Body = toolRendererFor("shell").body!;
  const { container } = render(
    <Body item={withCommand("cd /Users/jesse/work && make test", { output: "" })} live={false} sessionRef="ref_a" />,
  );
  expect(container.querySelector("code")?.textContent).toBe("make test");
});

test("body's Copy command affordance still copies the ORIGINAL command (display-only strip)", async () => {
  seedThreadCwd("ref_a", "/Users/jesse/work");
  const user = userEvent.setup();
  const writeText = vi.spyOn(navigator.clipboard, "writeText");
  const Body = toolRendererFor("shell").body!;
  const raw = "cd /Users/jesse/work && make test";
  render(<Body item={withCommand(raw, { output: "" })} live={false} sessionRef="ref_a" />);

  await user.click(screen.getByRole("button", { name: "Copy command" }));
  expect(writeText).toHaveBeenCalledExactlyOnceWith(raw);
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

  expect(outputCode(container)?.textContent).toBe("K".repeat(7_997));
  expect(container.querySelector('[data-ansi-fg="green"]')?.textContent).toBe("K".repeat(7_997));
  expect(container.textContent).not.toContain("[32m");
});

test("a settled shell tail restores styling that began before the retained output", () => {
  const Body = toolRendererFor("shell").body!;
  const output = `plain\u001b[32m${"x".repeat(8_100)}KEPT\u001b[0m`;
  const { container } = render(<Body item={withCommand("long-run", { output })} live={false} />);

  expect(outputCode(container)?.textContent).toBe(
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
  expect(outputCode(view.container)?.textContent).toBe("TWO2");
});

test("authoritative completed output replaces a non-prefix live snapshot", () => {
  const Body = toolRendererFor("shell").body!;
  const view = render(<Body item={withCommand("run", { output: "live" })} live sessionRef="local:session-one" />);

  view.rerender(
    <Body item={withCommand("run", { output: "settled output" })} live={false} sessionRef="local:session-one" />,
  );
  expect(outputCode(view.container)?.textContent).toBe("settled output");
});

test("an ESC intermediate before the raw boundary makes a retained bracket a final byte", () => {
  const Body = toolRendererFor("shell").body!;
  const output = `${"p".repeat(50)}\u001b(${"["}${"V".repeat(7_999)}`;
  const { container } = render(<Body item={withCommand("long-run", { output })} live={false} />);

  expect(outputCode(container)?.textContent).toBe(
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

  expect(outputCode(container)?.textContent).toBe("earlier output not retained — showing the last 8,000 chars\nNEW");
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
  expect(outputCode(view.container)?.textContent).toBe(`${"x".repeat(7_981)}after`);
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
  expect(outputCode(view.container)?.textContent).toBe("");

  output += "\u0007done";
  view.rerender(<Body item={withCommand("long-run", { output })} live />);
  expect(outputCode(view.container)?.textContent).toBe("done");
});

test("body renders a command block for a command with no output", () => {
  const d = toolRendererFor("shell");
  const Body = d.body!;
  const { container } = render(<Body item={withCommand("true", { output: "" })} live={false} />);
  const codes = container.querySelectorAll("code");
  expect(codes).toHaveLength(1);
  expect(codes[0]?.textContent).toBe("true");
});

test("body renders nothing with no command and no output", () => {
  const Body = toolRendererFor("shell").body!;
  const { container } = render(<Body item={item({ toolName: "shell", output: "" })} live={false} />);
  expect(container.textContent).toBe("");
});

// The pretty-printed command is the PRIMARY content of an expanded shell
// row, so the command block never tail-folds: every formatted line renders,
// with no "Show N earlier lines" control (CodeBlock's fold={false}).
test("body renders a LONG command in full - the command block never folds", () => {
  const Body = toolRendererFor("shell").body!;
  const segments = Array.from({ length: 20 }, (_, i) => `echo step-${i + 1}`);
  const { container } = render(<Body item={withCommand(segments.join(" && "), { output: "" })} live={false} />);
  expect(container.querySelector("code")?.textContent).toContain("echo step-1");
  expect(container.querySelector("code")?.textContent).toContain("echo step-20");
  expect(screen.queryByRole("button", { name: /earlier lines/ })).toBeNull();
});

// detail() carries the exit code ONLY - never the command as well: the
// expanded body already shows the command pretty-printed, so a second copy
// in detail() would duplicate the call on an open row.
test("detail does NOT repeat the command", () => {
  const d = toolRendererFor("shell");
  const longCmd = "x".repeat(100);
  expect(d.summary(withCommand(longCmd))).toBe(`Ran ${"x".repeat(100)}`);
  expect(d.detail?.(withCommand(longCmd, { exitCode: 0 }))).toBe("exit 0");
});

describe("stripRedundantCd", () => {
  const cwd = "/Users/jesse/work";
  test("strips the exact cd-cwd prefix", () => {
    expect(stripRedundantCd("cd /Users/jesse/work && make test", cwd)).toBe("make test");
  });
  test("different directory is untouched", () => {
    expect(stripRedundantCd("cd /elsewhere && make test", cwd)).toBe("cd /elsewhere && make test");
  });
  test("quoted cwd is untouched (literal match only)", () => {
    expect(stripRedundantCd('cd "/Users/jesse/work" && make', cwd)).toBe('cd "/Users/jesse/work" && make');
  });
  test("trailing slash variant is untouched", () => {
    expect(stripRedundantCd("cd /Users/jesse/work/ && make", cwd)).toBe("cd /Users/jesse/work/ && make");
  });
  test("semicolon join is untouched", () => {
    expect(stripRedundantCd("cd /Users/jesse/work ; make", cwd)).toBe("cd /Users/jesse/work ; make");
  });
  test("cd mid-command is untouched", () => {
    expect(stripRedundantCd("make && cd /Users/jesse/work && ls", cwd)).toBe("make && cd /Users/jesse/work && ls");
  });
  test("undefined or empty cwd never strips", () => {
    expect(stripRedundantCd("cd /Users/jesse/work && make", undefined)).toBe("cd /Users/jesse/work && make");
    expect(stripRedundantCd("cd /Users/jesse/work && make", "")).toBe("cd /Users/jesse/work && make");
  });
  test("prefix-only command (nothing after &&) is untouched", () => {
    expect(stripRedundantCd("cd /Users/jesse/work && ", cwd)).toBe("cd /Users/jesse/work && ");
  });
});
