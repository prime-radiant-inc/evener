// @vitest-environment node

import { expect, test } from "vitest";
import { echoesTurnError, isErrorEvent, systemEventWords } from "./systemEventCopy";
import {
  type SystemEventWireCase,
  systemEventWireFailedTurn,
  systemEventWireFailedTurnWithOtherError,
  systemEventWireItem,
} from "./testing/systemEventWireFixtures";
import type { ThreadItem } from "./types.gen";

// Every case reads what the daemon actually sends
// (agent/testdata/systemeventwire), as the clients' item model carries it.
function modelOf(item: ThreadItem) {
  return { type: item.type, eventKind: item.eventKind, text: item.text ?? "", raw: item.raw };
}

const words = (name: SystemEventWireCase) => systemEventWords(modelOf(systemEventWireItem(name)));

test("names a loaded plugin, or says a plugin loaded, with no counts", () => {
  expect(words("plugin-loaded")).toEqual({ text: "Plugin superpowers loaded" });
  expect(words("plugin-loaded-unnamed")).toEqual({ text: "Plugin loaded" });
});

test("says how far a compaction brought the context: tokens, else turns, else just that it ran", () => {
  expect(words("context-compaction")).toEqual({ text: "Context compacted · 412K → 38K tokens" });
  expect(words("context-compaction-turns")).toEqual({ text: "Context compacted · 40 → 5 turns" });
  expect(words("context-compaction-bare")).toEqual({ text: "Context compacted" });
});

// A daemon that sends no structured raw still said something in its text.
test("keeps a plugin's or a compaction's own text when raw carries no structure", () => {
  expect(systemEventWords({ eventKind: "plugin_loaded", text: "Loaded plugin tools", raw: undefined })).toEqual({
    text: "Loaded plugin tools",
  });
  expect(systemEventWords({ eventKind: "plugin_loaded", text: "", raw: { other: 1 } })).toEqual({
    text: "Plugin loaded",
  });
  expect(
    systemEventWords({ eventKind: "context_compaction", text: "Layer: summary\nTurns: 40 -> 5", raw: undefined }),
  ).toEqual({ text: "Layer: summary\nTurns: 40 -> 5" });
});

test.each(["compaction-summary", "compaction-checkpoint"] as const)(
  "folds a %s under Context summary, opening to its markdown",
  (name) => {
    expect(words(name)).toEqual({
      text: systemEventWireItem(name).text,
      label: "Context summary",
      rendersMarkdown: true,
    });
  },
);

test("leaves every other event's text as it is", () => {
  expect(words("tool-repair")).toEqual({ text: systemEventWireItem("tool-repair").text });
});

test("knows an error event, and the one that echoes its turn's failure", () => {
  const failed = systemEventWireFailedTurn();
  const echo = (failed.items ?? []).map(modelOf).find(isErrorEvent);
  if (!echo || !failed.error) throw new Error("the failed turn carries no error pair");
  expect(echoesTurnError(echo, failed.error)).toBe(true);

  const withOther = systemEventWireFailedTurnWithOtherError();
  const turnError = withOther.error;
  if (!turnError) throw new Error("the failed turn carries no error");
  const errors = (withOther.items ?? []).map(modelOf).filter(isErrorEvent);
  expect(errors.map((item) => echoesTurnError(item, turnError))).toEqual([false, true]);
  expect(echoesTurnError(echo, undefined)).toBe(false);
  expect(isErrorEvent(modelOf(systemEventWireItem("tool-repair")))).toBe(false);
});

// apptranscript writes "The turn failed." as the failure item's text when the
// failure carries no message, so a turn.error with none is still echoed.
test("knows the echo of a failure that carries no message", () => {
  const fallback = { type: "systemMessage", eventKind: "error", text: "The turn failed." };
  expect(echoesTurnError(fallback, { message: "" })).toBe(true);
  expect(echoesTurnError(fallback, { message: null })).toBe(true);
  expect(echoesTurnError(fallback, {})).toBe(true);
  expect(echoesTurnError({ ...fallback, text: "Another error" }, { message: "" })).toBe(false);
});
