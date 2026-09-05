import { describe, expect, test } from "vitest";
import { ACTIONS } from "./actions";
import { serializeChord } from "./chord";
import { registerDefaultBindings } from "./defaults";
import { rebindAction } from "./overrides";
import { createKeybindingsRegistry, type KeybindingsRegistry } from "./registry";
import { validateOverrideRules } from "./validation";

function withDefaults(): KeybindingsRegistry {
  const registry = createKeybindingsRegistry();
  registerDefaultBindings(registry);
  return registry;
}

function defaultChord(registry: KeybindingsRegistry, bindingId: string): string {
  const binding = registry.getState().bindings.find((b) => b.id === bindingId);
  if (binding === undefined) throw new Error(`test setup: no binding ${bindingId}`);
  return serializeChord(binding.chord);
}

describe("validateOverrideRules", () => {
  test("accepts a well-formed rebind and unbind", () => {
    const registry = withDefaults();
    const result = validateOverrideRules(
      [
        { action: ACTIONS.paletteOpen, chord: "Control+P" },
        { action: ACTIONS.railToggle, chord: null },
      ],
      registry,
    );
    expect(result.warnings).toEqual([]);
    expect(result.rules).toHaveLength(2);
    expect(result.rules[0]?.action).toBe(ACTIONS.paletteOpen);
    expect(serializeChord(result.rules[0]?.chord ?? [])).toBe("Control+P");
    expect(result.rules[1]?.chord).toBeNull();
  });

  test("collects a warning and skips an unknown action without throwing", () => {
    const registry = withDefaults();
    const result = validateOverrideRules(
      [
        { action: "no.such.action", chord: "Control+P" },
        { action: ACTIONS.paletteOpen, chord: "Control+P" },
      ],
      registry,
    );
    expect(result.rules).toHaveLength(1);
    expect(result.warnings).toHaveLength(1);
    expect(result.warnings[0]?.reason).toBe("unknown-action");
    expect(result.warnings[0]?.rule.action).toBe("no.such.action");
  });

  test("collects a warning and skips an unparseable chord", () => {
    const registry = withDefaults();
    const result = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord: "" }], registry);
    expect(result.rules).toHaveLength(0);
    expect(result.warnings[0]?.reason).toBe("unparseable-chord");
  });

  test("last rule wins for repeated rules on the same action", () => {
    const registry = withDefaults();
    const result = validateOverrideRules(
      [
        { action: ACTIONS.paletteOpen, chord: "Control+Y" },
        { action: ACTIONS.paletteOpen, chord: "Control+U" },
      ],
      registry,
    );
    expect(result.warnings).toEqual([]);
    expect(result.rules).toHaveLength(1);
    expect(serializeChord(result.rules[0]?.chord ?? [])).toBe("Control+U");
  });

  test("flags a chord two rules in the same payload try to claim", () => {
    const registry = withDefaults();
    const result = validateOverrideRules(
      [
        { action: ACTIONS.paletteOpen, chord: "Control+P" },
        { action: ACTIONS.railToggle, chord: "Control+P" },
      ],
      registry,
    );
    expect(result.rules).toHaveLength(1);
    expect(result.rules[0]?.action).toBe(ACTIONS.paletteOpen);
    expect(result.warnings).toHaveLength(1);
    expect(result.warnings[0]?.reason).toBe("conflict");
    expect(result.warnings[0]?.conflictWith).toBe(ACTIONS.paletteOpen);
  });

  test("flags a rule that lands on another action's current effective binding", () => {
    const registry = withDefaults();
    const taken = defaultChord(registry, ACTIONS.paletteOpen);
    const result = validateOverrideRules([{ action: ACTIONS.composerFocus, chord: taken }], registry);
    expect(result.rules).toHaveLength(0);
    expect(result.warnings[0]?.reason).toBe("conflict");
    expect(result.warnings[0]?.conflictWith).toBe(ACTIONS.paletteOpen);
  });

  test("a chord freed by the same payload can be claimed by a later rule", () => {
    const registry = withDefaults();
    const freed = defaultChord(registry, ACTIONS.paletteOpen);
    const result = validateOverrideRules(
      [
        { action: ACTIONS.paletteOpen, chord: "Control+Y" },
        { action: ACTIONS.railToggle, chord: freed },
      ],
      registry,
    );
    expect(result.warnings).toEqual([]);
    expect(result.rules).toHaveLength(2);
  });

  test("an unbind frees the action's chord for a later rule", () => {
    const registry = withDefaults();
    const freed = defaultChord(registry, ACTIONS.paletteOpen);
    const result = validateOverrideRules(
      [
        { action: ACTIONS.paletteOpen, chord: null },
        { action: ACTIONS.railToggle, chord: freed },
      ],
      registry,
    );
    expect(result.warnings).toEqual([]);
    expect(result.rules).toHaveLength(2);
  });

  test("a chord taken in a different scope does not conflict", () => {
    const registry = withDefaults();
    // settings.close lives in the settings scope; a global-scope rule may
    // reuse its chord (stack order shadows deterministically).
    const settingsChord = defaultChord(registry, ACTIONS.settingsClose);
    const result = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord: settingsChord }], registry);
    expect(result.warnings).toEqual([]);
    expect(result.rules).toHaveLength(1);
  });
});

