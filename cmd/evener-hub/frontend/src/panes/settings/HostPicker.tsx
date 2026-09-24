// HostPicker is the settings route's shared host selector: the local hub plus
// every remote host the Hosts registry store lists (offline ones marked). One
// component, consumed by every host-scoped settings pane on top of the one
// shared selection (stores/settingsHost.ts, read through useSettingsHost) - a
// pane must not grow a picker of its own.
//
// A host that has left the registry keeps an option, so the current selection
// stays visible; the pane consuming the selection decides what an unknown host
// renders (CredentialsHostScope says so honestly).
import { useEffect } from "react";
import { useConnectionStore } from "../../stores/connection";
import { isLocalHost, LOCAL_HOST } from "../../stores/hostRouting";
import { hostsStore, isConfiguredHost, selectableHostRows, useHostsStore } from "../../stores/hosts";
import { FormRow, Select, type SelectOption } from "../../widgets";
import { HOST_POLL_MS } from "./sections/hosts";
import { useConnectedEffect } from "./sections/useConnectedEffect";
import { useSettingsHost } from "./settingsHost";

export const HOST_SELECT_ID = "settings-host";

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
