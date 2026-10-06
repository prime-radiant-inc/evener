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
/** One store per ref and scope, shared by every holder: the subagent count
 * and an Activity view read the same subtree store. A view that closes stops
 * observing its collections, and the store drops their pages still in flight,
 * so a count that keeps the store alive takes in no closed view's late page. */
const ownerKey = (ref: string, scope: SessionActivityScope) => JSON.stringify([ref, scope]);
const ownedStore = (client: AppwireClientLike, ref: string, scope: SessionActivityScope) =>
  owners.get(client)?.get(ownerKey(ref, scope))?.store;

/** Called only by committed view lifetimes. Routing refs stay unchanged even
 * when the resolved context names a different session after a workspace clear. */
export function acquireSessionActivity(
  client: AppwireClientLike,
  ref: string,
  scope: SessionActivityScope = "session",
): { store: SessionActivityStore; release(): void } {
  let connection = owners.get(client);
  if (!connection) {
    connection = new Map();
    owners.set(client, connection);
  }
  const key = ownerKey(ref, scope);
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
): SessionActivitySnapshot | null {
  return ownedStore(client, ref, scope)?.getSnapshot() ?? null;
}

export function useSessionActivity(
  ref: string | null,
  scope: SessionActivityScope = "session",
  collection?: SessionActivityCollection | readonly SessionActivityCollection[],
): { snapshot: SessionActivitySnapshot | null; loadMore(resource: SessionActivityCollection): Promise<void> } {
  const client = useConnectionStore((state) => state.client);
  const subscribe = useCallback(
    (listener: () => void) => {
      if (!client || !ref) return () => {};
      const lease = acquireSessionActivity(client, ref, scope);
      const stop = lease.store.subscribe(listener);
      return () => {
        stop();
        lease.release();
      };
    },
    [client, ref, scope],
  );
  const getSnapshot = useCallback(
    () => (client && ref ? sessionActivitySnapshot(client, ref, scope) : null),
    [client, ref, scope],
  );
  const snapshot = useSyncExternalStore(subscribe, getSnapshot, () => null);
  useEffect(() => {
    if (!client || !ref || !collection) return;
    const store = ownedStore(client, ref, scope);
    if (!store) return;
    const releases = (typeof collection === "string" ? [collection] : collection).map((resource) =>
      store.observe(resource),
    );
    return () => {
      for (const release of releases) release();
    };
  }, [client, ref, scope, collection]);
  const loadMore = useCallback(
    (resource: SessionActivityCollection) => {
      const store = client && ref ? ownedStore(client, ref, scope) : undefined;
      return store?.loadMore(resource) ?? Promise.resolve();
    },
    [client, ref, scope],
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
  const delegates = useSessionActivity(ref, "subtree").snapshot?.summary?.delegates;
  return delegates?.known ? delegates : null;
}
