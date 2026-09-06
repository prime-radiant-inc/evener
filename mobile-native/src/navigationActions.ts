import type {
  ArchiveParams,
  NavigationMutation,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

export class NavigationActions {
  private state = { pending: false, error: null as string | null };
  private listeners = new Set<() => void>();
  private disposed = false;
  constructor(
    private client: ConversationClientLike,
    private refresh: (receipt: NavigationMutation) => Promise<void>,
    private current: () => boolean,
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  dispose() {
    this.disposed = true;
    this.listeners.clear();
  }
  private publish(state: typeof this.state) {
    this.state = state;
    for (const listener of this.listeners) listener();
  }
  archive(target: Omit<ArchiveParams, "archived">, archived: boolean) {
    return this.run(() =>
      this.client.request("evener/archive/set", { ...target, archived }),
    );
  }
  favorite(id: string, favorited: boolean) {
    return this.run(() =>
      this.client.request("evener/favorite/set", {
        kind: "project",
        id,
        favorited,
      }),
    );
  }
  private async run(
    request: () => Promise<{ ok: boolean; navigation: NavigationMutation }>,
  ) {
    if (this.disposed || !this.current() || this.state.pending) return;
    this.publish({ pending: true, error: null });
    let accepted = false;
    try {
      const response = await request();
      if (this.disposed) return;
      if (!this.current()) throw new Error("Connection changed.");
      if (!response.ok) throw new Error("The hub did not accept this change.");
      accepted = true;
      await this.refresh(response.navigation);
      if (this.disposed) return;
      this.publish({ pending: false, error: null });
    } catch (cause) {
      if (this.disposed) return;
      this.publish({
        pending: false,
        error:
          accepted && cause instanceof Error
            ? cause.message
            : "Could not confirm the change. Refresh the list before trying again; it may have been applied.",
      });
    }
  }
}
