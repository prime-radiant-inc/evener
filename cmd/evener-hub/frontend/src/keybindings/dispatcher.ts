// The single window-level keydown dispatcher. Matching is tinykeys
// (createKeybindingsHandler over the active scope set); precedence, in order:
//
//   1. IME composition keydowns (event.isComposing / keyCode 229) are ignored.
//   2. The scope stack, top-down, then the global scope, decide WHICH binding
//      a chord resolves to (the first/highest-precedence binding claims it).
//   3. Per-binding policy on the matched binding, each flag reproducing one
//      of the pre-dispatcher listeners' own guards:
//      - ignoreIfDefaultPrevented (default true): an already-claimed keydown
//        is ignored.
//      - allowInModal (default false): an open modal suppresses the binding -
//        [aria-modal="true"] ancestor of the event target by default, plus
//        whatever store check the caller's injected predicate adds (the app
//        wiring adds paletteStore.open there).
//      - allowInEditable (default false): an editable event target suppresses
//        the binding.
//
// The first (highest-precedence) binding for a chord claims it outright: a
// firing binding preventDefaults and stops evaluation, and a shadowed or
// policy-blocked binding never falls through to a lower-precedence twin.
//
// An event another handler already claimed (event.defaultPrevented) is
// ignored per binding: ignoreIfDefaultPrevented defaults to true, matching
// the pre-dispatcher AppShell ⌘K/⌘I/⌘J, Settings Escape, and SelectionQuote
// ⌘' listeners - but NOT RailHost's ⌘B listener, which had no
// defaultPrevented check and so binds with ignoreIfDefaultPrevented: false
// in defaults.ts.

import { createKeybindingsHandler, type KeybindingsMap } from "tinykeys";
import { serializeChord } from "./chord";
import { GLOBAL_SCOPE, type KeybindingsRegistry, type KeybindingsState, keybindingsRegistry } from "./registry";

export type ModalOpenPredicate = (event: KeyboardEvent) => boolean;
export type EditableTargetPredicate = (target: EventTarget | null) => boolean;

/** Walks up from the target; the FIRST element bearing a contenteditable
 * attribute decides - editable unless its value is "false" (empty string,
 * "true" and "plaintext-only" are all editable), which is how the
 * contentEditable IDL property inherits in a real browser. A
 * contenteditable="false" island inside an editor (a toolbar, an embedded
 * control) is therefore NOT editable. */
function hasEditableContentEditableAncestor(target: HTMLElement): boolean {
  for (let element: HTMLElement | null = target; element !== null; element = element.parentElement) {
    const value = element.getAttribute("contenteditable");
    if (value !== null) return value.toLowerCase() !== "false";
  }
  return false;
}

/** shell/rail/RailHost.tsx's isEditableTarget, plus the contenteditable
 * attribute walk above: isContentEditable alone already covers descendants
 * of an editable ancestor in a real browser, but jsdom does not implement
 * the property at all (it is undefined there), so the attribute walk stands
 * in for it. Everything else doesn't count. */
export const isEditableTarget: EditableTargetPredicate = (target) => {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.isContentEditable ||
    hasEditableContentEditableAncestor(target) ||
    target.tagName === "INPUT" ||
    target.tagName === "TEXTAREA" ||
    target.tagName === "SELECT"
  );
};

/** The DOM half of the shell's old blockedByOpenModal check. The palette is
 * not an [aria-modal] element, so the app wiring (shell/installKeybindings.ts)
 * composes this with a paletteStore.open store check. */
export const isModalOpenTarget: ModalOpenPredicate = (event) => {
  const target = event.target;
  return target instanceof Element && target.closest('[aria-modal="true"]') !== null;
};

export interface DispatcherOptions {
  /** Defaults to the app-wide registry singleton. */
  registry?: KeybindingsRegistry;
  isModalOpen?: ModalOpenPredicate;
  isEditableTarget?: EditableTargetPredicate;
}

