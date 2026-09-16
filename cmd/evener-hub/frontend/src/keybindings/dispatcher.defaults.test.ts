// The default binding map driven through the real dispatcher: each default
// chord runs its own action, and the per-binding editable/modal/scope policy
// and the strict-vs-legacy modifier rules hold at dispatch time, not only in
// the map. The map itself is tested in the package
// (appwire-client/typescript/keybindingDefaults.test.ts).
import {
  ACTIONS,
  CHEATSHEET_SCOPE,
  createKeybindingsRegistry,
  type KeybindingsRegistry,
  registerDefaultBindings,
  SETTINGS_SCOPE,
} from "@evener/appwire-client";
import { parseKeybinding } from "tinykeys";
import { afterEach, describe, expect, test } from "vitest";
import { createKeybindingDispatcher, type KeybindingDispatcher } from "./dispatcher";

function keydown(init: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
}

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
    registry = createKeybindingsRegistry(parseKeybinding);
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
    window.dispatchEvent(keydown({ key: ",", code: "Comma", ctrlKey: true }));
    window.dispatchEvent(keydown({ key: "ArrowRight", code: "ArrowRight", altKey: true }));
    window.dispatchEvent(keydown({ key: "ArrowLeft", code: "ArrowLeft", altKey: true }));
    window.dispatchEvent(keydown({ key: "ArrowUp", code: "ArrowUp", altKey: true }));
    window.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true }));
    window.dispatchEvent(keydown({ key: "ArrowUp", code: "ArrowUp", altKey: true, shiftKey: true }));
    window.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true, shiftKey: true }));
    window.dispatchEvent(keydown({ key: "Home", code: "Home", altKey: true }));
    window.dispatchEvent(keydown({ key: "End", code: "End", altKey: true }));
    expect(calls).toEqual([
      ACTIONS.paletteOpen,
      ACTIONS.railToggle,
      ACTIONS.composerFocus,
      ACTIONS.nextNeedsYou,
      ACTIONS.selectionQuote,
      ACTIONS.settingsOpen,
      ACTIONS.sessionNext,
      ACTIONS.sessionPrevious,
      ACTIONS.transcriptLineUp,
      ACTIONS.transcriptLineDown,
      ACTIONS.transcriptPageUp,
      ACTIONS.transcriptPageDown,
      ACTIONS.transcriptScrollTop,
      ACTIONS.transcriptScrollBottom,
    ]);
  });

  test("the Alt+Arrow and Alt+Home/End chords are suppressed in an editable target", () => {
    setup();
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.dispatchEvent(keydown({ key: "ArrowRight", code: "ArrowRight", altKey: true }));
    input.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true }));
    input.dispatchEvent(keydown({ key: "ArrowDown", code: "ArrowDown", altKey: true, shiftKey: true }));
    input.dispatchEvent(keydown({ key: "Home", code: "Home", altKey: true }));
    input.dispatchEvent(keydown({ key: "End", code: "End", altKey: true }));
    expect(calls).toEqual([]);
  });

  test("⌘, fires settings.open from an editable target (it never collides with typing)", () => {
    setup();
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.dispatchEvent(keydown({ key: ",", code: "Comma", ctrlKey: true }));
    expect(calls).toEqual([ACTIONS.settingsOpen]);
  });

  test("Escape only closes settings while the settings scope is pushed", () => {
    setup();
    window.dispatchEvent(keydown({ key: "Escape", code: "Escape" }));
    expect(calls).toEqual([]);
    registry.getState().pushScope(SETTINGS_SCOPE);
    window.dispatchEvent(keydown({ key: "Escape", code: "Escape" }));
    expect(calls).toEqual([ACTIONS.settingsClose]);
  });

  test('the "?" trigger fires with or without Shift held, and is suppressed in an editable target', () => {
    setup();
    window.dispatchEvent(keydown({ key: "?", code: "Slash", shiftKey: true }));
    window.dispatchEvent(keydown({ key: "?", code: "Slash" }));
    expect(calls).toEqual([ACTIONS.cheatsheetToggle, ACTIONS.cheatsheetToggle]);
    calls = [];
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.dispatchEvent(keydown({ key: "?", code: "Slash", shiftKey: true }));
    expect(calls).toEqual([]);
  });

  test("Escape only closes the cheatsheet while the cheatsheet scope is pushed", () => {
    setup();
    window.dispatchEvent(keydown({ key: "Escape", code: "Escape" }));
    expect(calls).toEqual([]);
    registry.getState().pushScope(CHEATSHEET_SCOPE);
    window.dispatchEvent(keydown({ key: "Escape", code: "Escape" }));
    expect(calls).toEqual([ACTIONS.cheatsheetClose]);
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

  // STRICT extra-modifier contract (Phase 4a): the 2a map listed Shift/Alt
  // as OPTIONAL on palette.open/composer.focus/next-needs-you because the
  // legacy AppShell ⌘K/⌘I/⌘J listener checked only metaKey||ctrlKey + key
  // with NO shift/alt guard - which hijacked the browser's DevTools chords
  // (⌘⌥I, Ctrl+Shift+J). docs/superpowers/plans/2026-09-04-webui-keybindings-p4-plan.md
  // (Design decision 1) is the authority for the deliberate change: these
  // extra-modifier presses must NOT fire, so the browser gets its chords
  // back. legacyEitherMod is retained - Meta+Ctrl together still fires
  // (pinned below).
  test("⌘⇧K and ⌘⌥K do NOT fire palette.open (strict per the p4 plan)", () => {
    setup();
    window.dispatchEvent(keydown({ key: "k", code: "KeyK", metaKey: true, shiftKey: true }));
    window.dispatchEvent(keydown({ key: "k", code: "KeyK", metaKey: true, altKey: true }));
    expect(calls).toEqual([]);
  });

  test("Meta+Ctrl+K fires palette.open (legacyEitherMod kept: either or both modifiers)", () => {
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

  test("⌘⌥I does NOT fire composer.focus (the browser's DevTools chord reverts to the browser)", () => {
    setup();
    window.dispatchEvent(keydown({ key: "i", code: "KeyI", metaKey: true, altKey: true }));
    window.dispatchEvent(keydown({ key: "i", code: "KeyI", metaKey: true, shiftKey: true }));
    expect(calls).toEqual([]);
  });

  test("Ctrl+Shift+J and ⌘⇧J do NOT fire next-needs-you (DevTools/downloads chords revert to the browser)", () => {
    setup();
    window.dispatchEvent(keydown({ key: "j", code: "KeyJ", ctrlKey: true, shiftKey: true }));
    window.dispatchEvent(keydown({ key: "j", code: "KeyJ", metaKey: true, shiftKey: true }));
    expect(calls).toEqual([]);
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
    window.dispatchEvent(keydown({ key: ",", code: "Comma", metaKey: true }));
    expect(calls).toEqual([
      ACTIONS.paletteOpen,
      ACTIONS.railToggle,
      ACTIONS.composerFocus,
      ACTIONS.nextNeedsYou,
      ACTIONS.selectionQuote,
      ACTIONS.settingsOpen,
    ]);
  });
});