describe("validateOverrideRules reserved chords", () => {
  test("Control+W/N/T/Tab, Ctrl+digits, F11 are reserved on non-Apple platforms", () => {
    const registry = withDefaults();
    for (const chord of ["Control+W", "Control+N", "Control+T", "Control+Tab", "Control+5", "F11"]) {
      const result = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord }], registry, "other");
      expect(result.rules, chord).toHaveLength(0);
      expect(result.warnings[0]?.reason, chord).toBe("reserved-chord");
    }
  });

  test("Meta+W is NOT reserved on non-Apple platforms", () => {
    const registry = withDefaults();
    const result = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord: "Meta+W" }], registry, "other");
    expect(result.warnings).toEqual([]);
    expect(result.rules).toHaveLength(1);
  });

  test("the macOS family (Cmd+W/N/T/Q, Cmd+digits, Cmd+Shift+N/T) is reserved on Apple platforms", () => {
    const registry = withDefaults();
    for (const chord of ["Meta+W", "Meta+N", "Meta+T", "Meta+Q", "Meta+5", "Meta+Shift+N", "Meta+Shift+T"]) {
      const result = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord }], registry, "apple");
      expect(result.rules, chord).toHaveLength(0);
      expect(result.warnings[0]?.reason, chord).toBe("reserved-chord");
    }
  });

  test("the Control family stays reserved on Apple platforms too (the survey lists it unqualified)", () => {
    const registry = withDefaults();
    for (const chord of ["Control+W", "Control+Tab", "Control+5"]) {
      const result = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord }], registry, "apple");
      expect(result.rules, chord).toHaveLength(0);
      expect(result.warnings[0]?.reason, chord).toBe("reserved-chord");
    }
  });

  test("a code-SPELLED reserved chord (Control+KeyW) is still reserved - the browser reserves the physical key", () => {
    // tinykeys matches the code name against the same Ctrl+W event the
    // browser never delivers; the string-literal check would miss it
    // (roborev PR #884 round 8).
    const registry = withDefaults();
    for (const chord of ["Control+KeyW", "Meta+KeyT"]) {
      const result = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord }], registry, "apple");
      expect(result.rules, chord).toHaveLength(0);
      expect(result.warnings[0]?.reason, chord).toBe("reserved-chord");
    }
  });

  test("a regex chord matching a reserved event is reserved too (Control+(W)), conservatively", () => {
    // The regex never string-matches the canonical reserved set, but it
    // matches the same browser-reserved event (roborev PR #884 round 14).
    const registry = withDefaults();
    const result = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord: "Control+(W)" }], registry, "other");
    expect(result.rules).toHaveLength(0);
    expect(result.warnings[0]?.reason).toBe("reserved-chord");
    // A regex that matches no reserved event is fine.
    const ok = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord: "Control+(X)" }], registry, "other");
    expect(ok.warnings).toEqual([]);
    expect(ok.rules).toHaveLength(1);
  });

  test("Alt+F4 is reserved on non-Apple platforms but not on Apple", () => {
    const registry = withDefaults();
    const other = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord: "Alt+F4" }], registry, "other");
    expect(other.warnings[0]?.reason).toBe("reserved-chord");
    const apple = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord: "Alt+F4" }], registry, "apple");
    expect(apple.warnings).toEqual([]);
  });

  test("Ctrl+Shift+P is NOT reserved: the survey's caveat is Firefox-private-window specific", () => {
    const registry = withDefaults();
    const result = validateOverrideRules(
      [{ action: ACTIONS.paletteOpen, chord: "Control+Shift+P" }],
      registry,
      "other",
    );
    expect(result.warnings).toEqual([]);
    expect(result.rules).toHaveLength(1);
  });

  test("an optional extra modifier does not rescue a reserved chord (the no-modifier case is undeliverable)", () => {
    const registry = withDefaults();
    const result = validateOverrideRules(
      [{ action: ACTIONS.paletteOpen, chord: "[Shift]+Control+W" }],
      registry,
      "other",
    );
    expect(result.rules).toHaveLength(0);
    expect(result.warnings[0]?.reason).toBe("reserved-chord");
  });

  test("extra REQUIRED modifiers escape the reserved list (Cmd+Shift+W is not the survey's Cmd+W)", () => {
    const registry = withDefaults();
    const result = validateOverrideRules([{ action: ACTIONS.paletteOpen, chord: "Meta+Shift+W" }], registry, "apple");
    expect(result.warnings).toEqual([]);
    expect(result.rules).toHaveLength(1);
  });

  test("multi-press sequences are never reserved", () => {
    const registry = withDefaults();
    const result = validateOverrideRules(
      [{ action: ACTIONS.paletteOpen, chord: "Control+K Control+W" }],
      registry,
      "other",
    );
    expect(result.warnings).toEqual([]);
    expect(result.rules).toHaveLength(1);
  });
});

