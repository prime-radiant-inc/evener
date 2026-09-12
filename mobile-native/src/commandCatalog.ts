import {
  mergeSlashCommands,
  type SlashMenuItem,
} from "../../cmd/evener-hub/frontend/src/panes/session/composer/slashCompletion";
import { sessionActionError } from "../../cmd/evener-hub/frontend/src/protocol/errors";
import { visibleCatalogCommands } from "../../cmd/evener-hub/frontend/src/shell/palette/catalogCommands";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";

interface CatalogState {
  items: SlashMenuItem[];
  error: string | null;
  loading: boolean;
}
export class CommandCatalog {
  private state: CatalogState = { items: [], error: null, loading: false };
  private listeners = new Set<() => void>();
  private disposed = false;
  private inFlight?: Promise<void>;
  private dirty = false;
  private unsubscribe?: () => void;
  constructor(
    private client: ConversationClientLike,
    private ref: string,
  ) {}
  getSnapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<CatalogState>) {
    if (this.disposed) return;
    this.state = { ...this.state, ...change };
    for (const listener of this.listeners) listener();
  }
  start() {
    if (this.disposed || this.unsubscribe) return;
    this.unsubscribe = this.client.onNotification((n) => {
      if (
        n.method === "evener/plugin/updated" ||
        (n.method === "evener/thread/resync" && n.params.ref === this.ref)
      )
        void this.refresh();
    });
    void this.refresh();
  }
  refresh = (): Promise<void> => {
    if (this.disposed) return Promise.resolve();
    if (this.inFlight) {
      this.dirty = true;
      return this.inFlight;
    }
    this.inFlight = this.load().finally(() => {
      this.inFlight = undefined;
    });
    return this.inFlight;
  };
  private async load() {
    this.publish({ loading: true, error: null });
    do {
      this.dirty = false;
      const [catalog, thread] = await Promise.allSettled([
        this.client.request("evener/command/list", {}),
        this.client.request("thread/read", {
          ref: this.ref,
          includeTurns: false,
        }),
      ]);
      if (this.dirty || this.disposed) continue;
      try {
        if (catalog.status === "rejected") throw catalog.reason;
        if (thread.status === "rejected") throw thread.reason;
        const evener = thread.value.thread.evener;
        if (evener.ref !== this.ref)
          throw new Error("Catalog belongs to another session");
        const diagnostics = evener.diagnostics;
        const plugins = diagnostics?.plugins
          ? new Set(diagnostics.plugins.map((plugin) => plugin.name))
          : null;
        const commands = visibleCatalogCommands(
          catalog.value.commands ?? [],
          plugins,
        );
        this.publish({
          items: mergeSlashCommands([], commands, diagnostics?.skills ?? []),
          error: null,
        });
      } catch (error) {
        this.publish({
          error: sessionActionError(
            "Could not load commands and skills",
            error,
          ),
        });
      }
    } while (this.dirty && !this.disposed);
    this.publish({ loading: false });
  }
  dispose() {
    this.disposed = true;
    this.unsubscribe?.();
    this.listeners.clear();
  }
}
