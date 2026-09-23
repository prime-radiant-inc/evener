// CredentialsHostScope is the credentials settings surface's host scope
// (component 07b's read path made visible): a "Host" picker over the local hub
// plus every configured remote host the Hosts store already knows about, and -
// when a remote host is selected - a READ-ONLY view of THAT host's own provider
// listing, read through evener/host/request.
//
// Two stores, deliberately: the controller's rows are the package credential
// store's (stores/credentials.ts), and a remote host's own rows live in
// hostInstancesStore's per-host partition (useHostInstances). This surface only
// chooses WHICH of the two to read; it never merges them. Local - the default -
// renders CredentialsSection itself, so a user who never picks a host sees
// exactly today's pane.
//
// A host that is not attached (or no longer configured) is shown honestly:
// selecting it renders that host's own refusal/state, never a fallback to the
// controller's listing.

import type { InstanceEntry } from "@evener/appwire-client";
import { groupByProvider } from "@evener/appwire-client";
import { useState } from "react";
import { EMPTY_HOST_INSTANCE_STATE, fetchHost, useHostInstances } from "../../../../stores/credentials";
import { isLocalHost, LOCAL_HOST } from "../../../../stores/hostRouting";
import { hostsStore, useHostsStore } from "../../../../stores/hosts";
import { Chip, EmptyState, FormRow, Select, type SelectOption, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { useConnectedEffect } from "../useConnectedEffect";
import styles from "./CredentialsHostScope.module.css";
import { CredentialsSection } from "./CredentialsSection";
import { instanceMetaText } from "./InstanceRow";

const CLASS = {
  root: requireClass(styles.root, "CredentialsHostScope.module.css", "root"),
  remote: requireClass(styles.remote, "CredentialsHostScope.module.css", "remote"),
  heading: requireClass(styles.heading, "CredentialsHostScope.module.css", "heading"),
  note: requireClass(styles.note, "CredentialsHostScope.module.css", "note"),
  error: requireClass(styles.error, "CredentialsHostScope.module.css", "error"),
  groups: requireClass(styles.groups, "CredentialsHostScope.module.css", "groups"),
  group: requireClass(styles.group, "CredentialsHostScope.module.css", "group"),
  groupHeader: requireClass(styles.groupHeader, "CredentialsHostScope.module.css", "groupHeader"),
  list: requireClass(styles.list, "CredentialsHostScope.module.css", "list"),
  row: requireClass(styles.row, "CredentialsHostScope.module.css", "row"),
  rowName: requireClass(styles.rowName, "CredentialsHostScope.module.css", "rowName"),
  rowMeta: requireClass(styles.rowMeta, "CredentialsHostScope.module.css", "rowMeta"),
};

const HOST_SELECT_ID = "credentials-host";

export interface CredentialsHostScopeProps {
  /** Forwarded to CredentialsSection so the local view keeps the section
   * dispatch contract (Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
}

/**
 * The credentials settings section, scoped to a host. The picker's options are
 * the local hub plus the remote hosts the Hosts store lists; a remote selection
 * swaps the controller's editor for a read-only view of that host's own rows.
 */
export function CredentialsHostScope({ sectionId }: CredentialsHostScopeProps) {
  const load = useHostsStore((state) => state.load);
  const [host, setHost] = useState<string>(LOCAL_HOST);
  // The host registry is the Settings pane's own source of configured hosts
  // (stores/hosts.ts); this surface reads it, it does not add a second one.
  useConnectedEffect(() => hostsStore.getState().fetch(), []);

  const hostRows = load.phase === "ready" ? load.hosts.filter((row) => !row.removed) : [];
  const options: SelectOption[] = [
    { value: LOCAL_HOST, label: "This hub" },
    ...hostRows.map((row) => ({ value: row.name, label: row.attached ? row.name : `${row.name} (offline)` })),
  ];
  const known = hostRows.some((row) => row.name === host);
  if (!isLocalHost(host) && !known) {
    // Keep the current selection visible even after the host leaves the
    // registry, so the honest "no longer configured" state below names it.
    options.push({ value: host, label: host });
  }

  return (
    <div className={CLASS.root}>
      <FormRow
        label="Host"
        htmlFor={HOST_SELECT_ID}
        help="Whose provider instances to show. Remote hosts are shown read-only; configure them on the host itself."
      >
        <Select id={HOST_SELECT_ID} value={host} onChange={(event) => setHost(event.target.value)} options={options} />
      </FormRow>
      {isLocalHost(host) ? (
        <CredentialsSection sectionId={sectionId} />
      ) : known ? (
        <RemoteHostInstances host={host} />
      ) : (
        <p className={CLASS.note} role="status">
          Host {host} is no longer configured, so it has no provider listing to show.
        </p>
      )}
    </div>
  );
}

/** RemoteHostInstances is the read-only view of one remote host's own provider
 * listing. It reads that host's partition through useHostInstances (component
 * 07b) and re-reads it through evener/host/request when the selection changes or
 * the connection returns - the same load path the spawn form's useProviderSetup
 * uses. Nothing here writes: the rows carry no actions. */
function RemoteHostInstances({ host }: { host: string }) {
  const state = useHostInstances(host);
  useConnectedEffect(() => fetchHost(host), [host]);

  const title = `Providers on ${host}`;
  const groups = groupByProvider(state.instances);
  // hostPartition returns the frozen empty partition until that host has ever
  // been read (stores/credentials.ts), so identity against it is the honest
  // "nothing has been read yet" test - the skeleton is shown instead of a
  // premature "no instances" while the host's first answer is still out.
  const neverRead = state === EMPTY_HOST_INSTANCE_STATE;
  const pending = (neverRead || state.loading) && state.instances.length === 0;
  return (
    <section className={CLASS.remote} aria-label={title}>
      <h3 className={CLASS.heading}>{title}</h3>
      <p className={CLASS.note}>
        Read-only. These are {host}'s own provider instances, not this hub's - configure them on {host} itself.
      </p>
      {pending && <Skeleton />}
      {state.error !== null && (
        <p className={CLASS.error}>
          Couldn't read providers from {host}: {state.error}
        </p>
      )}
      {!pending && state.error === null && state.instances.length === 0 && (
        <EmptyState title={`No provider instances on ${host}.`} />
      )}
      {state.instances.length > 0 && (
        <div className={CLASS.groups}>
          {groups.map((group) => (
            <div key={group.providerId} className={CLASS.group}>
              {/* `name || id`, the same label the Add dialog and CredentialsSection
                  give a provider - one pane must not name a provider two ways. */}
              <div className={CLASS.groupHeader}>
                {state.availableProviders.find((provider) => provider.id === group.providerId)?.name ||
                  group.providerId}
              </div>
              <ul className={CLASS.list}>
                {group.instances.map((entry) => (
                  <RemoteInstanceRow key={entry.name} instance={entry} />
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

// RemoteInstanceRow is the read-only counterpart of InstanceRow: same identity
// and meta, no button and no chevron, because nothing on a remote host's row is
// actionable from here.
function RemoteInstanceRow({ instance }: { instance: InstanceEntry }) {
  const meta = instanceMetaText(instance);
  return (
    <li className={CLASS.row}>
      <span className={CLASS.rowName}>{instance.name}</span>
      {instance.isDefault && <Chip>★ default</Chip>}
      {meta !== "" && <span className={CLASS.rowMeta}>{meta}</span>}
    </li>
  );
}
