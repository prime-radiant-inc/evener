// @vitest-environment node

import { parseKeybinding } from "tinykeys";
import { describe, expect, test } from "vitest";
import { ACTIONS } from "./keybindingActions";
import { formatSequence, parseChord, serializeChord } from "./keybindingChord";
import {
  CHARACTER_KEY_TRIGGER_BINDING_ID,
  CHEATSHEET_SCOPE,
  DEFAULT_BINDINGS,
  defaultBindingShapesForAction,
  registerDefaultBindingsForAction,
  SETTINGS_SCOPE,
} from "./keybindingDefaults";
import { createKeybindingsRegistry, GLOBAL_SCOPE } from "./keybindingRegistry";

describe("default binding map", () => {
  test("is exactly the shell chords", () => {
    expect(DEFAULT_BINDINGS.map((b) => b.actionId)).toEqual([
      ACTIONS.paletteOpen,
      ACTIONS.railToggle,
      ACTIONS.composerFocus,
      ACTIONS.nextNeedsYou,
      ACTIONS.selectionQuote,
      ACTIONS.settingsOpen,
      ACTIONS.sessionNext,
      ACTIONS.sessionPrevious,
      ACTIONS.sessionLiveNext,
      ACTIONS.sessionLivePrevious,
      ACTIONS.transcriptLineUp,
      ACTIONS.transcriptLineDown,
      ACTIONS.transcriptPageUp,
      ACTIONS.transcriptPageDown,
      ACTIONS.transcriptScrollTop,
      ACTIONS.transcriptScrollBottom,
      ACTIONS.settingsClose,
      ACTIONS.cheatsheetToggle,
      ACTIONS.cheatsheetToggle, // the "?" character-key trigger is a second entry for the same action
      ACTIONS.cheatsheetClose,
    ]);
  });

  // Chords are compared through parse+serialize so the assertion is
  // platform-independent ("$mod" resolves per host platform at parse time).
  // palette.open / composer.focus / next-needs-you are STRICT single-press
  // chords since Phase 4a - docs/superpowers/plans/2026-09-04-webui-keybindings-p4-plan.md
  // (Design decision 1) is the authority for dropping the 2a map's OPTIONAL
  // Shift/Alt, which hijacked the browser's DevTools chords (⌘⌥I,
  // Ctrl+Shift+J). The remaining optional modifiers are the legacy-faithful
  // semantics: SelectionQuote guarded only AltGr (alt), rail.toggle guarded
  // both, and the Settings Escape listener had no modifier guard at all.
  test.each([
    [ACTIONS.paletteOpen, "$mod+K"],
    [ACTIONS.railToggle, "$mod+B"],
    [ACTIONS.composerFocus, "$mod+I"],
    [ACTIONS.nextNeedsYou, "$mod+J"],
    [ACTIONS.selectionQuote, "$mod+[Shift]+'"],
    [ACTIONS.settingsOpen, "$mod+,"],
    [ACTIONS.settingsClose, "[Control]+[Alt]+[Shift]+[Meta]+Escape"],
    [ACTIONS.sessionNext, "Alt+ArrowRight"],
    [ACTIONS.sessionPrevious, "Alt+ArrowLeft"],
    [ACTIONS.sessionLiveNext, "Alt+Shift+ArrowRight"],
    [ACTIONS.sessionLivePrevious, "Alt+Shift+ArrowLeft"],
    [ACTIONS.transcriptLineUp, "Alt+ArrowUp"],
    [ACTIONS.transcriptLineDown, "Alt+ArrowDown"],
    [ACTIONS.transcriptPageUp, "Alt+Shift+ArrowUp"],
    [ACTIONS.transcriptPageDown, "Alt+Shift+ArrowDown"],
    [ACTIONS.transcriptScrollTop, "Alt+Home"],
    [ACTIONS.transcriptScrollBottom, "Alt+End"],
    [ACTIONS.cheatsheetToggle, "$mod+/"],
    [ACTIONS.cheatsheetClose, "[Control]+[Alt]+[Shift]+[Meta]+Escape"],
  ])("%s is bound to %s", (actionId, chord) => {
    const binding = DEFAULT_BINDINGS.find((b) => b.actionId === actionId);
    expect(binding).toBeDefined();
    expect(serializeChord(parseChord(parseKeybinding, String(binding?.chord)))).toBe(
      serializeChord(parseChord(parseKeybinding, chord)),
    );
  });

  test('the "?" trigger lists Shift as OPTIONAL (every common layout types ? with Shift held; a bare binding would never fire)', () => {
    const binding = DEFAULT_BINDINGS.find((b) => b.id === CHARACTER_KEY_TRIGGER_BINDING_ID);
    expect(binding?.actionId).toBe(ACTIONS.cheatsheetToggle);
    expect(serializeChord(parseChord(parseKeybinding, String(binding?.chord)))).toBe(
      serializeChord(parseChord(parseKeybinding, "[Shift]+?")),
    );
  });

  test("editable-target policy matches today's per-chord behavior", () => {
    const policy = new Map(DEFAULT_BINDINGS.map((b) => [b.actionId, b.allowInEditable ?? false]));
    expect(policy.get(ACTIONS.paletteOpen)).toBe(true);
    expect(policy.get(ACTIONS.composerFocus)).toBe(true);
    expect(policy.get(ACTIONS.nextNeedsYou)).toBe(true);
    expect(policy.get(ACTIONS.selectionQuote)).toBe(true);
    expect(policy.get(ACTIONS.railToggle)).toBe(false);
    // settings.open fires from editable targets (the p4 plan, Design
    // decision 2): ⌘, never collides with typing.
    expect(policy.get(ACTIONS.settingsOpen)).toBe(true);
    // The Phase 3/4 navigation chords all suppress in editable targets:
    // plain Alt+Arrow, Alt+Shift+Arrow and Alt+Home/End keep their native
    // text-editing meanings (word/selection movement, caret to line
    // start/end) inside inputs and the composer.
    expect(policy.get(ACTIONS.sessionNext)).toBe(false);
    expect(policy.get(ACTIONS.sessionPrevious)).toBe(false);
    // Alt+Shift+ArrowLeft/Right are native word-selection chords in text
    // fields, so the live-session navigation pair suppresses there too.
    expect(policy.get(ACTIONS.sessionLiveNext)).toBe(false);
    expect(policy.get(ACTIONS.sessionLivePrevious)).toBe(false);
    expect(policy.get(ACTIONS.transcriptLineUp)).toBe(false);
    expect(policy.get(ACTIONS.transcriptLineDown)).toBe(false);
    expect(policy.get(ACTIONS.transcriptPageUp)).toBe(false);
    expect(policy.get(ACTIONS.transcriptPageDown)).toBe(false);
    expect(policy.get(ACTIONS.transcriptScrollTop)).toBe(false);
    expect(policy.get(ACTIONS.transcriptScrollBottom)).toBe(false);
    // cheatsheet.toggle's $mod+/ fires from editable targets (never collides
    // with typing); the "?" character-key trigger does NOT (it is itself a
    // printable character). cheatsheet.close mirrors settings.close.
    // (Asserted per-entry, not via the policy map: the two toggle entries
    // share one action id.)
    const toggleBase = DEFAULT_BINDINGS.find((b) => b.id === ACTIONS.cheatsheetToggle);
    const toggleQuestion = DEFAULT_BINDINGS.find((b) => b.id === CHARACTER_KEY_TRIGGER_BINDING_ID);
    const close = DEFAULT_BINDINGS.find((b) => b.id === ACTIONS.cheatsheetClose);
    expect(toggleBase?.allowInEditable).toBe(true);
    expect(toggleQuestion?.allowInEditable).toBe(false);
    expect(close?.allowInEditable).toBe(true);
  });

  test("settings.close and cheatsheet.close are scope-gated, not global", () => {
    expect(DEFAULT_BINDINGS.find((b) => b.actionId === ACTIONS.settingsClose)?.scope).toBe(SETTINGS_SCOPE);
    expect(DEFAULT_BINDINGS.find((b) => b.actionId === ACTIONS.cheatsheetClose)?.scope).toBe(CHEATSHEET_SCOPE);
    for (const other of DEFAULT_BINDINGS.filter(
      (b) => b.actionId !== ACTIONS.settingsClose && b.actionId !== ACTIONS.cheatsheetClose,
    )) {
      expect(other.scope ?? GLOBAL_SCOPE).toBe(GLOBAL_SCOPE);
    }
  });

  test("display formatting renders every default chord (optional modifiers are dropped from display - parked L22 minor, but it must not crash)", () => {
    for (const binding of DEFAULT_BINDINGS) {
      const rendered = formatSequence(parseChord(parseKeybinding, String(binding.chord)));
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
    // settings.open keeps the default suppression too (the p4 plan, Design
    // decision 2: allowInModal: false).
    expect(policy.get(ACTIONS.settingsOpen)).toBe(false);
    // cheatsheet.toggle's $mod+/ is exempt so the same chord toggles the
    // overlay CLOSED while it (or another modal) is open; the "?" trigger
    // and cheatsheet.close keep the default suppression (the OverlayPanel's
    // own Escape handler owns close while focus is inside the dialog).
    const toggleBase = DEFAULT_BINDINGS.find((b) => b.id === ACTIONS.cheatsheetToggle);
    const toggleQuestion = DEFAULT_BINDINGS.find((b) => b.id === CHARACTER_KEY_TRIGGER_BINDING_ID);
    const close = DEFAULT_BINDINGS.find((b) => b.id === ACTIONS.cheatsheetClose);
    expect(toggleBase?.allowInModal).toBe(true);
    expect(toggleQuestion?.allowInModal ?? false).toBe(false);
    expect(close?.allowInModal ?? false).toBe(false);
  });
});

describe("register vs shapes agreement", () => {
  // The preflight (defaultBindingShapesForAction) and the mutation
  // (registerDefaultBindingsForAction) share one predicate for the
  // conditional "?" entry; if they ever diverge the validation can accept a
  // restore that then throws, or reject one that would succeed.
  test("both paths include or exclude the ? trigger for the SAME pref value", () => {
    for (const pref of [true, false]) {
      const registry = createKeybindingsRegistry(parseKeybinding);
      const registered = registerDefaultBindingsForAction(registry, ACTIONS.cheatsheetToggle, {
        characterKeyTriggers: pref,
      }).map((b) => b.id);
      const shaped = defaultBindingShapesForAction(parseKeybinding, ACTIONS.cheatsheetToggle, {
        characterKeyTriggers: pref,
      }).map((s) => s.id);
      expect(registered).toEqual(shaped);
      expect(registered.includes(CHARACTER_KEY_TRIGGER_BINDING_ID)).toBe(pref);
    }
  });
});
