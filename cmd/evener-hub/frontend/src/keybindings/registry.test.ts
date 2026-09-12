import { describe, expect, test, vi } from "vitest";
import { createKeybindingsRegistry, GLOBAL_SCOPE } from "./registry";

describe("action registration", () => {
  test("registers and unregisters an action", () => {
    const registry = createKeybindingsRegistry();
    const run = vi.fn();
    const unregister = registry.getState().registerAction("palette.open", run);
    expect(registry.getState().actions.get("palette.open")).toEqual([run]);
    unregister();
    expect(registry.getState().actions.has("palette.open")).toBe(false);
  });

  test("multiple handlers coexist per action id; a disposer removes only its own registration", () => {
    // SelectionQuote mounts one instance per session pane, each registering
    // selection.quote - the multi-instance equivalent of the old per-instance
    // document listeners. Overwriting or clobbering across instances breaks
    // ⌘' in multi-pane workspaces.
    const registry = createKeybindingsRegistry();
    const first = vi.fn();
    const second = vi.fn();
    registry.getState().registerAction("a", first);
    const unregisterSecond = registry.getState().registerAction("a", second);
    unregisterSecond();
    expect(registry.getState().actions.get("a")).toEqual([first]);
  });

  test("a stale disposer cannot remove a later registration of the same id", () => {
    const registry = createKeybindingsRegistry();
    const first = vi.fn();
    const second = vi.fn();
    const unregisterFirst = registry.getState().registerAction("a", first);
    unregisterFirst();
    registry.getState().registerAction("a", second);
    unregisterFirst(); // stale: must not clobber the second registration
    expect(registry.getState().actions.get("a")).toEqual([second]);
  });
});

describe("binding registration", () => {
  test("applies defaults: global scope, editable targets suppressed", () => {
    const registry = createKeybindingsRegistry();
    const binding = registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    expect(binding.scope).toBe(GLOBAL_SCOPE);
    expect(binding.allowInEditable).toBe(false);
    expect(registry.getState().bindings).toHaveLength(1);
  });

  test("allowInModal defaults to false and is stored verbatim when set", () => {
    const registry = createKeybindingsRegistry();
    const plain = registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    expect(plain.allowInModal).toBe(false);
    const exempt = registry
      .getState()
      .registerBinding({ id: "b2", actionId: "a1", chord: "$mod+J", allowInModal: true });
    expect(exempt.allowInModal).toBe(true);
  });

  test("accepts a pre-parsed chord sequence", () => {
    const registry = createKeybindingsRegistry();
    const parsed = registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    const fromAst = registry.getState().registerBinding({ id: "b2", actionId: "a1", chord: parsed.chord, scope: "s" });
    expect(fromAst.chord).toEqual(parsed.chord);
  });

  test("rejects the same chord twice in the same scope", () => {
    const registry = createKeybindingsRegistry();
    registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    expect(() => registry.getState().registerBinding({ id: "b2", actionId: "a2", chord: "$mod+K" })).toThrow(
      /conflict/i,
    );
    expect(registry.getState().bindings).toHaveLength(1);
  });

  test("allows the same chord in different scopes (stack order shadows)", () => {
    const registry = createKeybindingsRegistry();
    registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "Escape" });
    expect(() =>
      registry.getState().registerBinding({ id: "b2", actionId: "a2", chord: "Escape", scope: "settings" }),
    ).not.toThrow();
  });

  test("rejects a duplicate binding id even across scopes", () => {
    const registry = createKeybindingsRegistry();
    registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    expect(() =>
      registry.getState().registerBinding({ id: "b1", actionId: "a2", chord: "$mod+J", scope: "settings" }),
    ).toThrow(/b1/);
  });

  test("unregisterBinding removes the entry", () => {
    const registry = createKeybindingsRegistry();
    registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    expect(registry.getState().unregisterBinding("b1")).toBe(true);
    expect(registry.getState().bindings).toHaveLength(0);
    expect(registry.getState().unregisterBinding("b1")).toBe(false);
  });

  test("carries an optional structured when clause without evaluating it", () => {
    const registry = createKeybindingsRegistry();
    const binding = registry
      .getState()
      .registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K", when: { pane: "settings" } });
    expect(binding.when).toEqual({ pane: "settings" });
  });
});

describe("scope stack", () => {
  test("push appends, pop removes the topmost matching scope", () => {
    const registry = createKeybindingsRegistry();
    registry.getState().pushScope("a");
    registry.getState().pushScope("b");
    registry.getState().pushScope("a");
    expect(registry.getState().scopeStack).toEqual(["a", "b", "a"]);
    expect(registry.getState().popScope("a")).toBe(true);
    expect(registry.getState().scopeStack).toEqual(["a", "b"]);
    expect(registry.getState().popScope("missing")).toBe(false);
    expect(registry.getState().scopeStack).toEqual(["a", "b"]);
  });

  test("the pushScope disposer removes its own entry and is idempotent", () => {
    const registry = createKeybindingsRegistry();
    registry.getState().pushScope("a");
    const dispose = registry.getState().pushScope("b");
    dispose();
    expect(registry.getState().scopeStack).toEqual(["a"]);
    dispose();
    expect(registry.getState().scopeStack).toEqual(["a"]);
  });
});
