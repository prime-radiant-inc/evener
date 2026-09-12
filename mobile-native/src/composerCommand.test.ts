import { expect, it } from "vitest";
import {
  parseSlashToken,
  spliceSlashCommand,
} from "../../cmd/evener-hub/frontend/src/panes/session/composer/slashCompletion";
import { builtinComposerItems, composerCommand } from "./composerCommand";

it.each([
  ["/compact", "compact", ""],
  ["/compact \t", "compact", ""],
  ["/shutdown", "shutdown", ""],
  ["/goal first\nsecond", "goal", "first\nsecond"],
  ["/goal", "goal", ""],
])("recognizes the web invocation %s", (text, id, argsText) => {
  expect(composerCommand(text)).toMatchObject({ command: { id }, argsText });
  expect(composerCommand(text, 1)).toBeNull();
});

it.each([
  "/compact extra",
  "/shutdown extra",
  "/Compact",
  " /compact",
  "/plugin:compact",
  "/unknown",
  "/goal\nobjective",
])("leaves ordinary messages intact: %s", (text) => {
  expect(composerCommand(text)).toBeNull();
});

it("offers capability-backed builtins without inventing unsupported actions", () => {
  const items = builtinComposerItems({ compact: true, goal: true });
  expect(items.map((item) => item.invocation)).toContain("/compact");
  expect(items.map((item) => item.invocation)).toContain("/goal");
  expect(items.map((item) => item.invocation)).toContain("/project");
  expect(items.map((item) => item.invocation)).toContain("/tasks");
  expect(items.map((item) => item.invocation)).not.toContain("/clear");
  expect(items.map((item) => item.invocation)).not.toContain("/interrupt");
  expect(items.map((item) => item.invocation)).not.toContain("/fork");
});

it("recomputes completion availability when session capabilities change", () => {
  expect(
    builtinComposerItems({ clear: true }).some(
      (item) => item.invocation === "/clear",
    ),
  ).toBe(true);
  expect(
    builtinComposerItems({ clear: false }).some(
      (item) => item.invocation === "/clear",
    ),
  ).toBe(false);
});

it("inserts a builtin through the web splice contract without losing surrounding draft text", () => {
  const item = builtinComposerItems({ goal: true }).find(
    (item) => item.invocation === "/goal",
  );
  if (!item) throw new Error("missing goal completion");
  const draft = "/go keep this objective";
  const token = parseSlashToken(draft, 3);
  if (!token) throw new Error("missing slash token");
  const result = spliceSlashCommand(draft, token, item.invocation);
  expect(result.text).toBe("/goal keep this objective");
  expect(composerCommand(result.text)).toMatchObject({
    command: { id: "goal" },
    argsText: "keep this objective",
  });
});
