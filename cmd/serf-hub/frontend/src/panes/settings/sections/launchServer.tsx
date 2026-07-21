// Settings -> Serf launch (#9): the global launch-config layer, via the
// schema-driven LaunchConfigForm (launchShared/). Sequential load (schema()
// then getLayer("/","global")) into a 2-state contract - unlike project.tsx's
// 3-state contract, a load failure has no distinct recoverable state, just a
// permanent failure message (parity-m7-settings.md §9).
import { useEffect, useState } from "react";
import type { LaunchConfigDiagnostic, LaunchConfigLayer, LaunchOption } from "../../../protocol/types.gen";
import { launchConfigStore } from "../../../stores/launchConfig";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./launchServer.module.css";
import { LaunchConfigForm } from "./launchShared/LaunchConfigForm";

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

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

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
}

/**
 * Serf launch defaults: applied to every serf spawn unless overridden by a
 * project layer or per-launch. Loads schema()+getLayer("/","global")
 * sequentially, then best-effort resolve("/") to seed the diagnostics panel
 * (failure there is swallowed - "non-fatal", matching the legacy exactly).
 * Save re-derives diagnostics from setLayer's OWN returned resolved config,
 * not a fresh resolve() call.
 */
export function LaunchServerSection(_props: LaunchServerSectionProps) {
  const [load, setLoad] = useState<LoadState>({ phase: "loading" });
  const [diagnostics, setDiagnostics] = useState<LaunchConfigDiagnostic[]>([]);

  useEffect(() => {
    let cancelled = false;
    async function run() {
      try {
        const schema = await launchConfigStore.getState().schema();
        const current = await launchConfigStore.getState().getLayer("/", "global");
        if (cancelled) return;
        setLoad({ phase: "ready", options: schema.options, current });
        try {
          const resolved = await launchConfigStore.getState().resolve("/");
          if (!cancelled) setDiagnostics(resolved.diagnostics ?? []);
        } catch {
          // non-fatal: the form is fully usable without the diagnostics hint
        }
      } catch (err) {
        if (!cancelled) setLoad({ phase: "error", message: errorMessage(err) });
      }
    }
    void run();
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <div className={CLASS.root}>
      <h2>Serf launch defaults</h2>
      <p className={CLASS.help}>
        These values are applied to every serf spawn unless overridden by a project layer or per-launch.
      </p>
      {load.phase === "loading" && <p className={CLASS.help}>Loading launch settings…</p>}
      {load.phase === "error" && <p className={CLASS.error}>Failed to load launch settings. {load.message}</p>}
      {load.phase === "ready" && (
        <>
          <Diagnostics diagnostics={diagnostics} />
          <LaunchConfigForm
            options={load.options}
            layer="global"
            current={load.current}
            successToast="Launch defaults saved"
            validatePath={(path, kind) => launchConfigStore.getState().validatePath(path, kind)}
            onSave={(config) => launchConfigStore.getState().setLayer("/", "global", config)}
            onSaved={(resolved) => setDiagnostics(resolved.diagnostics ?? [])}
          />
        </>
      )}
    </div>
  );
}
