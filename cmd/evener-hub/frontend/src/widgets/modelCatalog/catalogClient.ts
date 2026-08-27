import type { AppwireClientLike } from "../../protocol/testing/fakeClient";
import type { ModelDescriptor, ModelListParams, ModelListResponse } from "../../protocol/types.gen";
import { connectionStore } from "../../stores/connection";
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
