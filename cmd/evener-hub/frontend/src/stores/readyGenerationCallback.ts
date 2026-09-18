import type { AppwireClientLike } from "@evener/appwire-client";

// Both the transcript display and keybindings web stores wire one bare
// "begin a ready generation" function to whichever client is currently
// wired, and rewire it (unsubscribe the old client, subscribe the new one)
// on every client change. A client's OWN ready dispatch can snapshot its
// registered handlers before a later rewire's unsubscribe removes this one
// (client.ts's setState iterates Array.from(this.readyHandlers), and a
// FakeClient's emitStateChange mirrors that), so a client already replaced
// can still invoke the handler it was registered with - starting a
// duplicate or premature generation through the connected-client port,
// which resolves whichever client is current NOW, not the one that fired.
// Wrapping the handler with the client it was registered for, and running
// it only while that client is still the current one, makes a stale firing
// a no-op instead of a wrong-generation refresh.
//
// Identity of the CLIENT alone is not enough: a client object rewired away
// and later back (a reconnect to the same instance) registers a fresh
// onReady handler for it, and a stale handler from the EARLIER registration
// would pass a bare `client === currentClient()` check once that client is
// current again, even though a later registration for the same object has
// since superseded it. Each guard's own latestToken keys a registration's
// token by its client object, so only the MOST RECENT registration for that
// object - the one whose token is still the value stored - is allowed to
// fire.
//
// The token map must be per STORE, not shared: keybindings.ts and
// transcriptDisplay.ts both wire the same connectionStore client, so a single
// shared map would have the second store's registration overwrite the
// first's token, permanently fencing the first store's own callback out.
// createReadyGenerationCallback returns one guard backed by its own private
// map; each store calls it once, at module scope, and reuses the guard for
// every registration it makes.
export type ReadyGenerationCallback = (
  client: AppwireClientLike,
  currentClient: () => AppwireClientLike | null,
  beginReadyGeneration: () => void,
) => () => void;

export function createReadyGenerationCallback(): ReadyGenerationCallback {
  const latestToken = new WeakMap<AppwireClientLike, symbol>();
  return (client, currentClient, beginReadyGeneration) => {
    const token = Symbol();
    latestToken.set(client, token);
    return () => {
      if (client === currentClient() && latestToken.get(client) === token) beginReadyGeneration();
    };
  };
}
