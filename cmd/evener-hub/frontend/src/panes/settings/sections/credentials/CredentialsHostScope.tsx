// CredentialsHostScope is the credentials settings section, scoped to the host
// the SETTINGS ROUTE selects (component 07b's host-context): a shared HostPicker
// plus, when a remote host is selected, a READ-ONLY view of THAT host's own
// provider listing, read through evener/host/request.
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
import type { ReactNode } from "react";
import { EMPTY_HOST_INSTANCE_STATE, fetchHost, useHostInstances } from "../../../../stores/credentials";
import { isLocalHost } from "../../../../stores/hostRouting";
import { useHostsStore } from "../../../../stores/hosts";
import { EmptyState, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { HostPicker, isConfiguredHost } from "../../HostPicker";
import { useSettingsHost } from "../../settingsHost";
import { useConnectedEffect } from "../useConnectedEffect";
import styles from "./CredentialsHostScope.module.css";
import { CredentialsSection } from "./CredentialsSection";
import { ProviderInstanceGroups } from "./ProviderInstanceGroups";

const CLASS = {
  root: requireClass(styles.root, "CredentialsHostScope.module.css", "root"),
  remote: requireClass(styles.remote, "CredentialsHostScope.module.css", "remote"),
  heading: requireClass(styles.heading, "CredentialsHostScope.module.css", "heading"),
  note: requireClass(styles.note, "CredentialsHostScope.module.css", "note"),
  error: requireClass(styles.error, "CredentialsHostScope.module.css", "error"),
};

export interface CredentialsHostScopeProps {
  /** Forwarded to CredentialsSection so the local view keeps the section
   * dispatch contract (Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
}

export function CredentialsHostScope({ sectionId }: CredentialsHostScopeProps) {
  const { host } = useSettingsHost();
  const load = useHostsStore((state) => state.load);

  // The scope's body is one of three states; computed here rather than as a
  // nested ternary in the JSX.
  let body: ReactNode;
  if (isLocalHost(host)) {
    body = <CredentialsSection sectionId={sectionId} />;
  } else if (load.phase === "ready" && !isConfiguredHost(load, host)) {
    // Only the registry's own answer may call a host gone: while it is still
    // being read, the host's own partition (or its typed refusal) is shown
    // instead of a premature "no longer configured".
    body = (
      <p className={CLASS.note} role="status">
        Host {host} is no longer configured, so it has no provider listing to show.
      </p>
    );
  } else {
    body = <RemoteHostInstances host={host} />;
  }

  return (
    <div className={CLASS.root}>
      <HostPicker />
      {body}
    </div>
  );
}

/** RemoteHostInstances is the read-only view of one remote host's own provider
 * listing. It reads that host's partition through useHostInstances (component
 * 07b) and re-reads it through evener/host/request when the selection changes or
 * the connection returns - the same load path the spawn form's useProviderSetup
 * uses. Nothing here writes: the shared listing renders its read-only rows. */
function RemoteHostInstances({ host }: { host: string }) {
  const state = useHostInstances(host);
  useConnectedEffect(() => fetchHost(host), [host]);

  const title = `Providers on ${host}`;
  // hostPartition returns the frozen empty partition until that host has ever
  // been read (stores/credentials.ts), so identity against it is the honest
  // "nothing has been read yet" test - the skeleton is shown instead of a
  // premature "no instances" while the host's first answer is still out.
  const neverRead = state === EMPTY_HOST_INSTANCE_STATE;
  const empty = state.instances.length === 0;
  const pending = (neverRead || state.loading) && empty;
  return (
    <section className={CLASS.remote} aria-label={title}>
      <h3 className={CLASS.heading}>{title}</h3>
      <p className={CLASS.note}>Read-only. These are {host}'s own provider instances, not this hub's.</p>
      {pending && <Skeleton />}
      {state.error !== null && (
        <p className={CLASS.error}>
          Couldn't read providers from {host}: {state.error}
        </p>
      )}
      {!pending && state.error === null && empty && <EmptyState title={`No provider instances on ${host}.`} />}
      {!empty && (
        <ProviderInstanceGroups instances={state.instances} availableProviders={state.availableProviders} readOnly />
      )}
    </section>
  );
}
