// Settings -> Evener launch (#9): the global launch-config layer, via the
// schema-driven LaunchConfigForm (launchShared/). Sequential load (schema()
// then getLayer("/","global")) into a 2-state contract - unlike project.tsx's
// 3-state contract, a load failure has no distinct recoverable state, just a
// permanent failure message (parity-m7-settings.md §9).

import type { LaunchConfigDiagnostic, LaunchConfigLayer, LaunchOption } from "@evener/appwire-client";
import { friendlyErrorMessage } from "@evener/appwire-client";
import { useState } from "react";
import { LOCAL_HOST } from "../../../stores/hostRouting";
import { launchConfigStoreForHost } from "../../../stores/launchConfig";
import { Button } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { HostScopedSurface } from "./hostScopedSurface";
import styles from "./launchServer.module.css";
import type { LaunchFormPaths } from "./launchShared/fields";
import { LaunchConfigForm } from "./launchShared/LaunchConfigForm";
import { useHostScopedLoad } from "./useConnectedEffect";

const CLASS = {
  root: requireClass(styles.root, "launchServer.module.css", "root"),
  help: requireClass(styles.help, "launchServer.module.css", "help"),
  error: requireClass(styles.error, "launchServer.module.css", "error"),
  diagnostics: requireClass(styles.diagnostics, "launchServer.module.css", "diagnostics"),
  diagnosticsHeading: requireClass(styles.diagnosticsHeading, "launchServer.module.css", "diagnosticsHeading"),
  diagnosticsList: requireClass(styles.diagnosticsList, "launchServer.module.css", "diagnosticsList"),
};

type LoadState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; options: LaunchOption[]; current: LaunchConfigLayer };

function Diagnostics({ diagnostics }: { diagnostics: LaunchConfigDiagnostic[] }) {
  if (diagnostics.length === 0) return null;
  return (
    <div className={CLASS.diagnostics} role="status" aria-live="polite">
      <p className={CLASS.diagnosticsHeading}>Warnings</p>
      <ul className={CLASS.diagnosticsList}>
        {diagnostics.map((d, index) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: diagnostics are a flat, unordered warning list with no stable identity of their own (field+message can legitimately repeat across a resolve/save cycle)
          <li key={index}>{d.field ? `${d.field}: ${d.message}` : d.message}</li>
        ))}
      </ul>
    </div>
  );
}

export interface LaunchServerSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
  /** The host whose own launch defaults this section edits (component 07b).
   * Defaults to the local hub, so a direct render is today's local section. */
  host?: string;
}

/**
 * Evener launch defaults: applied to every evener spawn unless overridden by a
 * project layer or per-launch. Loads schema()+getLayer("/","global")
 * sequentially, then best-effort resolve("/") to seed the diagnostics panel
 * (failure there is swallowed - "non-fatal", matching the legacy exactly).
 * Save re-derives diagnostics from setLayer's OWN returned resolved config,
 * not a fresh resolve() call.
 */
