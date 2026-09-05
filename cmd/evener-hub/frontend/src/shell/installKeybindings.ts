// The one app-startup wiring point between the React-free keybindings module
// (src/keybindings/) and the live app: registers the default binding map and
// attaches the single window-level dispatcher, with the modal predicate
// composed from the real stores - paletteStore.open (the palette is
// CommandPalette's own overlay, not an [aria-modal] element, so the DOM check
// alone can't see it) OR an [aria-modal="true"] ancestor of the event target
// (every OverlayPanel-based Dialog/Sheet). This is exactly the old AppShell
// blockedByOpenModal check, kept blanket - with per-binding allowInModal
// exemptions living on the bindings themselves (keybindings/defaults.ts),
// the predicate never sniffs which chord was pressed.
//
// Idempotent per window: AppShell installs it at mount, and every component
// that registers a chord action (RailHost, Settings, SelectionQuote) calls it
// too so the chord also works when the component is mounted WITHOUT the shell
// - those components' own unit tests render them standalone, and a chord that
// silently did nothing there would be worse than this belt-and-braces call.
// Per-window lifetime by design: the dispatcher and the (actionless on their
// own) default bindings stay attached until the page unloads; actions and
// scopes come and go with their components. Vitest file isolation gives each
// test file a fresh module registry, so the singleton never crosses files.

import { registerDefaultBindings } from "../keybindings/defaults";
import { createKeybindingDispatcher, isModalOpenTarget } from "../keybindings/dispatcher";
import { keybindingsRegistry } from "../keybindings/registry";
import { installCharacterKeyTriggerReconcile } from "./cheatsheet/cheatsheetController";
import { paletteStore } from "./palette/paletteController";

let installed = false;

export function installKeybindings(): void {
  if (installed) return;
  installed = true;
  registerDefaultBindings(keybindingsRegistry);
  // The "?" character-key trigger's pref-conditional registration (the WCAG
  // 2.1.4 turn-off). Installed here, not by the overlay component, so the
  // invariant holds on mobile too - where the overlay never mounts and the
  // binding is inert for lack of an action - keeping the settings section's
  // customized-marker comparison truthful there as well.
  installCharacterKeyTriggerReconcile();
  const dispatcher = createKeybindingDispatcher({
    isModalOpen: (event) => paletteStore.getState().open || isModalOpenTarget(event),
  });
  dispatcher.attach(window);
}
