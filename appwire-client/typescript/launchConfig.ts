// launchConfig.ts is the launch-config wire gateway every settings surface
// built on the schema-driven launch-config engine (launchSchema rendered by
// each app's form) and the in-repo trust flow calls through:
// evener/launch/{schema,getLayer,setLayer,resolve,trustRepo} and
// evener/path/validate. createLaunchConfigStore is a factory - each app builds
// the one instance it wires up, and tests build their own - and the store is
// the getState/setState/subscribe triple plus getInitialState, the shape
// React's useSyncExternalStore (and zustand's useStore over it) binds to
// without the package depending on either.
//
// schema() is the one cached read here: the option schema is server-global,
// identical for every cwd/layer, so a settings user who visits several launch
// pages in one session pays for the RPC once per store. The cache is per
// instance - two stores over two hubs never see each other's schema.
// getLayer/resolve/setLayer/trustRepo/validatePath are deliberately UNCACHED:
// each is read-your-writes sensitive (a getLayer right after a setLayer must
// see the just-saved value) and varies by cwd+layer, so there is no single
// value to memoize the way schema's is.
//
// LaunchSettings is the per-hub/cwd/layer editor the native launch settings
// screen drives over that gateway: a draft with dirty tracking, a preflight
// baseline check before every whole-layer write, and the changed-elsewhere
// gate an evener/launch/updated notification raises on a dirty draft.

import type { AppwireClient } from "./client";
import type { LaunchConfigLayerName } from "./launchSchema";
import { equalJSON } from "./state/navigation/immutable";
import type {
  LaunchConfigLayer,
  LaunchConfigResolved,
  LaunchOption,
  LaunchOptionSchemaResponse,
  PathValidateResponse,
} from "./types.gen";

/** The two members of the client this module calls; AppwireClientLike satisfies it. */
export type LaunchConfigClient = Pick<AppwireClient, "request" | "onNotification">;

export interface LaunchConfigStoreState {
  schema(): Promise<LaunchOptionSchemaResponse>;
  getLayer(cwd: string, layer: LaunchConfigLayerName): Promise<LaunchConfigLayer>;
  setLayer(cwd: string, layer: LaunchConfigLayerName, config: LaunchConfigLayer): Promise<LaunchConfigResolved>;
  resolve(cwd: string, launchOverrides?: LaunchConfigLayer): Promise<LaunchConfigResolved>;
  trustRepo(cwd: string, hash: string): Promise<LaunchConfigResolved>;
  validatePath(path: string, kind?: string): Promise<PathValidateResponse>;
  /** Drops the cached schema (and any in-flight fetch) so the next schema() refetches. */
  invalidateSchema(): void;
}

/** Runs after every state change with the new state and the one it replaced
 * - the listener shape zustand's useStore subscribes with. */
export type LaunchConfigListener = (state: LaunchConfigStoreState, previous: LaunchConfigStoreState) => void;

export interface LaunchConfigStore {
  getState(): LaunchConfigStoreState;
  /** The state the store was created with: the snapshot a view binding
   * (React's useSyncExternalStore, zustand's useStore) reads before its first
   * subscription. */
  getInitialState(): LaunchConfigStoreState;
  /** Shallow-merges the partial (or the updater's result) into the state and
   * notifies every subscriber, even when nothing changed. */
  setState(
    partial: Partial<LaunchConfigStoreState> | ((state: LaunchConfigStoreState) => Partial<LaunchConfigStoreState>),
  ): void;
  /** Returns the unsubscribe function. */
  subscribe(listener: LaunchConfigListener): () => void;
}

