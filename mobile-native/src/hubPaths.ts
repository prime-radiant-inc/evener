import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface PathState {
  paths: string[] | null;
  loading: boolean;
  error: string | null;
}

// The hub's reply is untrusted wire data: the field maps over it and calls
// basename on every entry, so anything but a list of strings is a crash
// downstream rather than a bad suggestion. Validated here the way the other
// hub boundaries validate theirs (providerSignIn, nativePreferences), which
// each keep a local predicate rather than sharing one.
function isPathList(value: unknown): value is string[] {
  return (
    Array.isArray(value) && value.every((entry) => typeof entry === "string")
  );
}

/** Path suggestions belong to one field and one connected hub. */
export class HubPaths {
  private state: PathState = { paths: null, loading: false, error: null };
  private listeners = new Set<() => void>();
  private version = 0;
  private disposed = false;
  constructor(
    private client: ConversationClientLike,
    private includeFiles = false,
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(state: PathState) {
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
        includeFiles: this.includeFiles,
        limit: 100,
      });
      if (!isPathList(result.data)) throw new Error("Invalid path response");
      if (version === this.version)
        this.publish({ paths: result.data, loading: false, error: null });
    } catch {
      if (version === this.version)
        this.publish({
          paths: null,
          loading: false,
          error:
            "Could not load paths from this hub. Try again or enter the path manually.",
        });
    }
  };
  dispose() {
    this.disposed = true;
    this.version += 1;
    this.listeners.clear();
  }
}
