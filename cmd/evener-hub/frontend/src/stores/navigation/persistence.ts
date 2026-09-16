// The browser's binding of the navigation store's persistence port: the
// rail's one localStorage blob, read at store creation and written on every
// expansion change. railExpansion.ts owns the key, the cap and the
// swallow-every-failure rules; this file is only the two-method shape the
// package's store takes.
import type { NavigationPersistence } from "@evener/appwire-client/state/navigation";
import { loadExpansion, saveExpansion } from "../../shell/rail/railExpansion";

export const railExpansionPersistence: NavigationPersistence = {
  readExpansion: loadExpansion,
  writeExpansion: saveExpansion,
};