/** Builds a gateway over `client`, with its own schema cache. */
export function createLaunchConfigStore(client: LaunchConfigClient): LaunchConfigStore {
  const listeners = new Set<LaunchConfigListener>();
  // The schema cache and its in-flight tracking are closure-private, not part
  // of the store's reactive state: nothing renders off "is the schema cached
  // yet", so a fetch completing must not notify subscribers.
  let schemaCache: LaunchOptionSchemaResponse | null = null;
  let schemaInflight: Promise<LaunchOptionSchemaResponse> | null = null;
  let state: LaunchConfigStoreState = {
    async schema() {
      if (schemaCache) return schemaCache;
      if (!schemaInflight) {
        const fetching = client
          .request("evener/launch/schema", {})
          .then((resp) => {
            if (schemaInflight === fetching) schemaCache = resp;
            return resp;
          })
          .finally(() => {
            if (schemaInflight === fetching) schemaInflight = null;
          });
        schemaInflight = fetching;
      }
      return schemaInflight;
    },
    // async so a client that throws (the web's, before connect()) rejects
    // the returned promise instead of throwing at the call site.
    getLayer: async (cwd, layer) => client.request("evener/launch/getLayer", { cwd, layer }),
    setLayer: async (cwd, layer, config) => client.request("evener/launch/setLayer", { cwd, layer, config }),
    resolve: async (cwd, launchOverrides) => client.request("evener/launch/resolve", { cwd, launchOverrides }),
    trustRepo: async (cwd, hash) => client.request("evener/launch/trustRepo", { cwd, hash }),
    validatePath: async (path, kind) => client.request("evener/path/validate", { path, kind }),
    invalidateSchema() {
      schemaCache = null;
      schemaInflight = null;
    },
  };
  const initialState = state;
  return {
    getState: () => state,
    getInitialState: () => initialState,
    setState(partial) {
      const previous = state;
      state = { ...state, ...(typeof partial === "function" ? partial(state) : partial) };
      for (const listener of listeners) listener(state, previous);
    },
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  };
}

export interface LaunchSettingsState {
  options: LaunchOption[] | null;
  current: LaunchConfigLayer | null;
  draft: LaunchConfigLayer | null;
  resolved: LaunchConfigResolved | null;
  dirty: boolean;
  loading: boolean;
  saving: boolean;
  changedElsewhere: boolean;
  error: string | null;
  resolveError: string | null;
}

const CHANGED_ELSEWHERE = "Launch settings changed elsewhere. Reload before saving.";

