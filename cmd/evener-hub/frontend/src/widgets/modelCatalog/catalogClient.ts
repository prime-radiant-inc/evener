import type { AppwireClientLike, ModelDescriptor, ModelListParams, ModelListResponse } from "@evener/appwire-client";
import { connectionStore } from "../../stores/connection";
import { hostRequest } from "../../stores/hostRouting";
import type { ModelCatalog, ModelCatalogEntry } from "./index";

export interface FetchCatalogOptions {
  /** Harness id to scope the launchable model set (spawn). Omitted = default. */
  harness?: string;
  /** Working directory whose project config scopes the set (spawn). */
  cwd?: string;
}

function currentClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) throw new Error("model list unavailable: no AppWire client");
  return client;
}

function toCatalogEntry(entry: ModelDescriptor): ModelCatalogEntry {
  return {
    ...entry,
    displayName: entry.displayName || entry.model,
  };
}

export function modelListToCatalog(response: ModelListResponse): ModelCatalog {
  return {
    models: (response.data ?? []).map(toCatalogEntry),
    recent: (response.recent ?? []).map(toCatalogEntry),
    diagnostics: response.diagnostics ?? [],
  };
}

/** Loads the rich catalog from the active window's typed model/list RPC. */
export async function fetchModelCatalog(
  opts?: FetchCatalogOptions,
  client: AppwireClientLike = currentClient(),
): Promise<ModelCatalog> {
  const params: ModelListParams = {};
  if (opts?.harness) params.harness = opts.harness;
  if (opts?.cwd) params.cwd = opts.cwd;
  return modelListToCatalog(await client.request("model/list", params));
}

/** fetchModelCatalogForHost loads the rich catalog against the selected host
 * (component 07b). For the local hub this is exactly `fetchModelCatalog`'s plain
 * model/list on the controller's connection; for a remote host the call is
 * wrapped in evener/host/request, so the picker describes the host the layer is
 * being edited for rather than this hub. model/list is on the hub's
 * host-dependent discovery allow-list (stores/hostRouting.ts). */
export async function fetchModelCatalogForHost(
  host: string | null | undefined,
  opts?: FetchCatalogOptions,
): Promise<ModelCatalog> {
  const params: ModelListParams = {};
  if (opts?.harness) params.harness = opts.harness;
  if (opts?.cwd) params.cwd = opts.cwd;
  return modelListToCatalog(await hostRequest(currentClient(), host, "model/list", params));
}
