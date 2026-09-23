// CredentialsHostScope is the credentials settings section, scoped to the host
// the SETTINGS ROUTE selects (component 07b's host-context): a shared HostPicker
// plus, when a remote host is selected, a view of THAT host's own provider
// listing, read through evener/host/request. The listing stays read-only, with
// one deliberate exception: component 07d's "Sign in on host" for a Codex
// instance, which runs the device-code flow on that host.
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
import { errorText } from "@evener/appwire-client";
import { type ReactNode, useCallback, useState } from "react";
import { useConnectionStore } from "../../../../stores/connection";
import { deviceStartOnHost, fetchHost, useHostInstances } from "../../../../stores/credentials";
import { isLocalHost } from "../../../../stores/hostRouting";
import { useHostsStore } from "../../../../stores/hosts";
import { EmptyState, Skeleton } from "../../../../widgets";
import { requireClass } from "../../../../widgets/internal/requireClass";
import { HostPicker, isConfiguredHost, useHostIdentityFor } from "../../HostPicker";
import { useSettingsHost } from "../../settingsHost";
import { useConnectedEffect } from "../useConnectedEffect";
import styles from "./CredentialsHostScope.module.css";
import { CredentialsSection } from "./CredentialsSection";
import { DeviceCodeDialog } from "./oauthDialogs";
import { ProviderInstanceGroups } from "./ProviderInstanceGroups";
import { useEditorLifetime } from "./useEditorLifetime";

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
  // The identity the registry gives the selected name, kept across a registry
  // re-read that has not answered yet (see the hook's own note).
  const identity = useHostIdentityFor(load, host);

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
    body = <RemoteHostInstances host={host} identity={identity} />;
  }

  return (
    <div className={CLASS.root}>
      <HostPicker />
      {body}
    </div>
  );
}

/** HostSignInEditor is one in-progress "Sign in on host" device flow for a Codex
 * instance of the selected remote host. */
interface HostSignInEditor {
  name: string;
  flowId: string;
  userCode: string;
  verificationUrl: string;
  intervalSeconds: number;
}

/** RemoteHostInstances is the view of one remote host's own provider listing.
 * It reads that host's partition through useHostInstances (component 07b) and
 * re-reads it through evener/host/request - the same load path the spawn form's
 * useProviderSetup uses - whenever the selection, the registry's identity for
 * the name, or the CONNECTION changes. Its rows stay read-only; the ONE action
 * it offers is component 07d's "Sign in on host" for a Codex instance, which
 * drives the device-code flow on that host (see beginHostSignIn). */
function RemoteHostInstances({ host, identity }: { host: string; identity: string | null }) {
  const state = useHostInstances(host);
  const { client, state: connection } = useConnectionStore();
  const [signIn, setSignIn] = useState<HostSignInEditor | null>(null);
  const [signInError, setSignInError] = useState<string | null>(null);
  // Async start work may finish after the host changes or the pane unmounts;
  // only a live editor owns its feedback.
  const active = useEditorLifetime();
  // Stable identities, as CredentialsSection's own closeEditor is: the
  // DeviceCodeDialog's poll effect depends on the onSuccess/onCancel it is
  // given, and an unstable reference would restart that dialog's timer on every
  // RemoteHostInstances re-render - and, once an authorized poll's refetch
  // re-renders the pane, would cancel that poll's own continuation before it
  // could report the sign-in.
  const closeSignIn = useCallback(() => setSignIn(null), []);

  // beginHostSignIn starts the device-code flow ON `host`, through
  // evener/host/request (stores/credentials.ts's deviceStartOnHost). The code
  // and verification URL it returns are shown in the shared DeviceCodeDialog,
  // which is pointed at the same host for polling. Its failures are named, never
  // a silent dead end: an unattached host, a refused call, or a host whose
  // client offers no device flow each become a real message on screen.
  const beginHostSignIn = useCallback(
    async (name: string): Promise<void> => {
      setSignInError(null);
      try {
        const resp = await deviceStartOnHost(host, name);
        if (!active.current) return;
        if (resp.fallback) {
          // The host's client offered no device flow, so the only alternative is
          // a browser redirect - and there is no browser on the host. Say so.
          setSignInError(
            `Device-code sign-in is not enabled on ${host}. Set EVENER_LOGIN_HEADLESS=1 on the host and try again.`,
          );
          return;
        }
        setSignIn({
          name,
          flowId: resp.flowId,
          userCode: resp.userCode,
          verificationUrl: resp.verificationUrl,
          intervalSeconds: resp.intervalSeconds,
        });
      } catch (err) {
        if (!active.current) return;
        setSignInError(`Couldn't start sign-in on ${host}: ${errorText(err)}`);
      }
    },
    [host, active],
  );
  // The connection belongs in the deps because useConnectedEffect's started flag
  // is per-effect: with `host` alone, a transition released the in-flight read's
  // status and nothing ever re-issued it, so an unanswered read settled as an
  // empty listing and a reconnect kept pre-disconnect rows. The identity belongs
  // there too: a name re-registered as a different host is a different listing to
  // read, not the cached one.
  useConnectedEffect(() => fetchHost(host, identity), [host, identity, client, connection]);

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
      <p className={CLASS.note}>
        Read-only, except that a Codex instance can sign in on {host} itself. These are {host}'s own provider instances,
        not this hub's.
      </p>
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
        <ProviderInstanceGroups
          instances={state.instances}
          availableProviders={state.availableProviders}
          readOnly
          onHostSignIn={(name) => void beginHostSignIn(name)}
        />
      )}
      {signInError !== null && (
        <p className={CLASS.error} role="alert">
          {signInError}
        </p>
      )}
      {signIn !== null && (
        <DeviceCodeDialog
          key={signIn.flowId}
          name={signIn.name}
          flowId={signIn.flowId}
          userCode={signIn.userCode}
          verificationUrl={signIn.verificationUrl}
          intervalSeconds={signIn.intervalSeconds}
          host={host}
          onCancel={closeSignIn}
          onSuccess={closeSignIn}
          onRestart={() => void beginHostSignIn(signIn.name)}
        />
      )}
    </section>
  );
}
