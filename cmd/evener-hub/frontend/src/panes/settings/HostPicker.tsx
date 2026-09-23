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
import { isLocalHost, LOCAL_HOST } from "../../stores/hostRouting";
import { type HostsLoadState, hostsStore, useHostsStore } from "../../stores/hosts";
import { FormRow, Select, type SelectOption } from "../../widgets";
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

export function HostPicker() {
  const { host, selectHost } = useSettingsHost();
  const load = useHostsStore((state) => state.load);
  // The host registry is the Settings pane's own source of configured hosts
  // (stores/hosts.ts); this picker reads it, it does not add a second one.
  useConnectedEffect(() => hostsStore.getState().fetch(), []);

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
