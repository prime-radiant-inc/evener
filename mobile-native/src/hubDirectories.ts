import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface DirectoryState {
  paths: string[] | null;
  loading: boolean;
  error: string | null;
}

/** Directory suggestions belong to one field and one connected hub. */
export class HubDirectories {
  private state: DirectoryState = { paths: null, loading: false, error: null };
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
  private publish(state: DirectoryState) {
    if (this.disposed) return;
    this.state = state;
    for (const listener of this.listeners) listener();
  }
  clear = () => {
    this.version += 1;
    this.publish({ paths: null, loading: false, error: null });
  };
  load = async (prefix: string) => {
    if (this.disposed) return;
    const version = ++this.version;
    this.publish({ paths: null, loading: true, error: null });
    try {
      const result = await this.client.request("evener/paths/complete", {
        prefix,
        includeFiles: false,
        limit: 100,
      });
      if (version === this.version)
        this.publish({ paths: result.data, loading: false, error: null });
    } catch {
      if (version === this.version)
        this.publish({
          paths: null,
          loading: false,
          error:
            "Could not load directories from this hub. Try again or enter the path manually.",
        });
    }
  };
  dispose() {
    this.disposed = true;
    this.version += 1;
    this.listeners.clear();
  }
}