export function LaunchServerSection({ host = LOCAL_HOST }: LaunchServerSectionProps) {
  // The launch-config gateway for the selected host: the controller's own store
  // for the local hub, a per-host instance (over evener/host/request) for a
  // remote one. Resolved per render; the instance is stable per host.
  const store = launchConfigStoreForHost(host);
  // The browse-assisted path fields the shared form renders (path scalars, the
  // prompt file sub-fields, and pathList add rows) list THIS host's filesystem,
  // not the controller's: same store, same seam as the validate/save calls
  // above (component 07b). evener/paths/complete and evener/path/validate are
  // both on the proxy allow-list.
  const paths: LaunchFormPaths = {
    directory: {
      validatePath: (path, kind) => store.getState().validatePath(path, kind),
      createDirectory: (path) => store.getState().createDirectory(path),
    },
    complete: (prefix, includeFiles) => store.getState().completePaths(prefix, includeFiles),
  };
  const [load, setLoad] = useState<LoadState>({ phase: "loading" });
  // A re-read of content this pane is ALREADY showing that failed. Kept apart
  // from `load` because it is not a load failure: the form (and the draft in it)
  // stays, and this is what says the values on screen may be out of date.
  const [refreshError, setRefreshError] = useState<string | null>(null);
  const [diagnostics, setDiagnostics] = useState<LaunchConfigDiagnostic[]>([]);
  // The effective layer of the same resolve() that seeds the diagnostics
  // panel: unset fields whose empty marker is generic prepend their entry
  // here ("high (default)"). Undefined until the resolve lands - and forever,
  // if it fails (the same non-fatal contract as the diagnostics).
  const [resolvedDefaults, setResolvedDefaults] = useState<LaunchConfigLayer | undefined>(undefined);

  // useHostScopedLoad (not a bare useEffect): a direct deep link to
  // /settings/launch-evener can mount this section before AppShell's own
  // connect() handshake finishes, and schema()/getLayer() both require a
  // connected client (throw otherwise) - see that hook's own doc comment - and
  // it re-runs this read when the selected host COMES BACK, not only when it
  // changes. isCancelled guards the same "component unmounted mid-load" case the
  // legacy local `cancelled` flag did.
  const { reload, retryLoad } = useHostScopedLoad(
    host,
    async (blank, isCancelled) => {
      // Different content - a host switch, or a re-registration under the same
      // name - reloads: the previous host's form must not stand while the new
      // host's schema/layer are in flight. Clearing first is a no-op on the local
      // mount, whose initial state is already "loading". A re-read of THIS host
      // (a reconnect, blank === false) keeps the form on screen - and the draft
      // the user has typed into it.
      if (blank) {
        setLoad({ phase: "loading" });
        setDiagnostics([]);
        setResolvedDefaults(undefined);
        setRefreshError(null);
      }
      try {
        const [schema, current, resolved] = await Promise.all([
          store.getState().schema(),
          store.getState().getLayer("/", "global"),
          store
            .getState()
            .resolve("/")
            .catch(() => null),
        ]);
        if (isCancelled()) return false;
        setLoad({ phase: "ready", options: schema.options, current });
        setRefreshError(null);
        // resolve() is best effort: a null answer means it failed, so the derived
        // panel must not keep the previous run's warnings and inherited-value
        // labels as though they described this read.
        setDiagnostics(resolved?.diagnostics ?? []);
        setResolvedDefaults(resolved?.effective);
        return true;
      } catch (err) {
        // `false` tells the hook this run did NOT put content on screen, so the
        // next run of the same content is a first look rather than a re-read -
        // which is what lets the load error's Retry below replace itself.
        if (isCancelled()) return false;
        // Content this pane is not already showing has nothing to keep: the
        // failure is the load's own, and Retry below is what ends it.
        if (blank) {
          setLoad({ phase: "error", message: friendlyErrorMessage(err) });
          return false;
        }
        // A failed REFRESH keeps the form - and the draft in it - and reports
        // itself beside it: this host is attached (that is why we re-read it) and
        // may stay attached, so there is no next attach to wait for, and a pane
        // sitting silently on values that may be out of date is a worse answer
        // than saying so with a way to ask again.
        setRefreshError(friendlyErrorMessage(err));
        return false;
      }
    },
    [store],
  );

  return (
    <div className={CLASS.root}>
      <h2>Evener launch defaults</h2>
      <p className={CLASS.help}>
        These values are applied to every evener spawn unless overridden by a project layer or per-launch.
      </p>
      {load.phase === "loading" && <p className={CLASS.help}>Loading launch settings…</p>}
      {load.phase === "error" && (
        <p className={CLASS.error}>
          Failed to load launch settings. {load.message}{" "}
          <Button type="button" onClick={() => retryLoad()}>
            Retry
          </Button>
        </p>
      )}
      {load.phase === "ready" && (
        <>
          {refreshError !== null && (
            <p className={CLASS.error} role="status">
              Could not re-read this host's launch settings. {refreshError}{" "}
              <Button type="button" onClick={() => reload()}>
                Retry
              </Button>
            </p>
          )}
          <Diagnostics diagnostics={diagnostics} />
          <LaunchConfigForm
            options={load.options}
            layer="global"
            current={load.current}
            resolvedDefaults={resolvedDefaults}
            successToast="Launch defaults saved"
            validatePath={(path, kind) => store.getState().validatePath(path, kind)}
            paths={paths}
            host={host}
            draftOwner={store}
            onSave={(config) => store.getState().setLayer("/", "global", config)}
            onSaved={(resolved) => {
              setDiagnostics(resolved.diagnostics ?? []);
              setResolvedDefaults(resolved.effective);
            }}
          />
        </>
      )}
    </div>
  );
}

/** LaunchServerHostScope is the launch-evener settings section scoped to the
 * settings route's selected host (component 07b): the one shared HostPicker
 * plus LaunchServerSection, which resolves its launch-config gateway from the
 * host. Local renders today's section byte-for-byte; a remote host's schema,
 * layers and path helpers all go through evener/host/request. */
export function LaunchServerHostScope({ sectionId }: LaunchServerSectionProps) {
  return <HostScopedSurface>{(host) => <LaunchServerSection sectionId={sectionId} host={host} />}</HostScopedSurface>;
}
