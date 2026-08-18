// launchConfig.ts is the thin wire-truth gateway for every settings surface
// built on the schema-driven launch-config engine (launchShared/) plus the
// hand-rolled in-repo trust flow: serf/launch/{schema,getLayer,setLayer,
// resolve,trustRepo} and serf/path/validate. Follows stores/threads.ts's own
// requireClient()-via-connectionStore pattern (no connect() of its own).
//
// schema() is the one cached read here: the option schema is server-global,
// identical for every cwd/layer, and both consumers of this store
// (launchServer.tsx, project.tsx) fetch it once per pane mount - caching
// means a settings-pane user who visits both pages in one session only pays
// for the RPC once. getLayer/resolve/setLayer/trustRepo/validatePath are
// deliberately UNCACHED: each is read-your-writes sensitive (a getLayer
// right after a setLayer must see the just-saved value) and varies by
// cwd+layer, so there is no single value to memoize the way schema's is.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type {
  LaunchConfigLayer,
  LaunchConfigResolved,
  LaunchOptionSchemaResponse,
  PathValidateResponse,
} from "../protocol/types.gen";
import { connectionStore } from "./connection";

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error(
      "launchConfig store: no client connected; call useConnectionStore.getState().connect(client) first",
    );
  }
  return client;
}

export type LaunchConfigLayerName = "global" | "project";

export interface LaunchConfigStoreState {
  schema(): Promise<LaunchOptionSchemaResponse>;
  getLayer(cwd: string, layer: LaunchConfigLayerName): Promise<LaunchConfigLayer>;
  setLayer(cwd: string, layer: LaunchConfigLayerName, config: LaunchConfigLayer): Promise<LaunchConfigResolved>;
  resolve(cwd: string, launchOverrides?: LaunchConfigLayer): Promise<LaunchConfigResolved>;
  trustRepo(cwd: string, hash: string): Promise<LaunchConfigResolved>;
  validatePath(path: string, kind?: string): Promise<PathValidateResponse>;
}

// Module-private schema cache/in-flight tracking - not part of the store's
// reactive state (nothing renders off "is the schema cached yet"), mirroring
// threads.ts's own module-private inflightHydrates map.
let schemaCache: LaunchOptionSchemaResponse | null = null;
let schemaInflight: Promise<LaunchOptionSchemaResponse> | null = null;

export const launchConfigStore = createStore<LaunchConfigStoreState>(() => ({
  async schema() {
    if (schemaCache) return schemaCache;
    if (!schemaInflight) {
      const client = requireClient();
      schemaInflight = client
        .request("serf/launch/schema", {})
        .then((resp) => {
          schemaCache = resp;
          return resp;
        })
        .finally(() => {
          schemaInflight = null;
        });
    }
    return schemaInflight;
  },

  async getLayer(cwd, layer) {
    const client = requireClient();
    return client.request("serf/launch/getLayer", { cwd, layer });
  },

  async setLayer(cwd, layer, config) {
    const client = requireClient();
    return client.request("serf/launch/setLayer", { cwd, layer, config });
  },

  async resolve(cwd, launchOverrides) {
    const client = requireClient();
    return client.request("serf/launch/resolve", { cwd, launchOverrides });
  },

  async trustRepo(cwd, hash) {
    const client = requireClient();
    return client.request("serf/launch/trustRepo", { cwd, hash });
  },

  async validatePath(path, kind) {
    const client = requireClient();
    return client.request("serf/path/validate", { path, kind });
  },
}));

export function useLaunchConfigStore(): LaunchConfigStoreState;
export function useLaunchConfigStore<T>(selector: (state: LaunchConfigStoreState) => T): T;
export function useLaunchConfigStore<T>(selector?: (state: LaunchConfigStoreState) => T): T | LaunchConfigStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(launchConfigStore, selector) : useStore(launchConfigStore);
}

// resetLaunchConfigStoreForTests clears the module-private schema cache
// between tests - this store's own reactive state has nothing to reset (it
// holds only methods), but the cache is a singleton that would otherwise
// leak a fixture from one test into the next. No production code should
// ever call this.
export function resetLaunchConfigStoreForTests(): void {
  schemaCache = null;
  schemaInflight = null;
}
