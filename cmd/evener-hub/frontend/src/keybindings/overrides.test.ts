import { describe, expect, test } from "vitest";
import { ACTIONS } from "./actions";
import { serializeChord } from "./chord";
import { registerDefaultBindings } from "./defaults";
import { rebindAction, restoreDefaultBinding } from "./overrides";
import { createKeybindingsRegistry, GLOBAL_SCOPE, type KeybindingsRegistry } from "./registry";

function withDefaults(): KeybindingsRegistry {
  const registry = createKeybindingsRegistry();
  registerDefaultBindings(registry);
  return registry;
}

function bindingsFor(registry: KeybindingsRegistry, actionId: string) {
  return registry.getState().bindings.filter((b) => b.actionId === actionId);
}

describe("rebindAction", () => {
  test("replaces the default binding AND its mod-twin with a single override entry", () => {
    const registry = withDefaults();
    expect(bindingsFor(registry, ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);

    rebindAction(registry, ACTIONS.paletteOpen, "Control+P");

    const bindings = bindingsFor(registry, ACTIONS.paletteOpen);
    expect(bindings).toHaveLength(1);
    const override = bindings[0];
    expect(override?.id).toBe(`${ACTIONS.paletteOpen}#override`);
    expect(serializeChord(override?.chord ?? [])).toBe("Control+P");
  });

  test("keeps the default's scope and policy flags - a rebind changes the chord, not the guards", () => {
    const registry = withDefaults();
    rebindAction(registry, ACTIONS.railToggle, "Control+Alt+B");
    const override = bindingsFor(registry, ACTIONS.railToggle)[0];
    expect(override?.scope).toBe(GLOBAL_SCOPE);
    expect(override?.allowInEditable).toBe(false);
    expect(override?.allowInModal).toBe(true);
    expect(override?.ignoreIfDefaultPrevented).toBe(false);
  });

  test("keeps a non-global default's scope (settings.close stays in the settings scope)", () => {
    const registry = withDefaults();
    rebindAction(registry, ACTIONS.settingsClose, "Control+Shift+Escape");
    const override = bindingsFor(registry, ACTIONS.settingsClose)[0];
    expect(override?.scope).toBe("settings");
    expect(override?.allowInEditable).toBe(true);
  });

  test("does not twin-expand the override chord: the user chord binds exactly what was written", () => {
    const registry = withDefaults();
    // "$mod" resolves to Control under jsdom; a twin would add a Meta entry.
    rebindAction(registry, ACTIONS.paletteOpen, "$mod+P");
    expect(bindingsFor(registry, ACTIONS.paletteOpen)).toHaveLength(1);
  });

  test("a null chord unbinds the action entirely", () => {
    const registry = withDefaults();
    rebindAction(registry, ACTIONS.paletteOpen, null);
    expect(bindingsFor(registry, ACTIONS.paletteOpen)).toHaveLength(0);
    // Other actions are untouched.
    expect(bindingsFor(registry, ACTIONS.railToggle)).toHaveLength(2);
  });

  test("re-applying the same override is idempotent", () => {
    const registry = withDefaults();
    rebindAction(registry, ACTIONS.paletteOpen, "Control+P");
    const first = bindingsFor(registry, ACTIONS.paletteOpen);
    rebindAction(registry, ACTIONS.paletteOpen, "Control+P");
    const second = bindingsFor(registry, ACTIONS.paletteOpen);
    expect(second).toHaveLength(1);
    expect(serializeChord(second[0]?.chord ?? [])).toBe(serializeChord(first[0]?.chord ?? []));
  });

  test("a later override replaces an earlier one (last rule wins)", () => {
    const registry = withDefaults();
    rebindAction(registry, ACTIONS.paletteOpen, "Control+P");
    rebindAction(registry, ACTIONS.paletteOpen, "Control+U");
    const bindings = bindingsFor(registry, ACTIONS.paletteOpen);
    expect(bindings).toHaveLength(1);
    expect(serializeChord(bindings[0]?.chord ?? [])).toBe("Control+U");
  });

  test("throws on an unknown action id and leaves the registry unchanged", () => {
    const registry = withDefaults();
    const before = registry.getState().bindings;
    expect(() => rebindAction(registry, "no.such.action", "Control+P")).toThrow(/unknown/);
    expect(registry.getState().bindings).toEqual(before);
  });

  test("throws on an unparseable chord and leaves the registry unchanged", () => {
    const registry = withDefaults();
    const before = registry.getState().bindings;
    expect(() => rebindAction(registry, ACTIONS.paletteOpen, "")).toThrow();
    expect(registry.getState().bindings).toEqual(before);
  });

  test("throws on a conflict with another action's effective binding, atomically", () => {
    const registry = withDefaults();
    registry.getState().registerBinding({ id: "other", actionId: "other.action", chord: "Control+X" });
    const before = registry.getState().bindings;
    expect(() => rebindAction(registry, ACTIONS.paletteOpen, "Control+X")).toThrow(/conflict/);
    // The failure is atomic: palette.open's default + twin survive intact.
    expect(registry.getState().bindings).toEqual(before);
  });

  test("no conflict when the same chord lives in a different scope", () => {
    const registry = withDefaults();
    registry
      .getState()
      .registerBinding({ id: "other", actionId: "other.action", chord: "Control+X", scope: "settings" });
    expect(() => rebindAction(registry, ACTIONS.paletteOpen, "Control+X")).not.toThrow();
  });
});

describe("restoreDefaultBinding", () => {
  test("restores the default and its mod-twin after an override", () => {
    const registry = withDefaults();
    rebindAction(registry, ACTIONS.paletteOpen, "Control+P");
    restoreDefaultBinding(registry, ACTIONS.paletteOpen);
    expect(bindingsFor(registry, ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
  });

  test("restores the default after an unbind", () => {
    const registry = withDefaults();
    rebindAction(registry, ACTIONS.paletteOpen, null);
    restoreDefaultBinding(registry, ACTIONS.paletteOpen);
    expect(bindingsFor(registry, ACTIONS.paletteOpen)).toHaveLength(2);
  });

  test("is a no-op equivalent on an action that was never overridden", () => {
    const registry = withDefaults();
    restoreDefaultBinding(registry, ACTIONS.paletteOpen);
    expect(bindingsFor(registry, ACTIONS.paletteOpen).map((b) => b.id)).toEqual([
      ACTIONS.paletteOpen,
      `${ACTIONS.paletteOpen}#mod-twin`,
    ]);
  });

  test("throws on an unknown action id", () => {
    const registry = withDefaults();
    expect(() => restoreDefaultBinding(registry, "no.such.action")).toThrow(/unknown/);
  });
});
