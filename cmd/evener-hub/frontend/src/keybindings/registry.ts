// The keybinding registry: a vanilla zustand store (same pattern as
// shell/palette/paletteController.ts) holding the action registry (action id
// -> run function), the binding entries, and the scope stack the dispatcher
// evaluates top-down. Pure logic - no DOM, no React.

import { createStore, type StoreApi } from "zustand/vanilla";
import { type KeySequence, parseChord, serializeChord } from "./chord";

/** The implicit bottom of every scope stack: bindings with no `scope` land here. */
export const GLOBAL_SCOPE = "global";

/** Structured when-clause, carried for Phase 2b. Task 1 stores it verbatim and
 * never evaluates it: scope-stack membership is the only gating the dispatcher
 * applies. */
export type WhenClause = Readonly<Record<string, string | boolean>>;

/** An action handler. Returning false DECLINES the event - the dispatcher
 * tries the action's next handler (multi-instance components like
 * SelectionQuote register one handler per mounted instance, and only the
 * instance holding state that can act accepts). Returning true or undefined
 * reports the event handled and stops iteration. */
// biome-ignore lint/suspicious/noConfusingVoidType: "false declines, anything else (including a void fall-through) accepts" IS the contract; boolean|undefined would reject the existing void-bodied handlers
export type ActionRunner = (event: KeyboardEvent) => boolean | void;

export interface Binding {
  id: string;
  actionId: string;
  chord: KeySequence;
  scope: string;
  when?: WhenClause;
  allowInEditable: boolean;
  /** When true, an open modal does NOT suppress this binding. Default false.
   * Three of the shell's pre-dispatcher listeners had no modal guard at all
   * (AppShell's deliberate ⌘K exemption, RailHost's ⌘B, SelectionQuote's ⌘'),
   * so modal suppression is per-binding rather than a blanket layer. */
  allowInModal: boolean;
  /** When true (the default), a keydown another handler already claimed
   * (event.defaultPrevented) suppresses this binding. RailHost's ⌘B listener
   * has no such check, so rail.toggle sets this to false. */
  ignoreIfDefaultPrevented: boolean;
}

export interface BindingInput {
  id: string;
  actionId: string;
  /** A tinykeys keybinding string ("$mod+K") or an already-parsed sequence. */
  chord: string | KeySequence;
  /** Defaults to the global scope. */
  scope?: string;
  when?: WhenClause;
  /** Defaults to false: bindings are suppressed while an editable target has focus. */
  allowInEditable?: boolean;
  /** Defaults to false: bindings are suppressed while a modal is open. */
  allowInModal?: boolean;
  /** Defaults to true. */
  ignoreIfDefaultPrevented?: boolean;
}

export interface KeybindingsState {
  /** Action id -> its handlers, in registration order. */
  actions: ReadonlyMap<string, readonly ActionRunner[]>;
  bindings: readonly Binding[];
  /** Bottom-to-top; the dispatcher evaluates it top-down before the global scope. */
  scopeStack: readonly string[];
  /** Appends a handler for the action; the returned disposer removes ONLY
   * this registration (by identity), never another instance's handler for
   * the same action, and is idempotent. */
  registerAction(id: string, run: ActionRunner): () => void;
  /**
   * Throws on a duplicate binding id, or on a chord conflict: the same chord
   * already bound in the SAME scope. The same chord in a DIFFERENT scope is
   * not a conflict - scope-stack order shadows it deterministically.
   */
  registerBinding(input: BindingInput): Binding;
  unregisterBinding(id: string): boolean;
  /** Returns an idempotent disposer that pops this entry. */
  pushScope(scope: string): () => void;
  /** Removes the topmost matching scope; false when the scope is not on the stack. */
  popScope(scope: string): boolean;
}

export type KeybindingsRegistry = StoreApi<KeybindingsState>;

export function createKeybindingsRegistry(): KeybindingsRegistry {
  return createStore<KeybindingsState>()((set, get) => ({
    actions: new Map(),
    bindings: [],
    scopeStack: [],

    registerAction(id, run) {
      set((s) => {
        const actions = new Map(s.actions);
        actions.set(id, [...(actions.get(id) ?? []), run]);
        return { actions };
      });
      let active = true;
      return () => {
        if (!active) return;
        active = false;
        set((s) => {
          const list = s.actions.get(id);
          if (!list) return {};
          const index = list.indexOf(run);
          if (index === -1) return {};
          const actions = new Map(s.actions);
          const next = [...list.slice(0, index), ...list.slice(index + 1)];
          if (next.length === 0) actions.delete(id);
          else actions.set(id, next);
          return { actions };
        });
      };
    },

    registerBinding(input) {
      const chord = typeof input.chord === "string" ? parseChord(input.chord) : input.chord;
      const binding: Binding = {
        id: input.id,
        actionId: input.actionId,
        chord,
        scope: input.scope ?? GLOBAL_SCOPE,
        ...(input.when === undefined ? {} : { when: input.when }),
        allowInEditable: input.allowInEditable ?? false,
        allowInModal: input.allowInModal ?? false,
        ignoreIfDefaultPrevented: input.ignoreIfDefaultPrevented ?? true,
      };
      const serialized = serializeChord(chord);
      for (const existing of get().bindings) {
        if (existing.id === binding.id) {
          throw new Error(`keybinding id "${binding.id}" is already registered`);
        }
        if (existing.scope === binding.scope && serializeChord(existing.chord) === serialized) {
          throw new Error(
            `keybinding conflict: "${serialized}" in scope "${binding.scope}" is already bound by "${existing.id}"`,
          );
        }
      }
      set((s) => ({ bindings: [...s.bindings, binding] }));
      return binding;
    },

    unregisterBinding(id) {
      const found = get().bindings.some((b) => b.id === id);
      if (found) set((s) => ({ bindings: s.bindings.filter((b) => b.id !== id) }));
      return found;
    },

    pushScope(scope) {
      set((s) => ({ scopeStack: [...s.scopeStack, scope] }));
      let active = true;
      return () => {
        if (!active) return;
        active = false;
        get().popScope(scope);
      };
    },

    popScope(scope) {
      const stack = get().scopeStack;
      const index = stack.lastIndexOf(scope);
      if (index === -1) return false;
      set({ scopeStack: [...stack.slice(0, index), ...stack.slice(index + 1)] });
      return true;
    },
  }));
}

/** The app-wide registry. Task 2 wires it to AppShell; tests build their own
 * with createKeybindingsRegistry(). */
export const keybindingsRegistry = createKeybindingsRegistry();
