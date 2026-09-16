// The command catalog: the plugin and user-global slash commands a hub offers
// (evener/command/list), read once per connection and re-read when the hub
// reports a plugin change, plus the per-session view of it - the commands a
// session's loaded plugins can run, merged with the skills its diagnostics
// advertise - which is what a composer's slash menu shows. Both stores are
// framework-free factories over the getState/setState/subscribe triple; each
// app builds the instances it wires to its view layer, and tests build their
// own. Pure logic - no DOM, no React.
//
// createCommandCatalog is the hub-wide catalog, unfiltered, for a host that
// already holds each session's diagnostics and projects the catalog through
// them itself (sessionPluginNames + visibleCatalogCommands + mergeSlashCommands).
// createSessionCommandCatalog is the same catalog scoped to one session for a
// host that does not: it reads the session's diagnostics through thread/read
// beside the catalog and publishes the merged menu, atomically - a catalog
// paired with a failed or foreign-session diagnostics read is never shown.

import { visibleCatalogCommands } from "./catalogCommands";
import type { AppwireClient } from "./client";
import { sessionActionError } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "./frameworkFreeStore";
import { mergeSlashCommands, type SlashMenuItem } from "./slashCompletion";
import type { CommandDescriptor } from "./types.gen";

export type CommandCatalogClient = Pick<AppwireClient, "request" | "onNotification">;

export interface CommandCatalogState {
  /** The hub-wide catalog, unfiltered: every consumer scopes it to a session itself. */
  commands: CommandDescriptor[];
  /** A catalog has arrived at least once; a later failed re-read leaves it and this set. */
  loaded: boolean;
  loading: boolean;
  error: string | null;
  /** Re-reads the catalog. A refresh during a load re-runs the load once more
   * after it, so what lands is never older than the last request for it. */
  refresh: () => Promise<void>;
}

export interface CommandCatalog extends FrameworkFreeStore<CommandCatalogState> {
  /** Re-reads the catalog whenever the hub reports a plugin change, for as
   * long as the returned disposer has not been called. */
  watch(): () => void;
}

export function createCommandCatalog(client: CommandCatalogClient): CommandCatalog {
  const store = createFrameworkFreeStore<CommandCatalogState>((set) => {
    const loop = refreshLoop(
      async (superseded) => {
        try {
          const commands = await readCatalog(client);
          if (!superseded()) set({ commands, loaded: true, error: null });
        } catch (error) {
          if (!superseded()) set({ error: sessionActionError("Could not load commands", error) });
        }
      },
      (loading) => set({ loading }),
    );
    return { commands: [], loaded: false, loading: false, error: null, refresh: loop.refresh };
  });
  return {
    ...store,
    watch: () =>
      client.onNotification((n) => {
        if (n.method === "evener/plugin/updated") void store.getState().refresh();
      }),
  };
}

/** The session's plugin inventory as visibleCatalogCommands' filter: null when
 * the diagnostics carry none (the daemon could not say, so no plugin command is
 * offered), the set of loaded plugin names otherwise - an empty set included,
 * which is an authoritative empty inventory. */
export function sessionPluginNames(
  diagnostics: { plugins?: ReadonlyArray<{ name: string }> } | undefined,
): ReadonlySet<string> | null {
  return diagnostics?.plugins ? new Set(diagnostics.plugins.map((plugin) => plugin.name)) : null;
}

export interface SessionCommandCatalogState {
  /** The session's slash menu: its runnable catalog commands, then its skills. */
  items: SlashMenuItem[];
  loading: boolean;
  error: string | null;
}

export interface SessionCommandCatalog extends FrameworkFreeStore<SessionCommandCatalogState> {
  refresh(): Promise<void>;
  /** Loads once and re-reads on a plugin change or this session's resync. Idempotent. */
  start(): void;
  /** Ends every subscription; nothing is published after this, a load in flight included. */
  dispose(): void;
}

export function createSessionCommandCatalog(client: CommandCatalogClient, ref: string): SessionCommandCatalog {
  const store = createFrameworkFreeStore<SessionCommandCatalogState>(() => ({
    items: [],
    loading: false,
    error: null,
  }));
  let unsubscribe: (() => void) | undefined;
  const loop = refreshLoop(
    async (superseded) => {
      const [catalog, thread] = await Promise.allSettled([
        readCatalog(client),
        client.request("thread/read", { ref, includeTurns: false }),
      ]);
      if (superseded()) return;
      try {
        if (catalog.status === "rejected") throw catalog.reason;
        if (thread.status === "rejected") throw thread.reason;
        const evener = thread.value.thread.evener;
        if (evener.ref !== ref) throw new Error("Catalog belongs to another session");
        const commands = visibleCatalogCommands(catalog.value, sessionPluginNames(evener.diagnostics));
        store.setState({ items: mergeSlashCommands([], commands, evener.diagnostics?.skills ?? []), error: null });
      } catch (error) {
        store.setState({ error: sessionActionError("Could not load commands and skills", error) });
      }
    },
    (loading) => store.setState({ loading }),
  );
  return {
    ...store,
    refresh: loop.refresh,
    start() {
      if (loop.disposed() || unsubscribe) return;
      unsubscribe = client.onNotification((n) => {
        if (n.method === "evener/plugin/updated" || (n.method === "evener/thread/resync" && n.params.ref === ref))
          void loop.refresh();
      });
      void loop.refresh();
    },
    dispose() {
      loop.dispose();
      unsubscribe?.();
    },
  };
}

async function readCatalog(client: CommandCatalogClient): Promise<CommandDescriptor[]> {
  const response = await client.request("evener/command/list", {});
  return response.commands ?? [];
}

// One load at a time. A refresh during a load marks it superseded - the
// attempt drops its result and the loop runs once more - so a stale response
// never lands over a newer request's, and a disposed loop publishes nothing.
function refreshLoop(attempt: (superseded: () => boolean) => Promise<void>, setLoading: (loading: boolean) => void) {
  let inFlight: Promise<void> | undefined;
  let dirty = false;
  let disposed = false;
  const superseded = () => dirty || disposed;
  const refresh = (): Promise<void> => {
    if (disposed) return Promise.resolve();
    if (inFlight) {
      dirty = true;
      return inFlight;
    }
    inFlight = (async () => {
      setLoading(true);
      do {
        dirty = false;
        await attempt(superseded);
      } while (dirty && !disposed);
      if (!disposed) setLoading(false);
    })().finally(() => {
      inFlight = undefined;
    });
    return inFlight;
  };
  return {
    refresh,
    disposed: () => disposed,
    dispose: () => {
      disposed = true;
    },
  };
}
