// The navigation store's one host dependency that is not the AppWire client:
// where rail expansion lives between visits. The store is handed a port at
// construction and never names a storage API itself, so its rules hold on a
// host whose storage is not the browser's - and a host with nowhere to keep
// expansion binds a port that keeps it in memory rather than pretending.
import { loadExpansion, saveExpansion } from "../../shell/rail/railExpansion";

/** Reads and writes the rail's per-row expand state for the navigation store. */
export interface NavigationPersistence {
  /** The map the host has kept, or an empty one when it has none. */
  readExpansion(): Map<string, boolean>;
  /** Keeps the map. Best-effort: a host that cannot store it drops it. */
  writeExpansion(expansion: ReadonlyMap<string, boolean>): void;
}

/** The browser's port: the rail's one localStorage blob, capped and
 * failure-tolerant per railExpansion.ts. */
export const railExpansionPersistence: NavigationPersistence = {
  readExpansion: loadExpansion,
  writeExpansion: saveExpansion,
};
