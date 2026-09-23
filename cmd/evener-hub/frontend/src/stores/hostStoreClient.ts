// hostStoreClient.ts is the store-boundary client for a REMOTE host (component
// 07b): the `Pick<AppwireClient, "request" | "onNotification">` port the
// package's launch-config, marketplace, plugins and launch-layer stores take,
// bound to the settings route's selected host.
//
// A store instance built over this port issues every request through
// evener/host/request, so the read or write lands on THAT host's hub, never on
// the controller's. It hears only the notifications the hub wrapped for that
// host - cmd/evener-hub/app_host_admin.go fans a remote host's own evener/...
// updates out tagged with the host whose notification it re-emits - unwrapped
// back to the plain method, so another host's change reaches no store bound
// here.
//
// The controller's own hub never uses this client: it keeps
// stores/connection.ts's connectedClientPort byte-for-byte (see
// stores/launchConfig.ts and stores/extensions.ts), so a local selection issues
// the same calls on the same client as before this module existed.
import type { AnyNotification, AppwireClient, MethodName, MethodTypes } from "@evener/appwire-client";
import { connectionStore, onConnectionNotification } from "./connection";
import { hostRequest } from "./hostRouting";

/** HostStoreClient is the narrow port every host-scoped package store takes
 * (LaunchConfigClient, MarketplacesClient, PluginsClient, LaunchLayerClient are
 * all this same Pick). */
export type HostStoreClient = Pick<AppwireClient, "request" | "onNotification">;

/** remoteHostStoreClient binds that port to `host`:
 *
 *   - `request` resolves connectionStore's CURRENT client on every call, never
 *     captured at module load, so a call before connect() fails loudly and a
 *     reconnect is picked up without rebuilding the store; it then forwards
 *     the call through evener/host/request.
 *   - `onNotification` follows the connection the way connectedClientPort
 *     does, and delivers only the frames the hub wrapped for `host`, unwrapped
 *     to the plain notification. Frames tagged with any other host - and the
 *     controller's own plain frames, which reach the local stores instead - are
 *     dropped.
 *
 * The per-call `opts` AppwireClient.request accepts has no evener/host/request
 * equivalent, and none of the host-scoped stores pass one (they call with
 * method and params only), so it is not forwarded. */
export function remoteHostStoreClient(host: string): HostStoreClient {
  return {
    request: async <M extends MethodName>(
      method: M,
      params: MethodTypes[M]["params"],
      opts?: { timeoutMs?: number },
    ): Promise<MethodTypes[M]["result"]> => {
      void opts;
      const client = connectionStore.getState().client;
      if (!client) {
        throw new Error(
          `host ${host} store: no client connected; call connectionStore.getState().connect(client) first`,
        );
      }
      return hostRequest(client, host, method, params);
    },
    onNotification: (cb) =>
      onConnectionNotification((n) => {
        if (n.method !== "evener/host/notification") return;
        if (n.params.host !== host) return;
        cb({ method: n.params.method, params: n.params.params } as AnyNotification);
      }),
  };
}
