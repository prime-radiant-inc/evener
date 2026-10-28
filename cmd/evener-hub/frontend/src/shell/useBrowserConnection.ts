import type { AppwireClientLike } from "@evener/appwire-client";
import { AppwireClient, rpcURLFromLocation } from "@evener/appwire-client";
import { useCallback, useEffect, useState } from "react";
import { connectionStore } from "../stores/connection";
import { bindNavigationConnection } from "../stores/navigation/binding";

type BackgroundOwner = { dispose(): Promise<void> };
type ClientSlot = { client: AppwireClientLike; owned: AppwireClient | null };

function createClientSlot(injected: AppwireClientLike | undefined): ClientSlot {
  if (injected) return { client: injected, owned: null };
  const real = new AppwireClient({ url: rpcURLFromLocation(window.location) });
  return { client: real, owned: real };
}

export function useBrowserConnection(options: {
  client?: AppwireClientLike;
  startBackground?: () => BackgroundOwner;
}): { client: AppwireClientLike; onClientReplaced(client: AppwireClientLike): void } {
  const [slot, setSlot] = useState(() => createClientSlot(options.client));
  const { client, owned } = slot;
  const startBackground = options.startBackground;
  const onClientReplaced = useCallback((fresh: AppwireClientLike) => {
    setSlot({ client: fresh, owned: fresh instanceof AppwireClient ? fresh : null });
  }, []);

  useEffect(() => {
    // Adopt this surface's client before checking for an already-ready one:
    // a previous surface may have left its injected client in the store.
    connectionStore.getState().connect(client);
    const unbindNavigation = bindNavigationConnection();
    const background = startBackground?.();
    // Real clients constructed by this hook must not dial jsdom's origin.
    if (!(owned && import.meta.env.MODE === "test")) {
      void client.connect().catch(() => {
        // The connection store publishes failure through the client state listener.
      });
    }
    return () => {
      unbindNavigation();
      const disposal = background?.dispose() ?? Promise.resolve();
      if (!owned) {
        void disposal.catch(() => {});
        return;
      }
      let timer: ReturnType<typeof setTimeout> | undefined;
      const deadline = new Promise<void>((resolve) => {
        timer = setTimeout(resolve, 30_000);
      });
      void Promise.race([disposal, deadline])
        .catch(() => {})
        .then(() => {
          clearTimeout(timer);
          owned.close();
        });
    };
  }, [client, owned, startBackground]);

  return { client, onClientReplaced };
}
