import type { SettingsOverviewResponse } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface OverviewState {
  data: SettingsOverviewResponse | null;
  loading: boolean;
  error: string | null;
}

/** Runtime information for one connected hub; failures retain the last read. */
export class HubOverview {
  private state: OverviewState = { data: null, loading: false, error: null };
  private listeners = new Set<() => void>();
  private version = 0;
  private disposed = false;
  constructor(private client: ConversationClientLike) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<OverviewState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  refresh = async () => {
    if (this.disposed) return;
    const version = ++this.version;
    this.publish({ loading: true, error: null });
    try {
      const data = await this.client.request("evener/settings/overview", {});
      if (version === this.version) {
        // Go omitempty encodes empty slices and zero counters as absent fields.
        const normalized = {
          ...data,
          agents: data.agents ?? [],
          codexLaunches: data.codexLaunches ?? [],
        };
        if (data.mcpDiscovered)
          normalized.mcpDiscovered = {
            ...data.mcpDiscovered,
            servers: data.mcpDiscovered.servers ?? [],
          };
        if (data.hub?.pastIndex)
          normalized.hub = {
            ...data.hub,
            pastIndex: {
              ...data.hub.pastIndex,
              count: data.hub.pastIndex.count ?? 0,
              perPage: data.hub.pastIndex.perPage ?? 0,
            },
          };
        this.publish({ data: normalized });
      }
    } catch {
      if (version === this.version)
        this.publish({
          error: "Could not refresh hub information. Try again when connected.",
        });
    } finally {
      if (version === this.version) this.publish({ loading: false });
    }
  };
  dispose() {
    this.disposed = true;
    this.version += 1;
    this.listeners.clear();
  }
}