export interface KeybindingDispatcher {
  handleKeyDown(event: KeyboardEvent): void;
  /** Adds the keydown listener; returns the detach function. */
  attach(target: Window): () => void;
  /** Stops tracking the registry. Always pair with the attach disposer. */
  dispose(): void;
}

export function createKeybindingDispatcher(options: DispatcherOptions = {}): KeybindingDispatcher {
  const registry = options.registry ?? keybindingsRegistry;
  const isModalOpen = options.isModalOpen ?? isModalOpenTarget;
  const isEditable = options.isEditableTarget ?? isEditableTarget;

  function buildHandler(state: KeybindingsState): EventListener {
    const map: KeybindingsMap = {};
    const claimed = new Set<string>();
    const scopes = [...state.scopeStack].reverse();
    scopes.push(GLOBAL_SCOPE);
    for (const scope of scopes) {
      for (const binding of state.bindings) {
        if (binding.scope !== scope) continue;
        const chordKey = serializeChord(binding.chord);
        if (claimed.has(chordKey)) continue;
        claimed.add(chordKey);
        map[chordKey] = (event) => {
          const real = realEvent ?? event;
          if (binding.ignoreIfDefaultPrevented && real.defaultPrevented) return;
          if (!binding.allowInModal && isModalOpen(real)) return;
          if (!binding.allowInEditable && isEditable(real.target)) return;
          const handlers = registry.getState().actions.get(binding.actionId);
          if (!handlers || handlers.length === 0) return;
          // Registration order, until one handler accepts (returns
          // true/undefined); a handler returning false declines and the next
          // is tried - the multi-instance equivalent of the per-component
          // listeners this module replaced (one SelectionQuote per session
          // pane; only the instance holding a selection acts). An event
          // every handler declines is not preventDefault'd.
          for (const run of handlers) {
            if (run(real) === false) continue;
            real.preventDefault();
            return;
          }
        };
      }
    }
    // ignore: () => false - the precedence layers above replace tinykeys'
    // default ignore wholesale; the default would suppress EVERY binding from
    // an editable target, which several shell chords explicitly allow.
    return createKeybindingsHandler(map, { ignore: () => false });
  }

  let current = buildHandler(registry.getState());
  const unsubscribe = registry.subscribe((state) => {
    current = buildHandler(state);
  });

  // The binding handler tinykeys invokes receives whatever event object the
  // handler was called with; when matching ran against a code-fallback view
  // (see handleKeyDown), the REAL event is what's run through the binding.
  // Single-threaded dispatch makes the stash safe: current() runs
  // synchronously to completion inside the try.
  let realEvent: KeyboardEvent | null = null;

  function handleKeyDown(event: KeyboardEvent): void {
    if (event.isComposing || event.keyCode === 229) return;
    // tinykeys' isKeyboardEvent gate drops any event with an empty `code`.
    // Real browsers always set one, but synthetic keydowns (fireEvent-style
    // tests, and any app code dispatching a bare KeyboardEvent) routinely
    // omit it - and the pre-dispatcher listeners this module replaced never
    // looked at `code`. Match against a view whose code falls back to the
    // key: matching consults code only as a fallback for string keys
    // (already case-insensitively matched on `key`) and as a second target
    // for regex keys (where code===key can never add a match the key itself
    // didn't already produce), so the fallback is semantically invisible.
    const view =
      event.code === "" && event.key !== ""
        ? new KeyboardEvent("keydown", {
            key: event.key,
            code: event.key,
            ctrlKey: event.ctrlKey,
            altKey: event.altKey,
            shiftKey: event.shiftKey,
            metaKey: event.metaKey,
          })
        : event;
    realEvent = event;
    try {
      current(view);
    } finally {
      realEvent = null;
    }
  }

  return {
    handleKeyDown,
    attach(target) {
      const listener = (event: Event) => {
        if (event instanceof KeyboardEvent) handleKeyDown(event);
      };
      target.addEventListener("keydown", listener);
      return () => target.removeEventListener("keydown", listener);
    },
    dispose() {
      unsubscribe();
    },
  };
}
