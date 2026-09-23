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
import { useConnectionStore } from "../../../../stores/connection";
import { fetchHost, useHostInstances } from "../../../../stores/credentials";
import { isLocalHost } from "../../../../stores/hostRouting";
import { useHostsStore } from "../../../../stores/hosts";
import { EmptyState, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { HostPicker, isConfiguredHost, useHostRegistryFacts } from "../../HostPicker";
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
  // What the registry says about the selected name, kept across a registry
  // re-read that has not answered yet (see the hook's own note): the identity its
  // cached rows are keyed on, and its attachment state as the retry trigger.
  const { identity, attached } = useHostRegistryFacts(load, host);

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
    body = <RemoteHostInstances host={host} identity={identity} attached={attached} />;
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
 * 07b) and re-reads it through evener/host/request - the same load path the
 * spawn form's useProviderSetup uses - whenever the selection, the registry's
 * identity for the name, or the CONNECTION changes. Nothing here writes: the
 * shared listing renders its read-only rows. */
function RemoteHostInstances({
  host,
  identity,
  attached,
}: {
  host: string;
  identity: string | null;
  attached: boolean;
}) {
  const state = useHostInstances(host);
  const { client, state: connection } = useConnectionStore();
  // The connection belongs in the deps because useConnectedEffect's started flag
  // is per-effect: with `host` alone, a transition released the in-flight read's
  // status and nothing ever re-issued it, so an unanswered read settled as an
  // empty listing and a reconnect kept pre-disconnect rows. The identity belongs
  // there too: a name re-registered as a different host is a different listing to
  // read, not the cached one.
  //
  // The ATTACHMENT state belongs there as well, and only there: an offline host
  // that fails and later attaches changes live registry data and nothing else, so
  // without this trigger its failed listing would never be retried and would sit
  // there with no retry control. It is deliberately NOT part of the identity the
  // rows are keyed on (see hosts.ts's hostIdentity): folding live state in would
  // invalidate a perfectly good listing on every detach.
  useConnectedEffect(() => fetchHost(host, identity), [host, identity, attached, client, connection]);

  const title = `Providers on ${host}`;
  // Rows are this host's only when they were read under the identity the
  // registry gives the name now. readIdentity is recorded on a SUCCESSFUL read,
  // so an unanswered (or transition-orphaned) read never passes for an empty
  // listing; a null identity - the registry still being read - proves no
  // mismatch and keeps the rows already verified for this name.
  const verified = state.readIdentity !== null && (identity === null || state.readIdentity === identity);
  const empty = state.instances.length === 0;
  const pending = !verified || (state.loading && empty);
  return (
    <section className={CLASS.remote} aria-label={title}>
      <h3 className={CLASS.heading}>{title}</h3>
      <p className={CLASS.note}>Read-only. These are {host}'s own provider instances, not this hub's.</p>
      {/* An error is an answer: the skeleton is for "nothing, and no failure,
          yet" - beside a refusal it would read as "still working". */}
      {pending && state.error === null && <Skeleton />}
      {state.error !== null && (
        <p className={CLASS.error}>
          Couldn't read providers from {host}: {state.error}
        </p>
      )}
      {verified && !pending && state.error === null && empty && (
        <EmptyState title={`No provider instances on ${host}.`} />
      )}
      {verified && !empty && (
        <ProviderInstanceGroups instances={state.instances} availableProviders={state.availableProviders} readOnly />
      )}
    </section>
  );
}
