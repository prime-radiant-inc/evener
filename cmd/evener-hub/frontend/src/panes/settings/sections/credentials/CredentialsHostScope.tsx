// CredentialsHostScope is the credentials settings section, scoped to the host
// the SETTINGS ROUTE selects (component 07b's host-context): a shared HostPicker
// plus, when a remote host is selected, a READ-ONLY view of THAT host's own
// provider listing, read through evener/host/request, and the credential-PUSH
// action (component 07c) that copies this hub's local keys to that host.
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
import type { HostPushCredentialsResponse } from "@evener/appwire-client";
import { errorText } from "@evener/appwire-client";
import { type ReactNode, useState } from "react";
import { pushHostCredentials, retryHostRead, useHostInstances } from "../../../../stores/credentials";
import { isLocalHost } from "../../../../stores/hostRouting";
import { isConfiguredHost, useHostsStore } from "../../../../stores/hosts";
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
  push: requireClass(styles.push, "CredentialsHostScope.module.css", "push"),
  report: requireClass(styles.report, "CredentialsHostScope.module.css", "report"),
  reportList: requireClass(styles.reportList, "CredentialsHostScope.module.css", "reportList"),
  reportRow: requireClass(styles.reportRow, "CredentialsHostScope.module.css", "reportRow"),
  reportInstance: requireClass(styles.reportInstance, "CredentialsHostScope.module.css", "reportInstance"),
  reportAction: requireClass(styles.reportAction, "CredentialsHostScope.module.css", "reportAction"),
  reportReason: requireClass(styles.reportReason, "CredentialsHostScope.module.css", "reportReason"),
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
 * listing, plus that host's push action. useHostInstances hands it the listing
 * for the CURRENT registry snapshot and issues the read when there is none (see
 * that hook), so nothing here knows about registry revisions. Nothing in the
 * listing writes: the shared listing renders its read-only rows, and the push
 * action below it is the one write the surface offers. */
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
    <>
      <section className={CLASS.remote} aria-label={title}>
        <h3 className={CLASS.heading}>{title}</h3>
        <p className={CLASS.note}>Read-only. These are {host}'s own provider instances, not this hub's.</p>
        {/* The host's own load warnings: a partial listing must say so rather than
            read as a complete one. */}
        {!unverifiable && verified && <Diagnostics diagnostics={state.diagnostics} />}
        {/* An error is an answer: the skeleton is for "nothing, and no failure,
            yet" - beside a refusal it would read as "still working". */}
        {/* A failure is an answer the user can act on: re-read the host (and the
            registry, when its read is what failed - see retryHostRead), so the pane
            never sits on an error with no way out. */}
        {state.error !== null && !unverifiable && (
          <>
            <p className={CLASS.error}>
              Couldn't read providers from {host}: {state.error}
            </p>
            <Button size="sm" onClick={() => retryHostRead(host)}>
              Retry
            </Button>
          </>
        )}
        {unverifiable && (
          <EmptyState
            title={`Couldn't check ${host}'s registration`}
            hint="The hosts list didn't load, so this host's own provider listing can't be verified as still belonging to the name it was selected by. Retry to read the hosts list again."
            action={
              <Button size="sm" onClick={() => retryHostRead(host)}>
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
      {/* Keyed on the host, and withheld while the host is unverifiable: the
          action holds its whole lifetime in local state, so without the key a
          switch would leave the previous host's pending state, report and error
          on screen - and the failure branch names the CURRENT host, which would
          make a late failure read as though the new host produced it. The
          unverifiable case is the same guard the listing above takes: this
          action sends this hub's keys, so offering it for a name the registry
          cannot confirm is exactly what the guard exists to prevent. */}
      {!unverifiable && <PushCredentials key={host} host={host} />}
    </>
  );
}

// PushState is the action's one lifetime: idle until fired, pending while the
// controller's call is out, then either the RESPONSE's own report or the
// failure's text. The report is held from the response and never assembled from
// anything captured before the write.
type PushState =
  | { phase: "idle" }
  | { phase: "pending" }
  | { phase: "report"; response: HostPushCredentialsResponse }
  | { phase: "failed"; message: string };

/** PushCredentials is the credential-push action on the remote-credentials
 * surface (component 07c): it copies THIS hub's local provider-instance keys to
 * the selected remote host and renders that host's own per-entry report. It is
 * only rendered for a remote selection - the local hub's store IS the push's
 * source, so a push targeting it is meaningless. No key value can be read,
 * rendered, or logged here: the wire carries none, and this code touches only
 * the report's instance/action/reason. */
function PushCredentials({ host }: { host: string }) {
  const [push, setPush] = useState<PushState>({ phase: "idle" });

  async function handlePush(): Promise<void> {
    setPush({ phase: "pending" });
    try {
      setPush({ phase: "report", response: await pushHostCredentials(host) });
    } catch (err) {
      // An unattached or unknown host, and a refused call, are real failures:
      // they surface here rather than as an empty report that would read like a
      // successful push of nothing.
      setPush({ phase: "failed", message: errorText(err) });
    }
  }

  return (
    <div className={CLASS.push}>
      <Button size="sm" variant="secondary" disabled={push.phase === "pending"} onClick={() => void handlePush()}>
        Push credentials to {host}
      </Button>
      <p className={CLASS.note}>
        Copies this hub's own provider-instance keys to {host}. A key {host} cannot take is reported, not forced.
      </p>
      {push.phase === "failed" && (
        <p className={CLASS.error} role="alert">
          Couldn't push credentials to {host}: {push.message}
        </p>
      )}
      {push.phase === "report" && <PushReport response={push.response} />}
    </div>
  );
}

/** PushReport renders one row per result of the push response: the host's own
 * action verbatim, with its reason when the wire carried one. The entries are
 * rendered as they are - a mix of some skipped and one failed stays a mix
 * rather than collapsing into one aggregate success - and an action string this
 * build does not know (the wire types it as a plain string, so a newer host may
 * introduce one) is shown exactly as sent rather than mapped onto a label that
 * means something else. */
function PushReport({ response }: { response: HostPushCredentialsResponse }) {
  const label = `Push report for ${response.host}`;
  if (response.results.length === 0) {
    return (
      <p className={CLASS.note} role="status" aria-label={label}>
        Nothing to push to {response.host}: this hub has no provider-instance keys.
      </p>
    );
  }
  return (
    <div className={CLASS.report} role="status" aria-label={label}>
      <ul className={CLASS.reportList}>
        {response.results.map((result) => (
          <li key={result.instance} className={CLASS.reportRow}>
            <span className={CLASS.reportInstance}>{result.instance}</span>
            <span className={CLASS.reportAction}>{result.action}</span>
            {result.reason !== undefined && result.reason !== "" && (
              <span className={CLASS.reportReason}>{result.reason}</span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
