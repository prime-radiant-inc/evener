// @vitest-environment node

import { expect, test } from "vitest";
import {
  CONTEXT_SUMMARY_LABEL,
  contextCompactedText,
  ERROR_EVENT_KIND,
  echoesTurnError,
  pluginLoadedText,
} from "./systemEventCopy";
import {
  systemEventWireFailedTurn,
  systemEventWireFailedTurnWithOtherError,
  systemEventWireItem,
} from "./testing/systemEventWireFixtures";

// Every case reads what the daemon actually sends
// (agent/testdata/systemeventwire).

test("names a loaded plugin, or says a plugin loaded, with no counts", () => {
  expect(pluginLoadedText(systemEventWireItem("plugin-loaded").raw)).toBe("Plugin superpowers loaded");
  expect(pluginLoadedText(systemEventWireItem("plugin-loaded-unnamed").raw)).toBe("Plugin loaded");
  expect(pluginLoadedText(undefined)).toBe("Plugin loaded");
});

test("says how far a compaction brought the context: tokens, else turns, else just that it ran", () => {
  expect(contextCompactedText(systemEventWireItem("context-compaction").raw)).toBe(
    "Context compacted · 412K → 38K tokens",
  );
  expect(contextCompactedText(systemEventWireItem("context-compaction-turns").raw)).toBe(
    "Context compacted · 40 → 5 turns",
  );
  expect(contextCompactedText(systemEventWireItem("context-compaction-bare").raw)).toBe("Context compacted");
});

test("folds a compaction's summary under one label", () => {
  expect(CONTEXT_SUMMARY_LABEL).toBe("Context summary");
});

test("knows the error that echoes its turn's failure, and no other", () => {
  const failed = systemEventWireFailedTurn();
  const echo = (failed.items ?? []).find((item) => item.eventKind === ERROR_EVENT_KIND);
  if (!echo || !failed.error) throw new Error("the failed turn carries no error pair");
  expect(echoesTurnError(echo, failed.error)).toBe(true);

  const withOther = systemEventWireFailedTurnWithOtherError();
  const errors = (withOther.items ?? []).filter((item) => item.eventKind === ERROR_EVENT_KIND);
  if (!withOther.error) throw new Error("the failed turn carries no error");
  const turnError = withOther.error;
  expect(errors.map((item) => echoesTurnError(item, turnError))).toEqual([false, true]);
  expect(echoesTurnError(echo, undefined)).toBe(false);
});