describe("validateOverrideRules dropped-override restorations", () => {
  test("a rule claiming a chord a dropped override's restored default needs is skipped with a warning", () => {
    const registry = withDefaults();
    const reclaimed = defaultChord(registry, ACTIONS.paletteOpen);
    // The live state the reviewer's payload 1 produces: palette.open moved
    // away, rail.toggle claimed its freed default chord.
    rebindAction(registry, ACTIONS.paletteOpen, "Control+Y");
    rebindAction(registry, ACTIONS.railToggle, reclaimed);

    // Payload 2 drops palette.open (its defaults must be restored) and keeps
    // rail.toggle's claim on the chord the restore needs. The restore wins:
    // the rule is skipped with a conflict warning so every action stays bound.
    const result = validateOverrideRules(
      [{ action: ACTIONS.railToggle, chord: reclaimed }],
      registry,
      "other",
      new Set([ACTIONS.paletteOpen, ACTIONS.railToggle]),
    );

    expect(result.rules).toEqual([]);
    expect(result.warnings).toHaveLength(1);
    expect(result.warnings[0]?.reason).toBe("conflict");
    expect(result.warnings[0]?.rule).toEqual({ action: ACTIONS.railToggle, chord: reclaimed });
    expect(result.warnings[0]?.conflictWith).toBe(ACTIONS.paletteOpen);
  });

  test("a dropped override restores cleanly when its default chord was not reclaimed", () => {
    const registry = withDefaults();
    rebindAction(registry, ACTIONS.paletteOpen, "Control+Y");
    const result = validateOverrideRules([], registry, "other", new Set([ACTIONS.paletteOpen]));
    expect(result.rules).toEqual([]);
    expect(result.warnings).toEqual([]);
  });
});

describe("validateOverrideRules overlap conflicts", () => {
  test("flags an override whose exact chord overlaps a default's OPTIONAL modifiers (dispatch would shadow it)", () => {
    // palette.open's default is Control+[Meta]+K: a plain Control+K matches
    // it at dispatch time, and the earlier-registered default would win -
    // so this is a conflict, not a valid remap.
    const registry = withDefaults();
    const result = validateOverrideRules([{ action: ACTIONS.composerFocus, chord: "Control+K" }], registry);
    expect(result.rules).toHaveLength(0);
    expect(result.warnings).toHaveLength(1);
    expect(result.warnings[0]?.reason).toBe("conflict");
    expect(result.warnings[0]?.conflictWith).toBe(ACTIONS.paletteOpen);
    expect(result.warnings[0]?.message).toContain('scope "global"');
  });

  test("a genuinely non-overlapping chord (extra required modifier nothing allows) still validates", () => {
    const registry = withDefaults();
    const result = validateOverrideRules([{ action: ACTIONS.composerFocus, chord: "Control+Alt+B" }], registry);
    expect(result.warnings).toEqual([]);
    expect(result.rules).toHaveLength(1);
  });

  test("the reserved-chord check runs before conflict detection: a reserved chord reports only reserved-chord", () => {
    const registry = withDefaults();
    // A foreign live binding holds Control+W, so the chord is BOTH reserved
    // and conflicting; check order must surface only the reserved warning.
    registry.getState().registerBinding({ id: "foreign", actionId: "foreign.action", chord: "Control+W" });
    const result = validateOverrideRules([{ action: ACTIONS.composerFocus, chord: "Control+W" }], registry, "other");
    expect(result.rules).toHaveLength(0);
    expect(result.warnings).toHaveLength(1);
    expect(result.warnings[0]?.reason).toBe("reserved-chord");
  });
});
