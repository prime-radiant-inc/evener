import type {
  PluginEntry,
  PluginListResponse,
  PluginRefParams,
} from "../../appwire-client/typescript/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface InstalledState {
  plugins: PluginEntry[] | null;
  loading: boolean;
  busy: boolean;
  error: string | null;
}

/** Installed plugins and writes belong to one connected hub's screen lifetime. */
export class InstalledPlugins {
  private state: InstalledState = {
    plugins: null,
    loading: false,
    busy: false,
    error: null,
  };
  private listeners = new Set<() => void>();
  private unsubscribe?: () => void;
  private disposed = false;
  private readVersion = 0;
  constructor(private client: ConversationClientLike) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<InstalledState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  start() {
    if (this.disposed || this.unsubscribe) return;
    this.unsubscribe = this.client.onNotification((event) => {
      if (event.method === "evener/plugin/updated") void this.refresh();
    });
    void this.refresh();
  }
  refresh = async (): Promise<void> => {
    if (this.disposed || this.state.busy) return;
    const version = ++this.readVersion;
    this.publish({ loading: true, error: null });
    try {
      const response = await this.client.request("evener/plugin/list", {});
      if (version === this.readVersion)
        this.publish({ plugins: response.plugins });
    } catch {
      if (version === this.readVersion)
        this.publish({
          error: "Could not load installed plugins. Try again when connected.",
        });
    } finally {
      if (version === this.readVersion) this.publish({ loading: false });
    }
  };
  private async mutate(action: () => Promise<PluginListResponse>) {
    if (this.disposed) throw new Error("Plugin screen is closed");
    if (this.state.busy) throw new Error("A plugin operation is in progress");
    this.readVersion += 1;
    this.publish({ busy: true, loading: false });
    try {
      const response = await action();
      this.publish({ plugins: response.plugins });
    } finally {
      this.publish({ busy: false });
      // A lost reply can follow a successful server write. Reconcile with a read;
      // never repeat the mutation automatically, including after hub reconnect.
      await this.refresh();
    }
  }
  install = (target: PluginRefParams) =>
    this.mutate(() => this.client.request("evener/plugin/install", target));
  upgrade = (target: PluginRefParams) =>
    this.mutate(() => this.client.request("evener/plugin/upgrade", target));
  remove = (target: PluginRefParams) =>
    this.mutate(() => this.client.request("evener/plugin/remove", target));
  enable = (target: PluginRefParams) =>
    this.mutate(() => this.client.request("evener/plugin/enable", target));
  disable = (target: PluginRefParams) =>
    this.mutate(() => this.client.request("evener/plugin/disable", target));
  setAutoUpgrade = (target: PluginRefParams, autoUpgrade: boolean) =>
    this.mutate(() =>
      this.client.request("evener/plugin/setAutoUpgrade", {
        ...target,
        autoUpgrade,
      }),
    );
  dispose() {
    this.disposed = true;
    this.readVersion += 1;
    this.unsubscribe?.();
    this.listeners.clear();
  }
}
