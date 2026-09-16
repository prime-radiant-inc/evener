// The app-wide keybinding registry: the package's factory bound to tinykeys'
// parser. Components register actions and scopes against it, the dispatcher
// listens to it, and the three binding-list UIs read it through zustand's
// useStore (the store shape is what useSyncExternalStore binds to). Tests
// build their own with createKeybindingsRegistry(parseKeybinding); Vitest
// file isolation gives each test file a fresh module registry, so this
// instance never crosses files.
import { createKeybindingsRegistry } from "@evener/appwire-client";
import { parseKeybinding } from "tinykeys";

export const keybindingsRegistry = createKeybindingsRegistry(parseKeybinding);
