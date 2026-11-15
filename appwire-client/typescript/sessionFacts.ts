// Facts a session's details surface reads off its thread: which host it runs
// on, the access it was started with, and the plugins it loaded. Every client
// derives them here so the browser and the phone cannot disagree about what a
// session is; each owns only how it lays the facts out.
import { sessionPluginNames } from "./commandCatalog";
import { LOCAL_HOST } from "./hostRouting";
import type { ThreadModel } from "./model";

/** A ref names its host first ("<host>:<session>"); a ref without one is the
 * hub's own. */
export function hostIdOf(ref: string): string {
  const at = ref.indexOf(":");
  return at > 0 ? ref.slice(0, at) : LOCAL_HOST;
}

/** A host as a details surface shows it: its name, and whether it is
 * connected; `online` is null when nothing says. */
export interface SessionHost {
  label: string;
  online: boolean | null;
}

/** Names each host a session can run on. The hub's own machine goes by the
 * hub's name when the client has one (the manifest only calls it "this host")
 * and is as connected as the hub; any other host goes by its manifest label
 * and state, or by its id while the manifest doesn't list it. While the hub is
 * away every host it lists reads offline, since the client can't reach any of
 * them. */
export function sessionHosts(
  sources: readonly { id: string; label: string; online: boolean }[] | undefined,
  hubName: string | null,
  hubConnected: boolean,
): (hostId: string) => SessionHost {
  const byId = new Map((sources ?? []).map((source) => [source.id, source]));
  return (hostId) => {
    const source = byId.get(hostId);
    if (hostId === LOCAL_HOST) return { label: hubName ?? source?.label ?? hostId, online: hubConnected };
    return { label: source?.label ?? hostId, online: source ? hubConnected && source.online : null };
  };
}

/** The plugins a session loaded, by name, in the order a reader scans them.
 * Null when the inventory is unknown, which is not the same as empty
 * (sessionPluginNames). Plugins are fixed when a session starts. */
export function loadedPluginNames(session: Pick<ThreadModel, "diagnostics">): string[] | null {
  const names = sessionPluginNames(session.diagnostics);
  return names ? [...names].sort((a, b) => a.localeCompare(b)) : null;
}

const ACCESS_MODES: Record<string, string> = {
  off: "Full access",
  "workspace-write": "Workspace write",
  "read-only": "Read-only",
  restricted: "Restricted",
};

/** The session's sandbox mode and network setting, or null when the hub
 * doesn't report them. A mode this client doesn't know shows its raw name.
 * Both are fixed when a session starts. */
export function accessFacts(session: Pick<ThreadModel, "access">): { mode: string; network: string } | null {
  const access = session.access;
  if (!access) return null;
  return { mode: ACCESS_MODES[access.sandbox] ?? access.sandbox, network: access.network ? "On" : "Off" };
}
