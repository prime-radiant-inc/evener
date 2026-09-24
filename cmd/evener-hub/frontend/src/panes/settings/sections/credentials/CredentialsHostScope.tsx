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
import { errorText, RequestTimeoutError } from "@evener/appwire-client";
import { type ReactNode, useState } from "react";
import {
  connectionGeneration,
  pushHostCredentials,
  retryHostRead,
  useConnectionGeneration,
  useHostInstances,
} from "../../../../stores/credentials";
import { isLocalHost } from "../../../../stores/hostRouting";
import { hostInstanceIdentity, isConfiguredHost, useHostsStore } from "../../../../stores/hosts";
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
    body = (
      <RemoteHostInstances
        host={host}
        registryFailed={load.phase === "error"}
        // Whether the registry's CURRENT answer names the host - the same
        // predicate the branch above decides the body with, so the two can never
        // disagree about what "the registry names this host" means. The body
        // renders without it (a listing already read under this revision is the
        // registry's own answer and stays on screen), but the WRITE below does
        // not run without it: see RemoteHostInstances.
        registryNamesHost={load.phase === "ready" && isConfiguredHost(load, host)}
      />
    );
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
function RemoteHostInstances({
  host,
  registryFailed,
  registryNamesHost,
}: {
  host: string;
  registryFailed: boolean;
  /** Whether the registry's current, READY snapshot lists this host. The
   * registry has to have ANSWERED: an unanswered one says nothing about the
   * name, which is why absence from it is not read as much here as a refusal
   * (CredentialsHostScope's own branch decides that case). */
  registryNamesHost: boolean;
}) {
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
      {/* Keyed on the host's REGISTRATION identity, and never withheld for a
          registry read - not even a FAILED one. The action holds its whole
          lifetime in local state, so the key is what stops that state outliving
          the host it belongs to - and it must be the identity rather than the
          name: a host re-registered under the same name is a different
          registration whose own push state is its own, and a name-keyed
          component would leave the previous registration's report standing under
          it (the same `hostInstanceIdentity` the host-scoped stores and the
          shared frame key on).
          Whether the host can be confirmed holds the BUTTON, never the mount:
          `registryNamesHost` (the registry's current answer names this host) and
          the listing's own `unverifiable` (the registry's read failed with
          nothing verified against it) travel INTO the action for exactly that. A
          registry re-read that fails must not lose a report on screen, an
          unknown-outcome warning, or an in-flight attempt's outcome - the blind
          re-push over a mutation that may already have landed is the harm this
          feature's own states exist to prevent - and the write stays prevented
          because the button is held either way.
          The host registration is one of the action's two ties; the CONNECTION
          its request goes out on is the other, and it is held inside the action
          rather than in this key - see PushCredentials, which must not lose an
          in-flight mutation's outcome to a remount. */}
      <PushCredentials
        key={`host:${hostInstanceIdentity(host)}`}
        host={host}
        registryNamesHost={registryNamesHost}
        unverifiable={unverifiable}
      />
    </>
  );
}

// PushOutcome is what one completed attempt settled on. The RESPONSE's own
// report is held as it arrived and never assembled from anything captured before
// the write. Every outcome carries the CONNECTION GENERATION it settled on
// (stores/credentials.ts's connectionGeneration): a push is a mutation, so its
// outcome is one connection's, and it is readable only while that connection
// still is the page's - see PushCredentials.
type PushOutcome =
  | { phase: "report"; generation: number; response: HostPushCredentialsResponse }
  | { phase: "failed"; generation: number; message: string }
  // The client's own deadline expired with the request on the wire. The host may
  // have applied the keys and may not have: an unknown outcome, which is neither
  // the host's refusal nor a report, and has no text of its own to show - the
  // client's "timed out after Nms" names a class and a method, not what the host
  // did.
  | { phase: "unknown"; generation: number };

// PushState is the action's one lifetime: idle until fired, pending while the
// controller's call is out, then the outcome above.
type PushState = { phase: "idle" } | { phase: "pending"; generation: number } | PushOutcome;

/** pushOutcome narrows a push state to the settled outcome it holds, or null
 * while it holds none (idle, or still in flight). */
