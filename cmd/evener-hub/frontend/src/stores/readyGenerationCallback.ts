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
export function readyGenerationCallback(
  client: AppwireClientLike,
  currentClient: () => AppwireClientLike | null,
  beginReadyGeneration: () => void,
): () => void {
  return () => {
    if (client === currentClient()) beginReadyGeneration();
  };
}
