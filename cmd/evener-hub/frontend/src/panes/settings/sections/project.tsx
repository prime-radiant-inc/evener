// Settings -> Per-project launch overrides (#18), reached via
// /settings/project?cwd=<dir> - never through the settings-nav list (see
// sections.ts's own comment; this "project" section id is deliberately
// absent from SETTINGS_SECTIONS). Shares launchServer.tsx's engine
// (LaunchConfigForm) but with the 3-state loaded contract (loading/error/
// ready, vs. launch-evener's 2-state) and no diagnostics panel at all
// (parity-m7-settings.md §18).
//
// Deliberate scope decision (this task's own report): when no `cwd` is
// present, this renders a plain instructional message rather than the
// legacy's project-picker list. That picker needs a {name, cwd} pair per
// known project; the only available RPC, evener/projects/recent, returns bare
// cwd strings with no name field, and isn't part of this task's assigned
// wire ground truth - reproducing the legacy list faithfully isn't possible
// with what's in scope here.
//
// cwd is read directly from window.location.search rather than through the
// pane-params system: PaneProps<SettingsPaneParams> only carries `section`
// (routing.ts's urlToPane/paneToURL have no query-string concept at all),
// and extending that shared, cross-stream contract is out of this stream's
// manifest. A popstate listener keeps it in sync with in-app navigation
// (routing.ts's own navigate() dispatches popstate on every push, the same
// signal AppShell itself listens for).

import type { LaunchConfigLayer, LaunchOption } from "@evener/appwire-client";
import { friendlyErrorMessage } from "@evener/appwire-client";
import { useEffect, useState } from "react";
import { LOCAL_HOST } from "../../../stores/hostRouting";
import { launchConfigStoreForHost } from "../../../stores/launchConfig";
import { requireClass } from "../../../widgets/internal/requireClass";
import { HostScopedSurface } from "./hostScopedSurface";
import type { LaunchFormPaths } from "./launchShared/fields";
import { LaunchConfigForm } from "./launchShared/LaunchConfigForm";
import styles from "./project.module.css";
import { useConnectedEffect } from "./useConnectedEffect";

const CLASS = {
  root: requireClass(styles.root, "project.module.css", "root"),
  help: requireClass(styles.help, "project.module.css", "help"),
  error: requireClass(styles.error, "project.module.css", "error"),
};

function readQueryCwd(): string {
  return new URLSearchParams(window.location.search).get("cwd") ?? "";
}

function useQueryCwd(): string {
  const [cwd, setCwd] = useState(readQueryCwd);
  useEffect(() => {
    function onPopState() {
      setCwd(readQueryCwd());
    }
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);
  return cwd;
}

type LoadState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; options: LaunchOption[]; current: LaunchConfigLayer; globalDefaults: LaunchConfigLayer };

export interface ProjectSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
  /** The host whose own project layer this section edits (component 07b).
   * Defaults to the local hub, so a direct render is today's local section. */
  host?: string;
}

/**
 * Per-project launch overrides: layered on top of the global Evener launch
 * defaults. Only fields set here override the global ones. The global layer
 * is fetched read-only, purely to drive the "default: {value}" inline hints
 * - this page never writes it.
 */
export function ProjectSection({ host = LOCAL_HOST }: ProjectSectionProps) {
  const cwd = useQueryCwd();
  // The launch-config gateway for the selected host: the controller's own store
  // for the local hub, a per-host instance (over evener/host/request) for a
  // remote one. Resolved per render; the instance is stable per host. Every read
  // and write below - schema/getLayer/resolve, the path validation the form
  // runs, and setLayer - goes through it, so a remote selection edits THAT
  // host's project layer rather than this hub's (component 07b). The cwd stays
  // in the route's ?cwd= query; the host comes from the settings route.
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
  // The effective layer of a best-effort resolve(cwd): unset fields whose
  // empty marker is generic prepend their entry here ("high (use global
  // default)"). Undefined until the resolve lands, on a resolve failure, and
  // cleared on every cwd change so one project's labels never leak into the
  // next project's form.
  const [resolvedDefaults, setResolvedDefaults] = useState<LaunchConfigLayer | undefined>(undefined);

  // useConnectedEffect (not a bare useEffect): a direct deep link to
  // /settings/project?cwd= can mount this section before AppShell's own
  // connect() handshake finishes, and schema()/getLayer() both require a
  // connected client (throw otherwise) - see that hook's own doc comment.
  // isCancelled guards the same "component unmounted (or cwd/host changed)
  // mid-load" case the legacy local `cancelled` flag did. The deps include the
  // store, so a host switch reloads: a []-deps effect could not.
  useConnectedEffect(
    async (isCancelled) => {
      if (!cwd) return;
      setLoad({ phase: "loading" });
      setResolvedDefaults(undefined);
      try {
        const [schema, current, globalDefaults, resolved] = await Promise.all([
          store.getState().schema(),
          store.getState().getLayer(cwd, "project"),
          store.getState().getLayer(cwd, "global"),
          store
            .getState()
            .resolve(cwd)
            .catch(() => null),
        ]);
        if (isCancelled()) return;
        setLoad({ phase: "ready", options: schema.options, current, globalDefaults });
        if (resolved && !isCancelled()) setResolvedDefaults(resolved.effective);
      } catch (err) {
        if (!isCancelled()) setLoad({ phase: "error", message: friendlyErrorMessage(err) });
      }
    },
    [cwd, store],
  );

  if (!cwd) {
    return (
      <div className={CLASS.root}>
        <h2>Project launch settings</h2>
        <p className={CLASS.help}>
          No project selected. Open this page via the gear icon next to a project in the sidebar, or navigate here with
          a <code>?cwd=</code> query parameter.
        </p>
      </div>
    );
  }

  return (
    <div className={CLASS.root}>
      <h2>Project launch settings</h2>
      <p className={CLASS.help}>{cwd}</p>
      <p className={CLASS.help}>
        Layered on top of the global Evener launch settings. Only fields set here override the global defaults.
      </p>
      {load.phase === "loading" && <p className={CLASS.help}>Loading project launch settings…</p>}
      {load.phase === "error" && <p className={CLASS.error}>Failed to load project launch settings. {load.message}</p>}
      {load.phase === "ready" && (
        <LaunchConfigForm
          options={load.options}
          layer="project"
          current={load.current}
          globalDefaults={load.globalDefaults}
          resolvedDefaults={resolvedDefaults}
          successToast="Project launch settings saved"
          validatePath={(path, kind) => store.getState().validatePath(path, kind)}
          paths={paths}
          host={host}
          onSave={(config) => store.getState().setLayer(cwd, "project", config)}
          onSaved={(resolved) => setResolvedDefaults(resolved.effective)}
        />
      )}
    </div>
  );
}

/** ProjectHostScope is the Per-project launch overrides section scoped to the
 * settings route's selected host (component 07b): the one shared HostPicker
 * plus ProjectSection, whose own project layer is that host's own. The cwd is
 * still read from the route's ?cwd= query; the host comes from the shared
 * frame, never a query parameter of this pane's own. Local renders today's
 * section byte-for-byte; a remote host's reads and writes all go through
 * evener/host/request. */
export function ProjectHostScope({ sectionId }: ProjectSectionProps) {
  return <HostScopedSurface>{(host) => <ProjectSection sectionId={sectionId} host={host} />}</HostScopedSurface>;
}
