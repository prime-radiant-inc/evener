// Host grouping: where a navigation row, and a project whose rows live on
// several hosts, sit when a client organizes its sessions by host (the web
// rail's "Organize by" and the phone Board's Projects/Hosts section). Pure
// projections of what the manifest and the rows already carry: a project's
// owning `sources`, a row's `host_id`, and the manifest's Source rows (label,
// online). The hub has no host-scoped project read (NavigationReadParams
// names no source), so a client reads each project's pages once and places
// every row under the host its own `host_id` names: a session shows once,
// however many hosts own its project.
import type { NavigationSessionSummary, Source } from "../../types.gen";

/** The source id the hub gives its own sessions; the wire spells it "local". */
export const CONTROLLER_SOURCE_ID = "local";

/** A host as a group heading: the manifest's label and online flag. */
export interface HostFacts {
  id: string;
  label: string;
  online: boolean;
}

/** What placing a row under a host reads from it. */
export type HostPlacedRow = Pick<NavigationSessionSummary, "host_id">;

/** The manifest's id to Source lookup, single-slot memoized on the sources
 * array's identity: a client hands back the manifest's own array, so every
 * call between manifest updates shares one Map instead of building one per
 * call. */
const sourceLookupCache: { sources: readonly Source[] | null; known: Map<string, Source> } = {
  sources: null,
  known: new Map(),
};

function sourceLookup(sources: readonly Source[]): Map<string, Source> {
  if (sourceLookupCache.sources !== sources) {
    sourceLookupCache.sources = sources;
    sourceLookupCache.known = new Map(sources.map((source): [string, Source] => [source.id, source]));
  }
  return sourceLookupCache.known;
}

/** Hosts in display order: this hub first, then online hosts by their labels
 * (the id breaking ties), offline hosts last (an offline host cannot reveal
 * rows until it reconnects, so it sorts behind the hosts that can). A host the
 * manifest does not name reads as online, the contract the web's session
 * chips follow, and falls back to its id as a label. */
export function orderedHosts(hostIds: Iterable<string>, sources: readonly Source[]): HostFacts[] {
  return [...new Set(hostIds)]
    .map((id) => {
      const source = sourceLookup(sources).get(id);
      return {
        id,
        label: source?.label ?? id,
        online: source ? source.online : true,
        tier: id === CONTROLLER_SOURCE_ID ? 0 : source ? (source.online ? 1 : 2) : 1,
      };
    })
    .sort((a, b) => a.tier - b.tier || a.label.localeCompare(b.label) || a.id.localeCompare(b.id))
    .map(({ id, label, online }) => ({ id, label, online }));
}

/** Every host a project's rows can sit under: its owning sources, plus the
 * host of every row loaded for it (a summary that predates a host still lands
 * where its rows are). A project naming neither (`sources` omitted or empty,
 * which the wire means as the controller's own) is this hub's. */
export function projectHostIds(sources: readonly string[] | undefined, rows: readonly HostPlacedRow[]): string[] {
  const hosts = new Set<string>(sources ?? []);
  for (const row of rows) hosts.add(row.host_id);
  if (hosts.size === 0) hosts.add(CONTROLLER_SOURCE_ID);
  return [...hosts];
}

/** The one host whose copy of a project carries the project's own counts and
 * paging when projects nest under hosts, so they read once instead of
 * claiming per-host numbers the wire does not carry: the first host in
 * display order with a loaded row, else the first host. A project no loaded
 * row names yet keeps the first host, so the choice cannot flip from copy to
 * copy while rows stream in. */
export function canonicalHostId(
  hostIds: readonly string[],
  rows: readonly HostPlacedRow[],
  sources: readonly Source[],
): string {
  const ordered = orderedHosts(hostIds, sources);
  const withRows = ordered.find(({ id }) => rows.some((row) => row.host_id === id));
  return (withRows ?? ordered[0])?.id ?? CONTROLLER_SOURCE_ID;
}
