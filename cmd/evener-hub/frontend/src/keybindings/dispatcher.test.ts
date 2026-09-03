import { afterEach, describe, expect, test, vi } from "vitest";
import { createKeybindingDispatcher, type KeybindingDispatcher } from "./dispatcher";
import { createKeybindingsRegistry, type KeybindingsRegistry } from "./registry";

// jsdom resolves tinykeys' "$mod" to "Control" on every host, so every Mod
// chord in this file is pressed with ctrlKey and the tests stay
// platform-deterministic.
function keydown(init: KeyboardEventInit): KeyboardEvent {
  return new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
}

function pressOn(target: Window | Element, init: KeyboardEventInit): KeyboardEvent {
  const event = keydown(init);
  target.dispatchEvent(event);
  return event;
}

const MOD_K: KeyboardEventInit = { key: "k", code: "KeyK", ctrlKey: true };

describe("dispatcher", () => {
  let registry: KeybindingsRegistry;
  let dispatcher: KeybindingDispatcher;
  let detach: () => void;

  afterEach(() => {
    detach();
    dispatcher.dispose();
    document.body.innerHTML = "";
  });

  function setup(options: { isModalOpen?: (event: KeyboardEvent) => boolean } = {}) {
    registry = createKeybindingsRegistry();
    dispatcher = createKeybindingDispatcher({ registry, ...options });
    detach = dispatcher.attach(window);
    return registry.getState();
  }

  test("runs the action bound to a matching chord and preventDefaults", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("palette.open", run);
    state.registerBinding({ id: "b1", actionId: "palette.open", chord: "$mod+K" });
    const event = pressOn(window, MOD_K);
    expect(run).toHaveBeenCalledTimes(1);
    expect(run).toHaveBeenCalledWith(event);
    expect(event.defaultPrevented).toBe(true);
  });

  test("ignores IME composition keydowns (isComposing / keyCode 229)", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    pressOn(window, { ...MOD_K, isComposing: true });
    pressOn(window, { ...MOD_K, keyCode: 229 });
    expect(run).not.toHaveBeenCalled();
  });

  test("yields to a keydown another handler already claimed", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    const inner = document.createElement("div");
    inner.addEventListener("keydown", (event) => event.preventDefault());
    document.body.appendChild(inner);
    pressOn(inner, MOD_K);
    expect(run).not.toHaveBeenCalled();
  });

  test("ignoreIfDefaultPrevented: false fires on a keydown another handler claimed", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    // RailHost's ⌘B listener has no defaultPrevented check (RailHost.tsx:59-66)
    // and toggles even when an inner handler claimed the keydown; the flag
    // exists so the module can express exactly that.
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+B", ignoreIfDefaultPrevented: false });
    const inner = document.createElement("div");
    inner.addEventListener("keydown", (event) => event.preventDefault());
    document.body.appendChild(inner);
    pressOn(inner, { key: "b", code: "KeyB", ctrlKey: true });
    expect(run).toHaveBeenCalledTimes(1);
  });

  test("suppresses bindings in editable targets by default", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    const input = document.createElement("input");
    document.body.appendChild(input);
    const event = pressOn(input, MOD_K);
    expect(run).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  test("allowInEditable bindings fire from editable targets", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K", allowInEditable: true });
    const input = document.createElement("input");
    document.body.appendChild(input);
    pressOn(input, MOD_K);
    expect(run).toHaveBeenCalledTimes(1);
  });

  test("the modal predicate suppresses bindings by default", () => {
    const state = setup({ isModalOpen: () => true });
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K", allowInEditable: true });
    const event = pressOn(window, MOD_K);
    expect(run).not.toHaveBeenCalled();
    expect(event.defaultPrevented).toBe(false);
  });

  test("allowInModal: true fires while the modal predicate reports open", () => {
    // Today's ⌘K handler deliberately exempts palette.open from the modal
    // guard (and RailHost's ⌘B / SelectionQuote's ⌘' listeners have no modal
    // check at all), so the modal layer is per-binding, not blanket.
    const state = setup({ isModalOpen: () => true });
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K", allowInModal: true });
    pressOn(window, MOD_K);
    expect(run).toHaveBeenCalledTimes(1);
  });

  test("the default modal check sees an [aria-modal] ancestor of the target", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    const modal = document.createElement("div");
    modal.setAttribute("aria-modal", "true");
    const inner = document.createElement("button");
    modal.appendChild(inner);
    document.body.appendChild(modal);
    pressOn(inner, MOD_K);
    expect(run).not.toHaveBeenCalled();
    pressOn(window, MOD_K);
    expect(run).toHaveBeenCalledTimes(1);
  });

  test("scope stack top-down shadows the global scope for the same chord", () => {
    const state = setup();
    const globalRun = vi.fn();
    const scopedRun = vi.fn();
    state.registerAction("global.action", globalRun);
    state.registerAction("scoped.action", scopedRun);
    state.registerBinding({ id: "b1", actionId: "global.action", chord: "$mod+K" });
    state.registerBinding({ id: "b2", actionId: "scoped.action", chord: "$mod+K", scope: "settings" });
    pressOn(window, MOD_K);
    expect(globalRun).toHaveBeenCalledTimes(1);
    expect(scopedRun).not.toHaveBeenCalled();
    state.pushScope("settings");
    pressOn(window, MOD_K);
    expect(scopedRun).toHaveBeenCalledTimes(1);
    expect(globalRun).toHaveBeenCalledTimes(1);
    state.popScope("settings");
    pressOn(window, MOD_K);
    expect(globalRun).toHaveBeenCalledTimes(2);
  });

  test("a scope without a matching binding falls through to global", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    state.pushScope("settings");
    pressOn(window, MOD_K);
    expect(run).toHaveBeenCalledTimes(1);
  });

  test("a binding whose action is not registered does not fire or preventDefault", () => {
    const state = setup();
    state.registerBinding({ id: "b1", actionId: "missing", chord: "$mod+K" });
    const event = pressOn(window, MOD_K);
    expect(event.defaultPrevented).toBe(false);
  });

  test("action handlers run in registration order until one handles the event", () => {
    const state = setup();
    const calls: string[] = [];
    state.registerAction("a", () => {
      calls.push("first");
      return false; // declines: try the next handler
    });
    state.registerAction("a", () => {
      calls.push("second");
    });
    state.registerAction("a", () => {
      calls.push("third");
    });
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    const event = pressOn(window, MOD_K);
    expect(calls).toEqual(["first", "second"]);
    expect(event.defaultPrevented).toBe(true);
  });

  test("an event every handler declines is not preventDefault'd", () => {
    const state = setup();
    state.registerAction("a", () => false);
    state.registerAction("a", () => false);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    const event = pressOn(window, MOD_K);
    expect(event.defaultPrevented).toBe(false);
  });

  test("matches a synthetic keydown with no code (fireEvent-style), running the real event", () => {
    // The pre-dispatcher listeners never looked at event.code, and the app's
    // existing suites (and any app code dispatching a bare KeyboardEvent)
    // construct codeless events that tinykeys' isKeyboardEvent gate would
    // otherwise drop.
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    const event = pressOn(window, { key: "k", ctrlKey: true });
    expect(event.code).toBe("");
    expect(run).toHaveBeenCalledTimes(1);
    expect(run).toHaveBeenCalledWith(event);
    expect(event.defaultPrevented).toBe(true);
  });

  test("matches a multi-press sequence", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "x y" });
    pressOn(window, { key: "x", code: "KeyX" });
    expect(run).not.toHaveBeenCalled();
    const event = pressOn(window, { key: "y", code: "KeyY" });
    expect(run).toHaveBeenCalledTimes(1);
    expect(event.defaultPrevented).toBe(true);
  });

  test("a binding stops matching once unregistered", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    state.unregisterBinding("b1");
    pressOn(window, MOD_K);
    expect(run).not.toHaveBeenCalled();
  });

  test("detach removes the listener", () => {
    const state = setup();
    const run = vi.fn();
    state.registerAction("a", run);
    state.registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    detach();
    pressOn(window, MOD_K);
    expect(run).not.toHaveBeenCalled();
    detach = () => undefined;
  });
});

