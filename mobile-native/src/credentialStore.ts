import {
  type CredentialInstancesStore,
  createCredentialInstancesStore,
} from "@evener/appwire-client/state/credentials";
import { randomUUID } from "expo-crypto";
import { useEffect, useLayoutEffect, useState } from "react";
import { useConnection } from "./ConnectionProvider";
import { clientServesHub } from "./connectionIdentity";

// This app's identity on the auth mutations it issues: the hub echoes it in
// evener/auth/updated so every client attributes the change to its origin.
// One per process is enough - nothing durable compares it later.
const nativeClientId = `native-${randomUUID()}`;

/** An unbound credential store: the package's credential instances store core
 * with this app's identity. Constructing it subscribes to nothing; only
 * connectionChanged listens on a client and lets reads through. */
export function createNativeCredentialStore(): CredentialInstancesStore {
  return createCredentialInstancesStore({ ownClientId: () => nativeClientId });
}

/** The credential store for the Providers screen, shared by the provider list
 * and any sign-in flow open on it. It lives above both because a sign-in flow
 * outlives a client replacement while the list is rebuilt per client; this
 * hook tells it which connection the rows belong to, and it publishes the
 * rows. The connection it binds must serve the hub the route names: after a
 * re-key the connection still reports the previous hub's ready client for a
 * window, and the listing read would fetch that hub's provider rows under
 * the new hub. */
export function useCredentialStore(): CredentialInstancesStore {
	const { activeProfile, client, state } = useConnection();
	// A client the connection layer knows proved ready under another hub is
	// withheld until the connection re-points; an unknown client passes
	// exactly as before (connectionIdentity's rule — a genuinely new client
	// arrives unknown, and only the re-point distinguishes it).
	const gated =
		client !== null && clientServesHub(client, activeProfile?.id)
			? client
			: null;
	// The instance is committed state, and nothing binds it during render: a
	// render React discards (StrictMode's double render, a concurrent render it
	// throws away) would otherwise leave a bound store whose cleanup below never
	// runs, listening on the client for the client's lifetime. Binding happens
	// in a layout effect: React runs a parent's layout effects before any
	// child's passive effects, so the store knows its client before the provider
	// list's start() reads through it, and a discarded render binds nothing.
	const [store] = useState(createNativeCredentialStore);
	useLayoutEffect(() => {
		store.connectionChanged(gated, state);
	}, [store, gated, state]);
  // Unmount only, and separate from the effect above: a single effect with a
  // cleanup would say "closed" on every client swap before saying "ready",
  // cancelling reads in flight.
  useEffect(() => () => store.connectionChanged(null, "closed"), [store]);
  return store;
}
