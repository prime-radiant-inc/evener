// @vitest-environment node
import { describe, expect, test } from "vitest";
import { findBuiltinArgument, matchBuiltinInvocation } from "./builtinInvocation";

const builtins = [
  { id: "compact" },
  { id: "goal", args: { kind: "free" } },
  { id: "model", args: { kind: "enum" } },
  { id: "plugin:review", args: { kind: "free" } },
] as const;

describe("matchBuiltinInvocation", () => {
  test("a bare argless built-in matches with an empty argsText", () => {
    expect(matchBuiltinInvocation("/compact", builtins)).toEqual({ command: builtins[0], argsText: "" });
    expect(matchBuiltinInvocation("/compact  \t", builtins)).toEqual({ command: builtins[0], argsText: "" });
  });

  test("an argless built-in followed by text is not a match, so the draft is sent as a message", () => {
    expect(matchBuiltinInvocation("/compact right now", builtins)).toBeNull();
  });

  test("a built-in that takes arguments captures everything after the first space or tab", () => {
    expect(matchBuiltinInvocation("/goal fix the login bug", builtins)).toEqual({
      command: builtins[1],
      argsText: "fix the login bug",
    });
    expect(matchBuiltinInvocation("/model\tanthropic/claude-x", builtins)?.argsText).toBe("anthropic/claude-x");
  });

  test("a built-in that takes arguments still matches with nothing after the name", () => {
    expect(matchBuiltinInvocation("/goal", builtins)).toEqual({ command: builtins[1], argsText: "" });
  });

  test("argument text keeps its newlines and inner spacing", () => {
    expect(matchBuiltinInvocation("/goal line one\n  line two\n", builtins)?.argsText).toBe("line one\n  line two\n");
  });

  test("only a space or tab separates the name from its arguments", () => {
    expect(matchBuiltinInvocation("/goal\nline", builtins)).toBeNull();
  });

  test("the returned command is the caller's own object, found by exact id", () => {
    const match = matchBuiltinInvocation("/plugin:review this", builtins);
    expect(match?.command).toBe(builtins[3]);
    expect(matchBuiltinInvocation("/Goal x", builtins)).toBeNull();
  });

  test("an unknown name, a missing slash, a slash with nothing after it or text before the slash is not a match", () => {
    expect(matchBuiltinInvocation("/frobnicate", builtins)).toBeNull();
    expect(matchBuiltinInvocation("goal fix it", builtins)).toBeNull();
    expect(matchBuiltinInvocation("/", builtins)).toBeNull();
    expect(matchBuiltinInvocation("/ goal", builtins)).toBeNull();
    expect(matchBuiltinInvocation("hello /goal", builtins)).toBeNull();
    expect(matchBuiltinInvocation("", builtins)).toBeNull();
  });
});

describe("findBuiltinArgument", () => {
  const items = [
    { id: "anthropic/claude-x", label: "Claude X" },
    { id: "openai/gpt-y", label: "GPT Y" },
  ];

  test("resolves by id or by label, ignoring case and surrounding whitespace", () => {
    expect(findBuiltinArgument(items, "anthropic/claude-x")).toBe(items[0]);
    expect(findBuiltinArgument(items, "  ANTHROPIC/Claude-X\n")).toBe(items[0]);
    expect(findBuiltinArgument(items, "gpt y")).toBe(items[1]);
  });

  test("a partial name, an unknown value or empty text resolves to nothing", () => {
    expect(findBuiltinArgument(items, "claude")).toBeUndefined();
    expect(findBuiltinArgument(items, "mistral/large")).toBeUndefined();
    expect(findBuiltinArgument(items, "   ")).toBeUndefined();
    expect(findBuiltinArgument([], "anything")).toBeUndefined();
  });
});
