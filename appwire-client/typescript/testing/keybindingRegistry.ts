// registryWithDefaults builds the registry the keybinding tests start from: the
// package's factory bound to tinykeys (the parser both apps supply) with the
// shipped default bindings registered. In-repo test support, not shipped.

import { parseKeybinding } from "tinykeys";
import { registerDefaultBindings } from "../keybindingDefaults";
import { createKeybindingsRegistry, type KeybindingsRegistry } from "../keybindingRegistry";

export function registryWithDefaults(): KeybindingsRegistry {
  const registry = createKeybindingsRegistry(parseKeybinding);
  registerDefaultBindings(registry);
  return registry;
}
