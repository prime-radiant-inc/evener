import { parseKeybinding } from "tinykeys";
import { describe, expect, test, vi } from "vitest";
import type { KeybindingParser } from "./chord";
import { createKeybindingsRegistry, GLOBAL_SCOPE, type KeybindingsState } from "./registry";

describe("the parser port", () => {
  test("string chords parse through the parser the registry was created with", () => {
    const parse: KeybindingParser = () => [[["Meta"], [], "z"]];
    const registry = createKeybindingsRegistry(parse);
    const binding = registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "ignored" });
    expect(binding.chord).toEqual([{ modifiers: ["Meta"], optionalModifiers: [], key: "z" }]);
    expect(registry.parseKeybinding).toBe(parse);
  });
});

describe("store shape", () => {
  test("two registries share nothing: actions, bindings and scopes registered in one never appear in the other", () => {
    const first = createKeybindingsRegistry(parseKeybinding);
    const second = createKeybindingsRegistry(parseKeybinding);
    first.getState().registerAction("a", vi.fn());
    first.getState().registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    first.getState().pushScope("settings");
    expect(second.getState().actions.size).toBe(0);
    expect(second.getState().bindings).toEqual([]);
    expect(second.getState().scopeStack).toEqual([]);
    // The same chord in the same scope is free in the other registry.
    expect(() => second.getState().registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" })).not.toThrow();
  });

  test("subscribe delivers the new and previous state on every change; the disposer stops delivery", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
    const before = registry.getState();
    const listener = vi.fn();
    const unsubscribe = registry.subscribe(listener);
    registry.getState().pushScope("a");
    expect(listener).toHaveBeenCalledTimes(1);
    const [state, previous] = listener.mock.calls[0] as [KeybindingsState, KeybindingsState];
    expect(state).toBe(registry.getState());
    expect(state.scopeStack).toEqual(["a"]);
    expect(previous).toBe(before);
    expect(previous.scopeStack).toEqual([]);
    unsubscribe();
    registry.getState().pushScope("b");
    expect(listener).toHaveBeenCalledTimes(1);
  });

  test("getInitialState is the creation state, for snapshot-based view bindings", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
    const initial = registry.getState();
    registry.getState().pushScope("a");
    expect(registry.getInitialState()).toBe(initial);
    expect(registry.getState()).not.toBe(initial);
  });

  test("setState merges a partial or an updater and notifies", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
    const listener = vi.fn();
    registry.subscribe(listener);
    registry.setState({ scopeStack: ["x"] });
    registry.setState((s) => ({ scopeStack: [...s.scopeStack, "y"] }));
    expect(registry.getState().scopeStack).toEqual(["x", "y"]);
    expect(typeof registry.getState().pushScope).toBe("function");
    expect(listener).toHaveBeenCalledTimes(2);
  });
});

describe("action registration", () => {
  test("registers and unregisters an action", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
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
    const registry = createKeybindingsRegistry(parseKeybinding);
    const first = vi.fn();
    const second = vi.fn();
    registry.getState().registerAction("a", first);
    const unregisterSecond = registry.getState().registerAction("a", second);
    unregisterSecond();
    expect(registry.getState().actions.get("a")).toEqual([first]);
  });

  test("a stale disposer cannot remove a later registration of the same id", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
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
    const registry = createKeybindingsRegistry(parseKeybinding);
    const binding = registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    expect(binding.scope).toBe(GLOBAL_SCOPE);
    expect(binding.allowInEditable).toBe(false);
    expect(registry.getState().bindings).toHaveLength(1);
  });

  test("allowInModal defaults to false and is stored verbatim when set", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
    const plain = registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    expect(plain.allowInModal).toBe(false);
    const exempt = registry
      .getState()
      .registerBinding({ id: "b2", actionId: "a1", chord: "$mod+J", allowInModal: true });
    expect(exempt.allowInModal).toBe(true);
  });

  test("accepts a pre-parsed chord sequence", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
    const parsed = registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    const fromAst = registry.getState().registerBinding({ id: "b2", actionId: "a1", chord: parsed.chord, scope: "s" });
    expect(fromAst.chord).toEqual(parsed.chord);
  });

  test("rejects the same chord twice in the same scope", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
    registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    expect(() => registry.getState().registerBinding({ id: "b2", actionId: "a2", chord: "$mod+K" })).toThrow(
      /conflict/i,
    );
    expect(registry.getState().bindings).toHaveLength(1);
  });

  test("allows the same chord in different scopes (stack order shadows)", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
    registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "Escape" });
    expect(() =>
      registry.getState().registerBinding({ id: "b2", actionId: "a2", chord: "Escape", scope: "settings" }),
    ).not.toThrow();
  });

  test("rejects a duplicate binding id even across scopes", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
    registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    expect(() =>
      registry.getState().registerBinding({ id: "b1", actionId: "a2", chord: "$mod+J", scope: "settings" }),
    ).toThrow(/b1/);
  });

  test("unregisterBinding removes the entry", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
    registry.getState().registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K" });
    expect(registry.getState().unregisterBinding("b1")).toBe(true);
    expect(registry.getState().bindings).toHaveLength(0);
    expect(registry.getState().unregisterBinding("b1")).toBe(false);
  });

  test("carries an optional structured when clause without evaluating it", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
    const binding = registry
      .getState()
      .registerBinding({ id: "b1", actionId: "a1", chord: "$mod+K", when: { pane: "settings" } });
    expect(binding.when).toEqual({ pane: "settings" });
  });
});

describe("scope stack", () => {
  test("push appends, pop removes the topmost matching scope", () => {
    const registry = createKeybindingsRegistry(parseKeybinding);
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
    const registry = createKeybindingsRegistry(parseKeybinding);
    registry.getState().pushScope("a");
    const dispose = registry.getState().pushScope("b");
    dispose();
    expect(registry.getState().scopeStack).toEqual(["a"]);
    dispose();
    expect(registry.getState().scopeStack).toEqual(["a"]);
  });
});
