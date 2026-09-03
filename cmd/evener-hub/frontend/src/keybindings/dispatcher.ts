// The single window-level keydown dispatcher. Matching is tinykeys
// (createKeybindingsHandler over the active scope set); precedence, in order:
//
//   1. IME composition keydowns (event.isComposing / keyCode 229) are ignored.
//   2. Per-binding editable-target policy (allowInEditable, default false).
//   3. An open modal suppresses every binding: [aria-modal="true"] ancestor of
//      the event target by default, plus whatever store check the caller's
//      injected predicate adds (Task 2 wires paletteStore.open there).
//   4. The scope stack, top-down.
//   5. The global scope.
//
// The first (highest-precedence) binding for a chord claims it outright: a
// firing binding preventDefaults and stops evaluation, and a shadowed or
// policy-blocked binding never falls through to a lower-precedence twin.
//
// An event another handler already claimed (event.defaultPrevented) is
// ignored per binding: ignoreIfDefaultPrevented defaults to true, matching
// AppShell (AppShell.tsx:392), Settings (Settings.tsx:188), and
// SelectionQuote (SelectionQuote.tsx:148) - but NOT RailHost's ⌘B listener
// (RailHost.tsx:59-66), which has no defaultPrevented check and so binds with
// ignoreIfDefaultPrevented: false in defaults.ts.

import { createKeybindingsHandler, type KeybindingsMap } from "tinykeys";
import { serializeChord } from "./chord";
import { GLOBAL_SCOPE, type KeybindingsRegistry, type KeybindingsState, keybindingsRegistry } from "./registry";

export type ModalOpenPredicate = (event: KeyboardEvent) => boolean;
export type EditableTargetPredicate = (target: EventTarget | null) => boolean;

/** shell/rail/RailHost.tsx's isEditableTarget, plus the [contenteditable]
 * attribute check: isContentEditable alone already covers descendants of an
 * editable ancestor in a real browser, but jsdom does not implement the
 * property at all (it is undefined there), and the attribute form is what
 * tinykeys' own default ignore uses. Everything else doesn't count. */
export const isEditableTarget: EditableTargetPredicate = (target) => {
  if (!(target instanceof HTMLElement)) return false;
  return (
    target.isContentEditable ||
    target.closest("[contenteditable]") !== null ||
    target.tagName === "INPUT" ||
    target.tagName === "TEXTAREA" ||
    target.tagName === "SELECT"
  );
};

/** The DOM half of AppShell.tsx's blockedByOpenModal. The palette is not an
 * [aria-modal] element, so callers should compose this with a
 * paletteStore.open-style store check (Task 2 injects that predicate). */
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
          if (binding.ignoreIfDefaultPrevented && event.defaultPrevented) return;
          if (!binding.allowInEditable && isEditable(event.target)) return;
          const run = registry.getState().actions.get(binding.actionId);
          if (!run) return;
          event.preventDefault();
          run(event);
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

  function handleKeyDown(event: KeyboardEvent): void {
    if (event.isComposing || event.keyCode === 229) return;
    if (isModalOpen(event)) return;
    current(event);
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