function pushOutcome(state: PushState): PushOutcome | null {
  switch (state.phase) {
    case "report":
    case "failed":
    case "unknown":
      return state;
    default:
      return null;
  }
}

/** PushCredentials is the credential-push action on the remote-credentials
 * surface (component 07c): it copies THIS hub's local provider-instance keys to
 * the selected remote host and renders that host's own per-entry report. It is
 * only rendered for a remote selection - the local hub's store IS the push's
 * source, so a push targeting it is meaningless. No key value can be read,
 * rendered, or logged here: the wire carries none, and this code touches only
 * the report's instance/action/reason.
 *
 * Its state belongs to the host registration above (the key) AND to the
 * CONNECTION its request goes out on: a push is a mutation, so the answer that
 * arrives after that connection was replaced describes a hub this page is no
 * longer wired to. That settlement is dropped rather than rendered as the
 * replacement connection's outcome, and so is one that had already been
 * RENDERED when the replacement happened: every outcome carries its generation,
 * and an outcome whose connection is gone is withheld (never silently reset,
 * and never left standing over a listing that now describes another connection)
 * with the reason said out loud. An attempt reads as an outcome this connection
 * never saw - never as a silent retry, and never as a report the user cannot
 * tell the provenance of.
 *
 * The button is held for anything that leaves this host unconfirmed: the
 * registry has not answered naming it (`registryNamesHost`), or its own read
 * FAILED with nothing verified to check the name against (`unverifiable`, the
 * listing's own state). Either way this sends this hub's keys, so it runs only
 * while the registry's own listing says this host is still a host. The MOUNT is
 * deliberately keyed on neither - see the call site - so a registry re-read
 * cannot remount the action out from under a pending attempt or a report the
 * user is reading. */