describe("default editable-target detection", () => {
  test.each(["input", "textarea", "select"])("treats <%s> as editable", (tagName) => {
    const registry = createKeybindingsRegistry();
    const dispatcher = createKeybindingDispatcher({ registry });
    const detach = dispatcher.attach(window);
    const run = vi.fn();
    registry.getState().registerAction("a", run);
    registry.getState().registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    const element = document.createElement(tagName);
    document.body.appendChild(element);
    pressOn(element, MOD_K);
    expect(run).not.toHaveBeenCalled();
    detach();
    dispatcher.dispose();
    document.body.innerHTML = "";
  });

  test("treats a contenteditable element as editable", () => {
    const registry = createKeybindingsRegistry();
    const dispatcher = createKeybindingDispatcher({ registry });
    const detach = dispatcher.attach(window);
    const run = vi.fn();
    registry.getState().registerAction("a", run);
    registry.getState().registerBinding({ id: "b1", actionId: "a", chord: "$mod+K" });
    // jsdom does not implement the contentEditable/isContentEditable IDL
    // properties, so the test sets the content attribute the dispatcher's
    // [contenteditable] check (and tinykeys' own default ignore) matches.
    const element = document.createElement("div");
    element.setAttribute("contenteditable", "true");
    document.body.appendChild(element);
    pressOn(element, MOD_K);
    expect(run).not.toHaveBeenCalled();
    detach();
    dispatcher.dispose();
    document.body.innerHTML = "";
  });
});
