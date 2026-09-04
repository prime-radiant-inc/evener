import { afterEach, describe, expect, test } from "vitest";
import { ACTIONS } from "./actions";
import { formatSequence, parseChord, serializeChord } from "./chord";
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
  // The optional modifiers are the legacy-faithful semantics: the AppShell
  // K/I/J listener had no shift/alt guard, SelectionQuote guarded only
  // AltGr (alt), rail.toggle guarded both, and the Settings Escape listener
  // had no modifier guard at all.
  test.each([
    [ACTIONS.paletteOpen, "$mod+[Shift]+[Alt]+K"],
    [ACTIONS.railToggle, "$mod+B"],
    [ACTIONS.composerFocus, "$mod+[Shift]+[Alt]+I"],
    [ACTIONS.nextNeedsYou, "$mod+[Shift]+[Alt]+J"],
    [ACTIONS.selectionQuote, "$mod+[Shift]+'"],
    [ACTIONS.settingsClose, "[Control]+[Alt]+[Shift]+[Meta]+Escape"],
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

  test("display formatting renders every default chord (optional modifiers are dropped from display - parked L22 minor, but it must not crash)", () => {
    for (const binding of DEFAULT_BINDINGS) {
      const rendered = formatSequence(parseChord(String(binding.chord)));
      expect(rendered.length).toBeGreaterThan(0);
    }
  });

  test("only rail.toggle opts out of the defaultPrevented gate (RailHost.tsx:59-66 has no such check)", () => {
    for (const binding of DEFAULT_BINDINGS) {
      const expected = binding.actionId !== ACTIONS.railToggle;
      expect(binding.ignoreIfDefaultPrevented ?? true).toBe(expected);
    }
  });

  test("modal policy matches today's per-site modal guards", () => {
    // palette.open (AppShell's deliberate ⌘K exemption), rail.toggle and
    // selection.quote (their listeners have no modal check at all) are exempt;
    // composer.focus / next-needs-you (AppShell's blockedByOpenModal) and
    // settings.close keep the default suppression.
    const policy = new Map(DEFAULT_BINDINGS.map((b) => [b.actionId, b.allowInModal ?? false]));
    expect(policy.get(ACTIONS.paletteOpen)).toBe(true);
    expect(policy.get(ACTIONS.railToggle)).toBe(true);
    expect(policy.get(ACTIONS.selectionQuote)).toBe(true);
    expect(policy.get(ACTIONS.composerFocus)).toBe(false);
    expect(policy.get(ACTIONS.nextNeedsYou)).toBe(false);
    expect(policy.get(ACTIONS.settingsClose)).toBe(false);
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

  // Extra-modifier variants: the legacy AppShell ⌘K/⌘I/⌘J listener checked
  // only metaKey||ctrlKey + key (NO shift/alt guard), and the legacy
  // Settings Escape listener checked only key === "Escape" - so these all
  // fired legacy and must still fire.
  test("⌘⇧K and ⌘⌥K fire palette.open (legacy had no shift/alt guard)", () => {
    setup();
    window.dispatchEvent(keydown({ key: "k", code: "KeyK", metaKey: true, shiftKey: true }));
    window.dispatchEvent(keydown({ key: "k", code: "KeyK", metaKey: true, altKey: true }));
    expect(calls).toEqual([ACTIONS.paletteOpen, ACTIONS.paletteOpen]);
  });

  test("Meta+Ctrl+K fires palette.open (legacy accepted either or both modifiers)", () => {
    setup();
    window.dispatchEvent(keydown({ key: "k", code: "KeyK", metaKey: true, ctrlKey: true }));
    expect(calls).toEqual([ACTIONS.paletteOpen]);
  });

  test("Meta+Ctrl+B fires rail.toggle (legacy accepted either or both; only Alt/Shift stay strict)", () => {
    setup();
    window.dispatchEvent(keydown({ key: "b", code: "KeyB", metaKey: true, ctrlKey: true }));
    expect(calls).toEqual([ACTIONS.railToggle]);
  });

  test("Meta+Ctrl+' fires selection.quote (legacy accepted either or both; only Alt stays strict)", () => {
    setup();
    window.dispatchEvent(keydown({ key: "'", code: "Quote", metaKey: true, ctrlKey: true }));
    expect(calls).toEqual([ACTIONS.selectionQuote]);
  });

  test("⌘⇧I fires composer.focus", () => {
    setup();
    window.dispatchEvent(keydown({ key: "i", code: "KeyI", metaKey: true, shiftKey: true }));
    expect(calls).toEqual([ACTIONS.composerFocus]);
  });

  test("Ctrl+Shift+J fires next-needs-you", () => {
    setup();
    window.dispatchEvent(keydown({ key: "j", code: "KeyJ", ctrlKey: true, shiftKey: true }));
    expect(calls).toEqual([ACTIONS.nextNeedsYou]);
  });

  test("Shift+Escape closes settings while the settings scope is pushed (legacy had no modifier guard)", () => {
    setup();
    registry.getState().pushScope(SETTINGS_SCOPE);
    window.dispatchEvent(keydown({ key: "Escape", code: "Escape", shiftKey: true }));
    expect(calls).toEqual([ACTIONS.settingsClose]);
  });

  test("⌘⇧' fires selection.quote but ⌘⌥' does NOT (the legacy AltGr guard)", () => {
    setup();
    window.dispatchEvent(keydown({ key: "'", code: "Quote", metaKey: true, shiftKey: true }));
    expect(calls).toEqual([ACTIONS.selectionQuote]);
    window.dispatchEvent(keydown({ key: "'", code: "Quote", metaKey: true, altKey: true }));
    expect(calls).toEqual([ACTIONS.selectionQuote]);
  });

  test("⌘⇧B and ⌘⌥B do NOT fire rail.toggle (the legacy listener guarded both)", () => {
    setup();
    window.dispatchEvent(keydown({ key: "b", code: "KeyB", metaKey: true, shiftKey: true }));
    window.dispatchEvent(keydown({ key: "b", code: "KeyB", metaKey: true, altKey: true }));
    expect(calls).toEqual([]);
  });

  test("every $mod chord also fires under Meta (today's listeners accept metaKey||ctrlKey)", () => {
    // jsdom resolves tinykeys' $mod to Control, but the shell's pre-dispatcher
    // listeners (and Mac users hitting Ctrl+K) accept either modifier on every
    // platform, so registerDefaultBindings registers the cross-platform twin
    // of each $mod chord as well.
    setup();
    window.dispatchEvent(keydown({ key: "k", code: "KeyK", metaKey: true }));
    window.dispatchEvent(keydown({ key: "b", code: "KeyB", metaKey: true }));
    window.dispatchEvent(keydown({ key: "i", code: "KeyI", metaKey: true }));
    window.dispatchEvent(keydown({ key: "j", code: "KeyJ", metaKey: true }));
    window.dispatchEvent(keydown({ key: "'", code: "Quote", metaKey: true }));
    expect(calls).toEqual([
      ACTIONS.paletteOpen,
      ACTIONS.railToggle,
      ACTIONS.composerFocus,
      ACTIONS.nextNeedsYou,
      ACTIONS.selectionQuote,
    ]);
  });
});