/** One hub/cwd/layer editor. Whole-layer writes require a fresh baseline check. */
export class LaunchSettings {
  private state: LaunchSettingsState = {
    options: null,
    current: null,
    draft: null,
    resolved: null,
    dirty: false,
    loading: false,
    saving: false,
    changedElsewhere: false,
    error: null,
    resolveError: null,
  };
  private client: LaunchConfigClient | null;
  // The gateway bound to the current client, rebuilt on setConnection.
  private rpc: LaunchConfigStoreState | null;
  private version = 0;
  private disposed = false;
  private unsubscribe?: () => void;
  private listeners = new Set<() => void>();
  constructor(
    client: LaunchConfigClient | null,
    readonly cwd: string,
    readonly layer: LaunchConfigLayerName,
  ) {
    this.client = client;
    this.rpc = client && createLaunchConfigStore(client).getState();
  }
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<LaunchSettingsState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  start() {
    if (this.disposed || this.unsubscribe || !this.client) return;
    this.unsubscribe = this.client.onNotification((event) => {
      if (event.method !== "evener/launch/updated") return;
      // A global update can change a project's inherited values too.
      if (event.params.layer !== "global" && event.params.cwd !== this.cwd) return;
      if (this.state.saving) return; // The post-write read reconciles notifications.
      if (this.state.dirty) {
        this.version += 1;
        this.publish({ changedElsewhere: true, loading: false });
      } else void this.refresh();
    });
  }
  /** Rebind only within the same hub. A different hub needs a different editor. */
  async setConnection(client: LaunchConfigClient | null) {
    if (this.disposed || this.client === client) return;
    this.version += 1;
    this.unsubscribe?.();
    this.unsubscribe = undefined;
    this.client = client;
    this.rpc = client && createLaunchConfigStore(client).getState();
    this.publish({ loading: false, saving: false });
    if (client) {
      this.start();
      await this.load(false, true);
    }
  }
  refresh = (discardDraft = false) => this.load(discardDraft, false);
  private load = async (discardDraft: boolean, reconcile: boolean) => {
    if (
      this.disposed ||
      !this.client ||
      !this.rpc ||
      this.state.saving ||
      (this.state.dirty && !discardDraft && !reconcile)
    )
      return;
    const { client, rpc } = this;
    const version = ++this.version;
    this.publish({ loading: true, error: null });
    try {
      // The schema is fetched directly rather than through the gateway's cache:
      // every load is a fresh, independent read, so a hung request from an
      // obsolete refresh can never hold up the one that supersedes it.
      const [schema, current, resolved] = await Promise.all([
        client.request("evener/launch/schema", {}),
        rpc.getLayer(this.cwd, this.layer),
        rpc.resolve(this.cwd).catch(() => null),
      ]);
      if (version !== this.version || this.disposed) return;
      const preserve = reconcile && this.state.dirty && this.state.draft;
      const draft = preserve ? this.state.draft : current;
      const dirty = !equalJSON(draft, current);
      this.publish({
        options: schema.options,
        current,
        draft,
        resolved,
        dirty,
        changedElsewhere:
          !!preserve && dirty && (this.state.changedElsewhere || !equalJSON(current, this.state.current)),
        resolveError: resolved ? null : "Effective launch values could not be loaded.",
      });
    } catch {
      if (version === this.version)
        this.publish({
          error: "Could not load launch settings. Try again when connected.",
        });
    } finally {
      if (version === this.version) this.publish({ loading: false });
    }
  };
  edit<K extends keyof LaunchConfigLayer>(field: K, value: LaunchConfigLayer[K]) {
    if (this.disposed || this.state.saving || this.state.loading || !this.state.draft)
      throw Error("Launch settings are not editable");
    const option = this.state.options?.find((item) => item.wireField === field);
    if (!option?.defaultableLayers?.includes(this.layer)) throw Error("Field is not editable in this layer");
    const draft = { ...this.state.draft };
    if (value === undefined) delete draft[field];
    else draft[field] = value;
    this.publish({
      draft,
      dirty: !equalJSON(draft, this.state.current),
      error: null,
    });
  }
  save = async (): Promise<boolean> => {
    if (
      this.disposed ||
      !this.rpc ||
      this.state.saving ||
      this.state.loading ||
      !this.state.dirty ||
      !this.state.current ||
      !this.state.draft
    )
      return false;
    if (this.state.changedElsewhere) {
      this.publish({ error: CHANGED_ELSEWHERE });
      return false;
    }
    const draft = this.state.draft;
    const baseline = this.state.current;
    const rpc = this.rpc;
    const version = ++this.version;
    const active = () => !this.disposed && version === this.version;
    this.publish({ saving: true, error: null });
    let sent = false;
    let confirmed = false;
    try {
      const latest = await rpc.getLayer(this.cwd, this.layer);
      if (!active()) return false;
      if (!equalJSON(latest, baseline)) {
        this.publish({ changedElsewhere: true, error: CHANGED_ELSEWHERE });
        return false;
      }
      sent = true;
      const resolved = await rpc.setLayer(this.cwd, this.layer, draft);
      if (!active()) return false;
      confirmed = true;
      this.publish({ resolved, resolveError: null });
    } catch {
      if (!active()) return false;
      this.publish({
        error: sent
          ? "Could not confirm the save. Review the current layer before trying again."
          : "Could not check the current layer. Nothing was sent; try again when connected.",
      });
    } finally {
      if (sent && active()) {
        try {
          const current = await rpc.getLayer(this.cwd, this.layer);
          if (active())
            this.publish({
              current,
              dirty: !equalJSON(current, draft),
              changedElsewhere: !equalJSON(current, draft),
            });
        } catch {
          if (active())
            this.publish({
              changedElsewhere: true,
              error: "Could not read the layer after saving. Reload to confirm its state.",
            });
          confirmed = false;
        }
      }
      if (active()) this.publish({ saving: false });
    }
    return confirmed && active() && !this.state.changedElsewhere;
  };
  trustRepository = async (hash: string): Promise<boolean> => {
    const repo = this.state.resolved?.repo;
    if (
      this.disposed ||
      !this.rpc ||
      this.layer !== "project" ||
      this.state.dirty ||
      this.state.loading ||
      this.state.saving ||
      !hash ||
      repo?.hash !== hash ||
      !["untrusted", "changed", "rejected"].includes(repo.trust)
    )
      return false;
    const rpc = this.rpc;
    const version = ++this.version;
    const active = () => !this.disposed && version === this.version;
    this.publish({ saving: true, error: null });
    let confirmed = false;
    try {
      try {
        await rpc.trustRepo(this.cwd, hash);
      } catch {
        // Resolve independently even if the mutation reply was lost.
      }
      if (!active()) return false;
      const resolved = await rpc.resolve(this.cwd);
      if (!active()) return false;
      confirmed = resolved.repo?.hash === hash && resolved.repo.trust === "trusted";
      this.publish({
        resolved,
        resolveError: null,
        error: confirmed ? null : "Repository trust was not confirmed. Review the current file before trying again.",
      });
    } catch {
      if (active())
        this.publish({
          resolved: null,
          error: "Could not confirm repository trust. Reload before reviewing the file again.",
        });
    } finally {
      if (active()) this.publish({ saving: false });
    }
    return active() && confirmed;
  };
  dispose() {
    this.disposed = true;
    this.version += 1;
    this.unsubscribe?.();
    this.listeners.clear();
  }
}
