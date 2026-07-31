// @vitest-environment node

import { expect, test } from "vitest";
import type { ItemModel } from "../../../protocol/model";
import {
  isDormantTranscript,
  isItemVisible,
  SYSTEM_PRELUDE_TURN_ID,
  SYSTEM_PROMPT_ITEM_ID,
  type TranscriptVisibilityPrefs,
  visibleItems,
} from "./transcriptVisibility";

const ALL_OFF: TranscriptVisibilityPrefs = {
  roundTimings: false,
  hookExitsAll: false,
  hookExitsNormal: false,
  promptLoaded: false,
};

function prefs(overrides: Partial<TranscriptVisibilityPrefs> = {}): TranscriptVisibilityPrefs {
  return { ...ALL_OFF, ...overrides };
}

function item(overrides: Partial<ItemModel> = {}): ItemModel {
  return { id: "item_1", turnId: "turn_1", type: "systemMessage", text: "", ...overrides };
}

function hook(exitCode: number | undefined): ItemModel {
  return item({ id: `hook_${exitCode}`, eventKind: "hook_completed", text: "PreToolUse hook exit 0", exitCode });
}

// --- items no toggle governs ------------------------------------------------

test("a non-system item is always visible, whatever the prefs", () => {
  expect(isItemVisible(item({ type: "agentMessage", eventKind: undefined }), prefs())).toBe(true);
  expect(isItemVisible(item({ type: "commandExecution", exitCode: 1 }), prefs())).toBe(true);
});

test("a tool call carrying a nonzero exitCode is NOT mistaken for a hook exit", () => {
  // ItemModel.exitCode is a shell tool call's exit code too - classification
  // is by eventKind, never by the mere presence of an exit code.
  const shell = item({ type: "commandExecution", toolName: "shell", exitCode: 1, eventKind: undefined });
  expect(isItemVisible(shell, prefs())).toBe(true);
});

test("a systemMessage kind no toggle governs stays visible with every toggle off", () => {
  for (const eventKind of ["skill_activated", "plugin_loaded", "compaction", "turn_limit", "goal_ended"]) {
    expect(isItemVisible(item({ eventKind }), prefs())).toBe(true);
  }
});

test("a systemMessage whose text merely mentions a hook exit is NOT classified as one", () => {
  // The legacy renderer sniffed /\bhook\b.*\bexit\s+(-?\d+)\b/i out of the
  // message text; classification here is by the typed eventKind only.
  const lookalike = item({ eventKind: "skill_activated", text: "the hook ran and reported exit 1" });
  expect(isItemVisible(lookalike, prefs())).toBe(true);
});

// --- hook exits -------------------------------------------------------------

test("with both hook toggles off, no hook exit item is visible", () => {
  expect(isItemVisible(hook(0), prefs())).toBe(false);
  expect(isItemVisible(hook(1), prefs())).toBe(false);
  expect(isItemVisible(hook(-1), prefs())).toBe(false);
  expect(isItemVisible(hook(undefined), prefs())).toBe(false);
});

test("hookExitsAll shows every hook exit, whatever the code", () => {
  const on = prefs({ hookExitsAll: true });
  expect(isItemVisible(hook(0), on)).toBe(true);
  expect(isItemVisible(hook(1), on)).toBe(true);
  expect(isItemVisible(hook(-1), on)).toBe(true);
});

test("hookExitsNormal shows exit 0 only, and hides every nonzero exit", () => {
  const on = prefs({ hookExitsNormal: true });
  expect(isItemVisible(hook(0), on)).toBe(true);
  expect(isItemVisible(hook(1), on)).toBe(false);
  expect(isItemVisible(hook(-1), on)).toBe(false);
});

test("hookExitsAll subsumes hookExitsNormal: both on behaves exactly like all-on", () => {
  const both = prefs({ hookExitsAll: true, hookExitsNormal: true });
  expect(isItemVisible(hook(0), both)).toBe(true);
  expect(isItemVisible(hook(1), both)).toBe(true);
  expect(isItemVisible(hook(-1), both)).toBe(true);
});

test("an unknown exit code is never treated as a clean zero: only hookExitsAll shows it", () => {
  // A daemon predating the projector's typed ExitCode leaves it undefined.
  // undefined is "unknown", NOT 0 - the wire's own ExitCode contract is
  // "never fabricated as zero", so normal-only must not claim it passed.
  expect(isItemVisible(hook(undefined), prefs({ hookExitsNormal: true }))).toBe(false);
  expect(isItemVisible(hook(undefined), prefs({ hookExitsAll: true }))).toBe(true);
});

// --- prompt loaded ----------------------------------------------------------

