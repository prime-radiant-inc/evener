import { expect, it } from "vitest";
import { composerCommand } from "./composerCommand";

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
