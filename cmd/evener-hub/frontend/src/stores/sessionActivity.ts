import {
  type AppwireClientLike,
  type SessionActivityCollection,
  type SessionActivityScope,
  type SessionActivitySnapshot,
  SessionActivityStore,
  type SessionActivitySummary,
} from "@evener/appwire-client";
import { useCallback, useEffect, useSyncExternalStore } from "react";
import { useConnectionStore } from "./connection";

interface Owner {
  store: SessionActivityStore;
  holders: number;
}
const owners = new WeakMap<AppwireClientLike, Map<string, Owner>>();
/** A view owns the store its collections page through; the subagent count
 * owns its own summary-only store, so a count held by an always-mounted
 * surface (the footer, a closed Activity trigger) never keeps a closed view's
 * collection reads alive: closing the view disposes its store and fences its
 * late pages, whatever still counts. */
type OwnerRole = "view" | "count";
const ownerKey = (ref: string, scope: SessionActivityScope, role: OwnerRole) => JSON.stringify([ref, scope, role]);
const ownedStore = (client: AppwireClientLike, ref: string, scope: SessionActivityScope, role: OwnerRole) =>
  owners.get(client)?.get(ownerKey(ref, scope, role))?.store;

/** Called only by committed view lifetimes. Routing refs stay unchanged even
 * when the resolved context names a different session after a workspace clear. */
export function acquireSessionActivity(
  client: AppwireClientLike,
  ref: string,
  scope: SessionActivityScope = "session",
  role: OwnerRole = "view",
): { store: SessionActivityStore; release(): void } {
  let connection = owners.get(client);
  if (!connection) {
    connection = new Map();
    owners.set(client, connection);
  }
  const key = ownerKey(ref, scope, role);
  let owner = connection.get(key);
  if (!owner) {
    owner = { store: new SessionActivityStore(client, ref, { scope }), holders: 0 };
    connection.set(key, owner);
  }
  owner.holders += 1;
  owner.store.start();
  let released = false;
  return {
    store: owner.store,
    release() {
      if (released) return;
      released = true;
      owner.holders -= 1;
      if (owner.holders !== 0) return;
      owner.store.dispose();
      connection.delete(key);
      if (connection.size === 0) owners.delete(client);
    },
  };
}

/** Pure snapshot lookup: an abandoned React render cannot acquire ownership. */
export function sessionActivitySnapshot(
  client: AppwireClientLike,
  ref: string,
  scope: SessionActivityScope,
  role: OwnerRole = "view",
): SessionActivitySnapshot | null {
  return ownedStore(client, ref, scope, role)?.getSnapshot() ?? null;
}

export function useSessionActivity(
  ref: string | null,
  scope: SessionActivityScope = "session",
  collection?: SessionActivityCollection | readonly SessionActivityCollection[],
): { snapshot: SessionActivitySnapshot | null; loadMore(resource: SessionActivityCollection): Promise<void> } {
  return useOwnedSessionActivity(ref, scope, collection, "view");
}

function useOwnedSessionActivity(
  ref: string | null,
  scope: SessionActivityScope,
  collection: SessionActivityCollection | readonly SessionActivityCollection[] | undefined,
  role: OwnerRole,
): { snapshot: SessionActivitySnapshot | null; loadMore(resource: SessionActivityCollection): Promise<void> } {
  const client = useConnectionStore((state) => state.client);
  const subscribe = useCallback(
    (listener: () => void) => {
      if (!client || !ref) return () => {};
      const lease = acquireSessionActivity(client, ref, scope, role);
      const stop = lease.store.subscribe(listener);
      return () => {
        stop();
        lease.release();
      };
    },
    [client, ref, scope, role],
  );
  const getSnapshot = useCallback(
    () => (client && ref ? sessionActivitySnapshot(client, ref, scope, role) : null),
    [client, ref, scope, role],
  );
  const snapshot = useSyncExternalStore(subscribe, getSnapshot, () => null);
  useEffect(() => {
    if (!client || !ref || !collection) return;
    const store = ownedStore(client, ref, scope, role);
    if (!store) return;
    const releases = (typeof collection === "string" ? [collection] : collection).map((resource) =>
      store.observe(resource),
    );
    return () => {
      for (const release of releases) release();
    };
  }, [client, ref, scope, role, collection]);
  const loadMore = useCallback(
    (resource: SessionActivityCollection) => {
      const store = client && ref ? ownedStore(client, ref, scope, role) : undefined;
      return store?.loadMore(resource) ?? Promise.resolve();
    },
    [client, ref, scope, role],
  );
  return { snapshot, loadMore };
}

/** A session's subagents at every depth: how many runs are open and how many
 * there are, from the hub's subtree activity summary, or null until the hub
 * knows. This is the web's one count of "running subagents": the status bar's
 * Agents chip, the activity sidebar's Agents tab, the Overview and Activity
 * action counts and the liveness line all read it, as the hub's Live tally and
 * the phone count them. */
export function useSubagentCounts(ref: string | null): SessionActivitySummary["delegates"] | null {
  const delegates = useOwnedSessionActivity(ref, "subtree", undefined, "count").snapshot?.summary?.delegates;
  return delegates?.known ? delegates : null;
}