function PushCredentials({
  host,
  registryNamesHost,
  unverifiable,
}: {
  host: string;
  registryNamesHost: boolean;
  /** The listing's own state: the registry's read FAILED and nothing here was
   * verified against it, so no answer confirms this name is still a configured
   * host. The button is held - the action, and whatever state it holds, stays
   * mounted (see the call site). */
  unverifiable: boolean;
}) {
  const [push, setPush] = useState<PushState>({ phase: "idle" });
  // The connection this action is on, subscribed rather than read once: a
  // replacement is what makes an in-flight attempt's outcome unknowable, and
  // the render that follows it is where that becomes readable.
  const generation = useConnectionGeneration();
  // The attempt that is still THIS connection's, and the one that outlived the
  // connection it was issued on. A replaced connection's attempt is neither
  // this one's success nor its failure: the notice below says so, and the
  // button is live again so sending the keys a second time stays the user's
  // decision - never a silent retry of a mutation.
  const pending = push.phase === "pending" && push.generation === generation;
  const orphaned = push.phase === "pending" && push.generation !== generation;
  // A settled outcome is READABLE only on the connection that produced it. The
  // listing this action sits under is re-read for the connection in use now
  // (useHostInstances keys on the generation), so an outcome from a connection
  // that is gone would pair a fresh listing with a report of what a different
  // hub did. It is withheld - held, not deleted - so the render after a
  // replacement says why the thing the user was reading is no longer there.
  const outcome = pushOutcome(push);
  const shown = outcome !== null && outcome.generation === generation ? outcome : null;
  const staleOutcome = outcome !== null && outcome.generation !== generation;
  // The registry cannot confirm this host right now: it has not answered naming
  // it yet (`registryNamesHost`), or its own read failed with nothing verified
  // to check the name against (`unverifiable`). Either way this button sends
  // this hub's keys, so the write is held - the state above, and everything it
  // is holding, is not.
  const held = !registryNamesHost || unverifiable;

  async function handlePush(): Promise<void> {
    // Read from the store at call time, in the same turn the call resolves its
    // client (pushHostCredentials -> requireClient): the attempt is stamped
    // with the connection the request actually goes out on, never with the
    // render's own value, which a replacement that has not re-rendered yet
    // would leave behind - and a request on the NEW client would then be
    // compared against the OLD generation and dropped, hiding a report that is
    // this connection's own.
    const attempt = connectionGeneration();
    setPush({ phase: "pending", generation: attempt });
    try {
      const response = await pushHostCredentials(host);
      // The connection this push went out on is gone: its answer describes a
      // hub this page is no longer wired to, so it is dropped rather than shown
      // as the outcome of the connection that replaced it.
      if (connectionGeneration() !== attempt) return;
      setPush({ phase: "report", generation: attempt, response });
    } catch (err) {
      // A rejection from a replaced connection is not this connection's failure
      // either - it is dropped with the report above, and the render that follows
      // says what the attempt is. That covers a socket drop: the transport FAILS
      // the in-flight call (AppwireClient's handleSocketLoss), and the connection
      // transition that loss causes has already moved the generation by the time
      // this runs (and where it has not, the failure is withheld on the render
      // that follows it, for the same reason).
      if (connectionGeneration() !== attempt) return;
      // What is left is the client's own deadline expiring with the request on
      // the wire (RequestTimeoutError). A push is many sequential remote writes
      // over the host link (see pushHostCredentials), so the client giving up is
      // not the host refusing: whether the host applied the keys - all of them, some
      // of them, none - is exactly what nobody here can know. It reads as an
      // unknown outcome, and the way to send the keys again is a separate,
      // deliberate control below rather than the same button over a mutation
      // that may already have landed.
      if (err instanceof RequestTimeoutError) {
        setPush({ phase: "unknown", generation: attempt });
        return;
      }
      // Every other rejection names what happened: an unattached or unknown
      // host, and a refused call, are the host's own answer, and a client that
      // never sent the call is the client's. They surface as failures rather than
      // as an empty report that would read like a successful push of nothing.
      setPush({ phase: "failed", generation: attempt, message: errorText(err) });
    }
  }

  return (
    <div className={CLASS.push}>
      <Button size="sm" variant="secondary" disabled={pending || held} onClick={() => void handlePush()}>
        {/* An unknown outcome is not re-offered as the action that produced it:
            the same click over a mutation that may already have landed is the
            blind retry this state exists to stop, and the warning beside it is
            what makes a second send a decision. */}
        {shown?.phase === "unknown" ? `Push credentials to ${host} again` : `Push credentials to ${host}`}
      </Button>
      <p className={CLASS.note}>
        Copies this hub's own provider-instance keys to {host}. A key {host} cannot take is reported, not forced.
      </p>
      {/* The registry cannot confirm this host, so what this button sends has no
          confirmed target: held until its listing says otherwise. The two
          reasons are told apart because they are different states of the
          registry - no answer yet, and a read that failed. */}
      {unverifiable && (
        <p className={CLASS.note}>
          The hosts list couldn't be read, so nothing here confirms {host} is still a configured host: the push is held
          until the hosts list answers.
        </p>
      )}
      {!unverifiable && !registryNamesHost && (
        <p className={CLASS.note}>
          The hosts list has no answer for {host} yet, and this sends this hub's keys: the push is offered only while
          that listing names {host}.
        </p>
      )}
      {shown?.phase === "failed" && (
        <p className={CLASS.error} role="alert">
          Couldn't push credentials to {host}: {shown.message}
        </p>
      )}
      {shown?.phase === "unknown" && (
        <p className={CLASS.error} role="alert">
          The push to {host} didn't answer before this page's deadline, so its outcome is not known here: {host} may
          have applied the keys already. Check what {host} now holds, and send them again only if you mean to - a second
          push can apply them twice or overwrite what {host} has taken since.
        </p>
      )}
      {orphaned && (
        <p className={CLASS.error} role="alert">
          The hub connection was replaced while the push to {host} was in flight, so its outcome is not known here: this
          connection never saw the result. Push again only if you mean to send the keys again.
        </p>
      )}
      {/* The other half of the same rule: an outcome that HAD rendered is not the
          new connection's either. Withheld (the state is kept, so nothing is
          silently forgotten) and named, rather than left standing over a listing
          that has already been re-read for the connection in use now. */}
      {staleOutcome && (
        <p className={CLASS.error} role="alert">
          The hub connection was replaced after the push to {host} settled, so the connection in use now never saw its
          outcome: it is not shown here. Push again only if you mean to send the keys again.
        </p>
      )}
      {shown?.phase === "report" && <PushReport response={shown.response} />}
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
