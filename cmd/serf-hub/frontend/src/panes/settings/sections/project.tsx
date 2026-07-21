// Settings -> Per-project launch overrides (#18), reached via
// /settings/project?cwd=<dir> - never through the settings-nav list (see
// sections.ts's own comment; this "project" section id is deliberately
// absent from SETTINGS_SECTIONS). Shares launchServer.tsx's engine
// (LaunchConfigForm) but with the 3-state loaded contract (loading/error/
// ready, vs. launch-serf's 2-state) and no diagnostics panel at all
// (parity-m7-settings.md §18).
//
// Deliberate scope decision (this task's own report): when no `cwd` is
// present, this renders a plain instructional message rather than the
// legacy's project-picker list. That picker needs a {name, cwd} pair per
// known project; the only available RPC, serf/projects/recent, returns bare
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
import { useEffect, useState } from "react";
import type { LaunchConfigLayer, LaunchOption } from "../../../protocol/types.gen";
import { launchConfigStore } from "../../../stores/launchConfig";
import { requireClass } from "../../../widgets/internal/requireClass";
import { LaunchConfigForm } from "./launchShared/LaunchConfigForm";
import styles from "./project.module.css";

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

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export interface ProjectSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
}

/**
 * Per-project launch overrides: layered on top of the global Serf launch
 * defaults. Only fields set here override the global ones. The global layer
 * is fetched read-only, purely to drive the "default: {value}" inline hints
 * - this page never writes it.
 */
export function ProjectSection(_props: ProjectSectionProps) {
  const cwd = useQueryCwd();
  const [load, setLoad] = useState<LoadState>({ phase: "loading" });

  useEffect(() => {
    if (!cwd) return;
    let cancelled = false;
    setLoad({ phase: "loading" });
    async function run() {
      try {
        const schema = await launchConfigStore.getState().schema();
        const current = await launchConfigStore.getState().getLayer(cwd, "project");
        const globalDefaults = await launchConfigStore.getState().getLayer(cwd, "global");
        if (cancelled) return;
        setLoad({ phase: "ready", options: schema.options, current, globalDefaults });
      } catch (err) {
        if (!cancelled) setLoad({ phase: "error", message: errorMessage(err) });
      }
    }
    void run();
    return () => {
      cancelled = true;
    };
  }, [cwd]);

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
        Layered on top of the global Serf and Codex launch settings. Only fields set here override the global defaults.
      </p>
      {load.phase === "loading" && <p className={CLASS.help}>Loading project launch settings…</p>}
      {load.phase === "error" && <p className={CLASS.error}>Failed to load project launch settings. {load.message}</p>}
      {load.phase === "ready" && (
        <LaunchConfigForm
          options={load.options}
          layer="project"
          current={load.current}
          globalDefaults={load.globalDefaults}
          successToast="Project launch settings saved"
          validatePath={(path, kind) => launchConfigStore.getState().validatePath(path, kind)}
          onSave={(config) => launchConfigStore.getState().setLayer(cwd, "project", config)}
        />
      )}
    </div>
  );
}
