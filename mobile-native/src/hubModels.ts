import type { ModelCatalog } from "../../cmd/evener-hub/frontend/src/widgets/modelCatalog/types";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface State {
  catalog: ModelCatalog | null;
  loading: boolean;
  error: string | null;
}
/** The hub's launch catalog, independent of a running session. */
export class HubModels {
  private state: State = { catalog: null, loading: false, error: null };
  private version = 0;
  private disposed = false;
  private listeners = new Set<() => void>();
  constructor(private client: ConversationClientLike | null) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<State>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  refresh = async () => {
    if (this.disposed || !this.client) return;
    const version = ++this.version;
    this.publish({ loading: true, error: null });
    try {
      const result = await this.client.request("model/list", {});
      if (this.disposed || version !== this.version) return;
      const entries = (data: typeof result.data | undefined) =>
        (data ?? []).map((entry) => ({
          ...entry,
          displayName: entry.displayName || entry.model,
        }));
      this.publish({
        catalog: {
          models: entries(result.data),
          recent: entries(result.recent),
          diagnostics: result.diagnostics ?? [],
        },
      });
    } catch {
      if (version === this.version)
        this.publish({
          error: "Could not load this hub's models. Try again when connected.",
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
