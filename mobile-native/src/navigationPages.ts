import type { NavigationReadParams } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface PageState<T> {
  loaded: boolean;
  rows: T[];
  remaining: number;
  loading: boolean;
  error: string | null;
  stale: boolean;
}
/** Keep continuation within one logical navigation resource and revision. */
export class NavigationPages<T> {
  private state: PageState<T> = {
    loaded: false,
    rows: [],
    remaining: 0,
    loading: false,
    error: null,
    stale: false,
  };
  private listeners = new Set<() => void>();
  private request = 0;
  private offset = 0;
  private version: string | null = null;
  constructor(
    private client: ConversationClientLike,
    private params: NavigationReadParams,
    private field: "projects" | "sessions",
    private key: (row: T) => string,
    private limit = 50,
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(state: Partial<PageState<T>>) {
    this.state = { ...this.state, ...state };
    for (const listener of this.listeners) listener();
  }
  cancel() {
    this.request += 1;
    this.publish({ loading: false });
  }
  refresh() {
    return this.load(true);
  }
  more() {
    return this.load(false);
  }
  private async load(reset: boolean) {
    if (
      !reset &&
      (this.state.loading || this.state.stale || !this.state.remaining)
    )
      return;
    const request = ++this.request;
    this.publish({ loading: true, error: null });
    try {
      const response = await this.client.request("evener/navigation/read", {
        ...this.params,
        offset: reset ? 0 : this.offset,
        limit: this.limit,
      });
      if (request !== this.request) return;
      if (
        response.status !== "ok" ||
        !response.data ||
        typeof response.data !== "object"
      )
        throw new Error(
          "Could not read this navigation page. Refresh to try again.",
        );
      const data = response.data as Record<string, unknown>;
      const raw = data[this.field];
      if (
        !Array.isArray(raw) ||
        !Number.isSafeInteger(data.remaining) ||
        Number(data.remaining) < 0 ||
        (!raw.length && Number(data.remaining) > 0)
      )
        throw new Error(
          "The hub returned an invalid navigation page. Refresh to try again.",
        );
      const version = JSON.stringify([
        response.generationId,
        response.revision,
      ]);
      if (!reset && this.version !== version) {
        this.publish({
          loading: false,
          stale: true,
          error:
            "This list changed while you were browsing. Refresh to continue.",
        });
        return;
      }
      const unique = new Map(
        (reset ? [] : this.state.rows).map((row) => [this.key(row), row]),
      );
      for (const row of raw) {
        const key = this.key(row as T);
        if (typeof key !== "string" || !key)
          throw new Error(
            "The hub returned an invalid destination. Refresh to try again.",
          );
        unique.set(key, row as T);
      }
      this.offset = (reset ? 0 : this.offset) + raw.length;
      this.version = version;
      this.publish({
        loaded: true,
        rows: [...unique.values()],
        remaining: Number(data.remaining),
        loading: false,
        stale: false,
      });
    } catch (cause) {
      if (request !== this.request) return;
      this.publish({
        loading: false,
        error:
          cause instanceof Error
            ? cause.message
            : "Could not load this page. Try again.",
      });
    }
  }
}
