import type { ConnectionState } from "@evener/appwire-client";
import {
  type CredentialInstancesClient,
  type CredentialInstancesStore,
  createCredentialInstancesStore,
} from "@evener/appwire-client/state/credentials";
import { randomUUID } from "expo-crypto";
import { useEffect, useMemo } from "react";
import { useConnection } from "./ConnectionProvider";

// This app's identity on the auth mutations it issues: the hub echoes it in
// evener/auth/updated so every client attributes the change to its origin.
// One per process is enough - nothing durable compares it later.
const nativeClientId = `native-${randomUUID()}`;

/** A credential store already bound to a connection: the package's credential
 * instances store core with this app's identity, told which connection the
 * rows belong to before anything can read through it. */
export function createNativeCredentialStore(
  client: CredentialInstancesClient | null,
  state: ConnectionState,
): CredentialInstancesStore {
  const store = createCredentialInstancesStore({ ownClientId: () => nativeClientId });
  store.connectionChanged(client, state);
  return store;
}

/** The credential store for the selected hub: one per hub, shared by the
 * provider list and any sign-in flow open on the screen. It lives above both
 * because a sign-in flow outlives a client replacement while the list is
 * rebuilt per client; this hook tells it which connection the rows belong to,
 * and it publishes the rows. */
export function useCredentialStore(): CredentialInstancesStore {
  const { activeProfile, client, state } = useConnection();
  // Built already bound to the connection of the render that creates it:
  // React runs a child's effects before its parent's, so the provider list's
  // start() reads before this hook's effect below could have said which
  // client to read through. The effect then follows every transition.
  // biome-ignore lint/correctness/useExhaustiveDependencies: one store per hub; the connection is followed by the effect below
  const store = useMemo(() => createNativeCredentialStore(client, state), [activeProfile?.id]);
  // Two effects, not one: a single effect with a cleanup would say "closed"
  // on every client swap before saying "ready", cancelling reads in flight.
  useEffect(() => {
    store.connectionChanged(client, state);
  }, [store, client, state]);
  useEffect(() => () => store.connectionChanged(null, "closed"), [store]);
  return store;
}
