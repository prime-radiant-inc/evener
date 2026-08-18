// React context for the single AppwireClientLike the whole app shares
// (Global Constraints: "One AppwireClient per window, owned by the shell,
// injected via context"). AppShell constructs the real client and provides
// it here; everything below the shell reads it via useClient() instead of
// reaching into connectionStore.getState().client, which exists only as a
// seam for stores that have no connect() path of their own (see
// stores/connection.ts).
import { createContext, type ReactNode, useContext } from "react";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";

const ClientContext = createContext<AppwireClientLike | null>(null);

export function useClient(): AppwireClientLike {
  const client = useContext(ClientContext);
  if (!client) {
    throw new Error("useClient: no AppwireClient provided (render this inside a ClientProvider)");
  }
  return client;
}

export function ClientProvider({ client, children }: { client: AppwireClientLike; children: ReactNode }) {
  return <ClientContext.Provider value={client}>{children}</ClientContext.Provider>;
}
