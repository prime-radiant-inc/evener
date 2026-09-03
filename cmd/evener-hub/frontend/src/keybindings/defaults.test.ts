import { afterEach, describe, expect, test } from "vitest";
import { ACTIONS } from "./actions";
import { parseChord, serializeChord } from "./chord";
import { DEFAULT_BINDINGS, registerDefaultBindings, SETTINGS_SCOPE } from "./defaults";
import { createKeybindingDispatcher, type KeybindingDispatcher } from "./dispatcher";
import { createKeybindingsRegistry, GLOBAL_SCOPE, type KeybindingsRegistry } from "./registry";

function keydown(init: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
}

describe("default binding map", () => {
  test("is exactly the six shell chords", () => {
    expect(DEFAULT_BINDINGS.map((b) => b.actionId)).toEqual([
      ACTIONS.paletteOpen,
      ACTIONS.railToggle,
      ACTIONS.composerFocus,
      ACTIONS.nextNeedsYou,
      ACTIONS.selectionQuote,
      ACTIONS.settingsClose,
    ]);
  });

  // Chords are compared through parse+serialize so the assertion is
  // platform-independent ("$mod" resolves per host platform at parse time).
  test.each([
    [ACTIONS.paletteOpen, "$mod+K"],
    [ACTIONS.railToggle, "$mod+B"],
    [ACTIONS.composerFocus, "$mod+I"],
    [ACTIONS.nextNeedsYou, "$mod+J"],
    [ACTIONS.selectionQuote, "$mod+'"],
    [ACTIONS.settingsClose, "Escape"],
  ])("%s is bound to %s", (actionId, chord) => {
    const binding = DEFAULT_BINDINGS.find((b) => b.actionId === actionId);
    expect(binding).toBeDefined();
    expect(serializeChord(parseChord(String(binding?.chord)))).toBe(serializeChord(parseChord(chord)));
  });

  test("editable-target policy matches today's per-chord behavior", () => {
    const policy = new Map(DEFAULT_BINDINGS.map((b) => [b.actionId, b.allowInEditable ?? false]));
    expect(policy.get(ACTIONS.paletteOpen)).toBe(true);
    expect(policy.get(ACTIONS.composerFocus)).toBe(true);
    expect(policy.get(ACTIONS.nextNeedsYou)).toBe(true);
    expect(policy.get(ACTIONS.selectionQuote)).toBe(true);
    expect(policy.get(ACTIONS.railToggle)).toBe(false);
  });

  test("settings.close is scope-gated, not global", () => {
    const binding = DEFAULT_BINDINGS.find((b) => b.actionId === ACTIONS.settingsClose);
    expect(binding?.scope).toBe(SETTINGS_SCOPE);
    for (const other of DEFAULT_BINDINGS.filter((b) => b.actionId !== ACTIONS.settingsClose)) {
      expect(other.scope ?? GLOBAL_SCOPE).toBe(GLOBAL_SCOPE);
    }
  });
});

describe("default bindings through the dispatcher", () => {
  let registry: KeybindingsRegistry;
  let dispatcher: KeybindingDispatcher;
  let detach: () => void;
  let calls: string[];

  afterEach(() => {
    detach();
    dispatcher.dispose();
    document.body.innerHTML = "";
  });

  function setup() {
    registry = createKeybindingsRegistry();
    calls = [];
    for (const actionId of Object.values(ACTIONS)) {
      registry.getState().registerAction(actionId, () => {
        calls.push(actionId);
      });
    }
    registerDefaultBindings(registry);
    dispatcher = createKeybindingDispatcher({ registry });
    detach = dispatcher.attach(window);
  }

  test("each chord runs its own action", () => {
    setup();
    window.dispatchEvent(keydown({ key: "k", code: "KeyK", ctrlKey: true }));
    window.dispatchEvent(keydown({ key: "b", code: "KeyB", ctrlKey: true }));
    window.dispatchEvent(keydown({ key: "i", code: "KeyI", ctrlKey: true }));
    window.dispatchEvent(keydown({ key: "j", code: "KeyJ", ctrlKey: true }));
    window.dispatchEvent(keydown({ key: "'", code: "Quote", ctrlKey: true }));
    expect(calls).toEqual([
      ACTIONS.paletteOpen,
      ACTIONS.railToggle,
      ACTIONS.composerFocus,
      ACTIONS.nextNeedsYou,
      ACTIONS.selectionQuote,
    ]);
  });

  test("Escape only closes settings while the settings scope is pushed", () => {
    setup();
    window.dispatchEvent(keydown({ key: "Escape", code: "Escape" }));
    expect(calls).toEqual([]);
    registry.getState().pushScope(SETTINGS_SCOPE);
    window.dispatchEvent(keydown({ key: "Escape", code: "Escape" }));
    expect(calls).toEqual([ACTIONS.settingsClose]);
  });

  test("Mod+B is suppressed in an editable target while Mod+K still fires", () => {
    setup();
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.dispatchEvent(keydown({ key: "b", code: "KeyB", ctrlKey: true }));
    expect(calls).toEqual([]);
    input.dispatchEvent(keydown({ key: "k", code: "KeyK", ctrlKey: true }));
    expect(calls).toEqual([ACTIONS.paletteOpen]);
  });

  test("Mod+chords with an extra Shift do not match today's plain-chord bindings", () => {
    setup();
    window.dispatchEvent(keydown({ key: "k", code: "KeyK", ctrlKey: true, shiftKey: true }));
    expect(calls).toEqual([]);
  });
});
