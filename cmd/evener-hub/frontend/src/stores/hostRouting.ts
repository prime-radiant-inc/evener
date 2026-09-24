// hostRouting.ts is the frontend's host-scoped request seam (component 07b).
//
// The spawn form's discovery/validation calls are all answered by a hub against
// ITS local environment (models, harnesses, launch config, the filesystem,
// recent projects, the slash catalog, git HEAD, plugin diagnostics, provider
// instances). Left controller-scoped, a remote launch is validated against the
// wrong machine: a controller-local path accepted for a remote spawn, the
// controller's model/harness list, its repository's branch, its plugin and
// instance configuration. `evener/host/request` (component 07a) forwards one
// allow-listed hub-scoped RPC to the selected host's hub over its SSH channel,
// and that host's own handlers resolve these against the host.
//
// Rule: the local host keeps the plain call, byte-for-byte. Any other selected
// host wraps the call in `evener/host/request`; the hub's allow-list is the
// security boundary and refuses anything it does not name.
import type { AppwireClientLike, HostRequestParams, MethodName, MethodTypes } from "@evener/appwire-client";

// LOCAL_HOST is the manifest's id for the controller's own hub (component 06).
export const LOCAL_HOST = "local";

// HOST_DEPENDENT_DISCOVERY_METHODS is the exact set of spawn-form calls that
// must be issued against the selected host (component 06 §"Frontend changes",
// acceptance criterion 10; component 07 §"Proxy method"). It names the methods
// the remote proxy allow-lists: model discovery, harnesses, launch
// resolution/schema, path completion/validation, directory creation, recent
// projects, the spawn slash catalog, the branch/location git HEAD read, the
// plugin-preview panel, and the provider-instance list. A method removed from
// the product should be removed from both this set and the allow-list.
//
// The two sides are pinned to each other by a single checked-in list,
// cmd/evener-hub/host_request_methods.txt: this file's own test asserts the
// array below IS that list (both directions), and the Go proxy's test asserts
// every name on it is on remoteHostAdminMethods and is actually forwarded
// rather than refused (app_host_admin_test.go). Before that pin, each side only
// answered to a literal of its own, so a method dropped from one end left the
// other end green while the browser forwarded a call the proxy rejects.
export const HOST_DEPENDENT_DISCOVERY_METHODS = [
  "model/list",
  "evener/harnesses/list",
  "evener/launch/resolve",
  "evener/launch/schema",
  "evener/paths/complete",
  "evener/path/validate",
  "evener/dirs/create",
  "evener/projects/recent",
  "evener/spawn/slashCatalog",
  "evener/git/head",
  "evener/plugin/preview",
  "evener/instance/list",
] as const satisfies readonly MethodName[];

export type HostDependentDiscoveryMethod = (typeof HOST_DEPENDENT_DISCOVERY_METHODS)[number];

/** True when host is the controller's own hub, so the call stays on the plain
 * connection. An absent or empty host is local: a caller that has not threaded
 * a host through yet must not accidentally wrap a call aimed at itself. */
export function isLocalHost(host: string | null | undefined): boolean {
  return host == null || host === "" || host === LOCAL_HOST;
}

/** normalizeHost maps an absent, empty, or `local` spelling onto LOCAL_HOST - the
 * hostless default - and leaves every real host name alone. This is the ONE
 * spelling rule: the settings store's reading of its URL and shell/routing.ts's
 * carry of the selected host both go through it (it lives here, beside
 * isLocalHost, because those two modules must not import each other). */
export function normalizeHost(raw: string | null): string {
  return isLocalHost(raw) ? LOCAL_HOST : (raw as string);
}

/**
 * hostRequest issues `method` against the selected `host`.
 *
 * For the local host it is exactly `client.request(method, params, opts)`, so
 * every existing local behavior is unchanged. For a remote host it is
 * `client.request("evener/host/request", {host, method, params}, opts)`, whose
 * result is the forwarded method's own result verbatim, so callers type it as
 * the underlying method's result.
 *
 * `opts` is the caller's per-call deadline. It is honoured on both sides: the
 * local call bounds the method itself, the remote call bounds the proxy request
 * that carries it (see below) - a deadline is never dropped on the floor.
 */
export function hostRequest<M extends MethodName>(
  client: AppwireClientLike,
  host: string | null | undefined,
  method: M,
  params: MethodTypes[M]["params"],
  opts?: { timeoutMs?: number },
): Promise<MethodTypes[M]["result"]> {
  if (isLocalHost(host)) {
    return client.request(method, params, opts);
  }
  const forwarded: HostRequestParams = { host: host as string, method, params };
  // The caller's deadline rides the ONE call that carries their wait, the proxy
  // request itself: evener/host/request's params are {host, method, params} and
  // have no deadline field (none is invented here), so without this a caller who
  // asked for a longer budget than the client's own default would silently run
  // under that default instead. The bound is then the whole forwarded round trip
  // (the SSH hop plus the remote hub's handling), which is the closest
  // equivalent the wire admits; a deadline error names evener/host/request
  // rather than the method it forwarded.
  return client
    .request("evener/host/request", forwarded, opts)
    .then((result) => result as unknown as MethodTypes[M]["result"]);
}