test("promptLoaded off hides both the system prompt scaffold and the prompt-loaded line", () => {
  expect(isItemVisible(item({ eventKind: "system_prompt", text: "You are..." }), prefs())).toBe(false);
  expect(isItemVisible(item({ eventKind: "prompt_loaded", text: "Loaded prompt x (12 B)" }), prefs())).toBe(false);
});

test("promptLoaded on shows both", () => {
  const on = prefs({ promptLoaded: true });
  expect(isItemVisible(item({ eventKind: "system_prompt" }), on)).toBe(true);
  expect(isItemVisible(item({ eventKind: "prompt_loaded" }), on)).toBe(true);
});

test("an older daemon's system prompt is gated by its stable item id when eventKind is absent", () => {
  const legacyPrompt = item({ id: SYSTEM_PROMPT_ITEM_ID, eventKind: undefined, text: "You are..." });
  expect(isItemVisible(legacyPrompt, prefs())).toBe(false);
  expect(isItemVisible(legacyPrompt, prefs({ promptLoaded: true }))).toBe(true);
});

test("the hook toggles do not govern prompt items, and promptLoaded does not govern hook items", () => {
  expect(isItemVisible(item({ eventKind: "system_prompt" }), prefs({ hookExitsAll: true }))).toBe(false);
  expect(isItemVisible(hook(0), prefs({ promptLoaded: true }))).toBe(false);
});

// --- visibleItems -----------------------------------------------------------

test("visibleItems drops exactly the hidden items and preserves order", () => {
  const items = [
    item({ id: "a", type: "userMessage" }),
    hook(1),
    item({ id: "c", eventKind: "skill_activated" }),
    item({ id: "d", eventKind: "system_prompt" }),
  ];
  expect(visibleItems(items, prefs()).map((i) => i.id)).toEqual(["a", "c"]);
});

test("visibleItems returns the SAME array reference when nothing is hidden", () => {
  // TurnBlock reuses the turn object when this is identity-stable, so the
  // memoized item renderers keep bailing out on unchanged turns.
  const items = [item({ id: "a", type: "userMessage" }), item({ id: "b", eventKind: "skill_activated" })];
  expect(visibleItems(items, prefs())).toBe(items);
});

test("visibleItems preserves each surviving item's own object identity", () => {
  const keep = item({ id: "a", type: "userMessage" });
  const result = visibleItems([keep, hook(1)], prefs());
  expect(result[0]).toBe(keep);
});

// --- round timings ----------------------------------------------------------
// "Round timings" governs TWO surfaces that report the same measurement:
// TurnSeparator's friendly per-turn annotation, and this raw system item -
// "Round 2 total=1.5s llm=1.2s context=25ms ..." - which the projector emits
// as its own line. Only the first was gated, so turning the setting OFF left
// the rawer of the two on screen: the opposite of what it says it does.

test("a round_timings system item is hidden when Round timings is off", () => {
  const timings = item({ id: "rt", eventKind: "round_timings", text: "Round 2 total=1.5s llm=1.2s context=25ms" });
  expect(isItemVisible(timings, prefs())).toBe(false);
});

test("a round_timings system item shows when Round timings is on", () => {
  const timings = item({ id: "rt", eventKind: "round_timings", text: "Round 2 total=1.5s llm=1.2s context=25ms" });
  expect(isItemVisible(timings, prefs({ roundTimings: true }))).toBe(true);
});

test("Round timings does not govern the other toggles' items, or theirs it", () => {
  const timings = item({ id: "rt", eventKind: "round_timings" });
  expect(isItemVisible(timings, prefs({ hookExitsAll: true, promptLoaded: true }))).toBe(false);
  expect(isItemVisible(hook(0), prefs({ roundTimings: true }))).toBe(false);
  expect(isItemVisible(item({ eventKind: "system_prompt" }), prefs({ roundTimings: true }))).toBe(false);
});

// --- isDormantTranscript (kata bz2z / cmjb) ---------------------------------

test("zero turns is dormant", () => {
  expect(isDormantTranscript([])).toBe(true);
});

test("a single prelude turn (the synthetic system-prompt turn) is dormant", () => {
  expect(isDormantTranscript([{ id: SYSTEM_PRELUDE_TURN_ID }])).toBe(true);
});

test("a single real turn (not the prelude id) is NOT dormant", () => {
  expect(isDormantTranscript([{ id: "turn_1" }])).toBe(false);
});

test("the prelude turn alongside a real turn is NOT dormant - a real conversation exists", () => {
  expect(isDormantTranscript([{ id: SYSTEM_PRELUDE_TURN_ID }, { id: "turn_1" }])).toBe(false);
});

test("multiple real turns with no prelude at all is NOT dormant", () => {
  expect(isDormantTranscript([{ id: "turn_1" }, { id: "turn_2" }])).toBe(false);
});
