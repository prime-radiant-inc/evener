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
import { useHostInstances } from "../../../../stores/credentials";
import { isLocalHost } from "../../../../stores/hostRouting";
import { hostsStore, isConfiguredHost, useHostsStore } from "../../../../stores/hosts";
import { Button, EmptyState, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { HostPicker } from "../../HostPicker";
import { useSettingsHost } from "../../settingsHost";
import styles from "./CredentialsHostScope.module.css";
import { CredentialsSection, Diagnostics } from "./CredentialsSection";
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
    body = <RemoteHostInstances host={host} registryFailed={load.phase === "error"} />;
  }

  return (
    <div className={CLASS.root}>
      <HostPicker />
      {body}
    </div>
  );
}

/** RemoteHostInstances is the read-only view of one remote host's own provider
 * listing. useHostInstances hands it the listing for the CURRENT registry
 * snapshot and issues the read when there is none (see that hook), so nothing
 * here knows about registry revisions. Nothing here writes: the shared listing
 * renders its read-only rows. */
function RemoteHostInstances({ host, registryFailed }: { host: string; registryFailed: boolean }) {
  const state = useHostInstances(host);
  const title = `Providers on ${host}`;
  // Rows are shown only once a read for the current registry snapshot has
  // succeeded, so an unanswered (or superseded) read can never pass for an empty
  // listing.
  const verified = state.read;
  const empty = state.instances.length === 0;
  const pending = !verified || (state.loading && empty);
  // The registry failed without ever naming this host, so nothing read here can be
  // verified against it: say so - rather than a permanent skeleton, which reads as
  // "still working" - and offer the registry's own read to retry. A host the
  // registry DID name keeps its verified listing when a later re-read fails,
  // because that failure leaves the registry revision where it was.
  const unverifiable = registryFailed && !verified;
  return (
    <section className={CLASS.remote} aria-label={title}>
      <h3 className={CLASS.heading}>{title}</h3>
      <p className={CLASS.note}>Read-only. These are {host}'s own provider instances, not this hub's.</p>
      {/* The host's own load warnings: a partial listing must say so rather than
          read as a complete one. */}
      {!unverifiable && verified && <Diagnostics diagnostics={state.diagnostics} />}
      {/* An error is an answer: the skeleton is for "nothing, and no failure,
          yet" - beside a refusal it would read as "still working". */}
      {state.error !== null && (
        <p className={CLASS.error}>
          Couldn't read providers from {host}: {state.error}
        </p>
      )}
      {state.error === null && unverifiable && (
        <EmptyState
          title={`Couldn't check ${host}'s registration`}
          hint="The hosts list didn't load, so this host's own provider listing can't be verified as still belonging to the name it was selected by. Retry to read the hosts list again."
          action={
            <Button size="sm" onClick={() => void hostsStore.getState().fetch()}>
              Retry
            </Button>
          }
        />
      )}
      {state.error === null && !unverifiable && pending && <Skeleton />}
      {!unverifiable && verified && !pending && state.error === null && empty && (
        <EmptyState title={`No provider instances on ${host}.`} />
      )}
      {!unverifiable && verified && !empty && (
        <ProviderInstanceGroups instances={state.instances} availableProviders={state.availableProviders} readOnly />
      )}
    </section>
  );
}
