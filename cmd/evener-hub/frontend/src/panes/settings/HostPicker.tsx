// HostPicker is the settings route's shared host selector: the local hub plus
// every remote host the Hosts registry store lists (offline ones marked). One
// component, consumed by every host-scoped settings pane on top of the one
// shared selection (stores/settingsHost.ts, read through useSettingsHost) - a
// pane must not grow a picker of its own.
//
// A host that has left the registry keeps an option, so the current selection
// stays visible; the pane consuming the selection decides what an unknown host
// renders (CredentialsHostScope says so honestly).
import type { HostRow } from "@evener/appwire-client";
import { useEffect, useRef } from "react";
import { useConnectionStore } from "../../stores/connection";
import { isLocalHost, LOCAL_HOST } from "../../stores/hostRouting";
import { type HostsLoadState, hostIdentity, hostsStore, useHostsStore } from "../../stores/hosts";
import { FormRow, Select, type SelectOption } from "../../widgets";
import { HOST_POLL_MS } from "./sections/hosts";
import { useConnectedEffect } from "./sections/useConnectedEffect";
import { useSettingsHost } from "./settingsHost";

export const HOST_SELECT_ID = "settings-host";

/** selectableHostRows is the registry's non-removed rows, or none while it is
 * still loading or has failed. Shared by the picker and by the panes that must
 * decide what an unknown host means. */
export function selectableHostRows(load: HostsLoadState): HostRow[] {
  return load.phase === "ready" ? load.hosts.filter((row) => !row.removed) : [];
}

/** isConfiguredHost answers whether the registry currently lists `host`. A
 * false while the registry is still unread is ambiguous, so callers pair it
 * with the load phase (see CredentialsHostScope). */
export function isConfiguredHost(load: HostsLoadState, host: string): boolean {
  return selectableHostRows(load).some((row) => row.name === host);
}

/** hostIdentityFor is the registry's identity for `host` (hosts.ts's
 * hostIdentity), or null while the registry does not list it - still being read,
 * removed, or not configured at all. Cached rows are only the current host's
 * while this matches the identity they were read under. */
export function hostIdentityFor(load: HostsLoadState, host: string): string | null {
  const row = selectableHostRows(load).find((candidate) => candidate.name === host);
  return row === undefined ? null : hostIdentity(row);
}

/** HostRegistryFacts is what a host-scoped pane needs from the registry, as of
 * the last READY answer. */
export interface HostRegistryFacts {
  /** The identity the host's cached rows are keyed on - the configured entry, so
   * live state cannot invalidate a good listing. */
  identity: string | null;
  /** The host's attachment state. LIVE data, deliberately kept OUT of the
   * identity (folding it in would invalidate a listing on every detach) and used
   * only to decide when a failed read is worth retrying. */
  attached: boolean;
}

/** useHostRegistryFacts is the registry's identity and attachment state for
 * `host`, remembering the last READY answer. A registry re-read that fails, or is
 * still in flight, cannot say the name now means another host or that the host is
 * gone - so it must not discard what the last successful answer established, nor
 * flap the remote reads keyed on it (a flap re-issues the host's listing once per
 * phase). Only a ready answer replaces it. */
export function useHostRegistryFacts(load: HostsLoadState, host: string): HostRegistryFacts {
  const known = useRef<{ host: string; facts: HostRegistryFacts }>({
    host,
    facts: { identity: null, attached: false },
  });
  if (known.current.host !== host || load.phase === "ready") {
    const row = selectableHostRows(load).find((candidate) => candidate.name === host);
    known.current = {
      host,
      facts: { identity: row === undefined ? null : hostIdentity(row), attached: row?.attached ?? false },
    };
  }
  return known.current.facts;
}

export function HostPicker() {
  const { host, selectHost } = useSettingsHost();
  const load = useHostsStore((state) => state.load);
  const { client, state: connection } = useConnectionStore();
  // The host registry is the Settings pane's own source of configured hosts
  // (stores/hosts.ts); this picker reads it, it does not add a second one.
  //
  // The read follows the CONNECTION: a reconnect or a client swap must not leave
  // the picker - or the registry identity every remote read keys on - describing
  // the hub that was. And it re-reads on the registry's own quiet-refresh
  // cadence, because a host added or removed by another client is observable here
  // only by asking again: there is no controller-side host lifecycle notification
  // on the wire, which is why the Hosts section re-reads on this same cadence
  // (sections/hosts.tsx) rather than this picker inventing one.
  useConnectedEffect(() => hostsStore.getState().fetch(), [client, connection]);
  useEffect(() => {
    const id = setInterval(() => void hostsStore.getState().refresh(), HOST_POLL_MS);
    return () => clearInterval(id);
  }, []);

  const hostRows = selectableHostRows(load);
  const options: SelectOption[] = [
    { value: LOCAL_HOST, label: "This hub" },
    ...hostRows.map((row) => ({ value: row.name, label: row.attached ? row.name : `${row.name} (offline)` })),
  ];
  if (!isLocalHost(host) && !isConfiguredHost(load, host)) {
    options.push({ value: host, label: host });
  }

  return (
    <FormRow
      label="Host"
      htmlFor={HOST_SELECT_ID}
      help="Whose settings to show: this hub's own, or a remote host's own. A remote host's settings are always shown as that host's, never as this hub's."
    >
      <Select id={HOST_SELECT_ID} value={host} onChange={(event) => selectHost(event.target.value)} options={options} />
    </FormRow>
  );
}
