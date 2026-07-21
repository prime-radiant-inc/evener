// Settings -> Codex launch config (#10): server-rendered, read-only
// projection of hub.toml's [[codex_launches]] entries, overview-fed
// (serf/settings/overview -> data.codexLaunches). No in-UI create/edit/
// delete - edits require an external hub.toml edit + hub restart
// (parity-m7-settings.md §10). See overviewSeam.ts's own top comment for why
// `useOverview` is injected rather than importing stores/settingsOverview.ts
// directly.
import { useEffect } from "react";
import type { SettingsCodexLaunchEntry } from "../../../protocol/types.gen";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./launchCodex.module.css";
import { useSettingsOverview } from "./overviewSeam";

const CLASS = {
  root: requireClass(styles.root, "launchCodex.module.css", "root"),
  help: requireClass(styles.help, "launchCodex.module.css", "help"),
  entry: requireClass(styles.entry, "launchCodex.module.css", "entry"),
  row: requireClass(styles.row, "launchCodex.module.css", "row"),
  label: requireClass(styles.label, "launchCodex.module.css", "label"),
  value: requireClass(styles.value, "launchCodex.module.css", "value"),
  example: requireClass(styles.example, "launchCodex.module.css", "example"),
};

const EXAMPLE_TOML = `[[codex_launches]]
id          = "my-codex"
binary      = "/usr/local/bin/codex"
working_dir = "/path/to/project"
listen      = "ws://127.0.0.1:9190"
timeout     = "30s"`;

// formatTimeoutMillis mirrors the legacy template's own already-formatted
// .Timeout string closely enough for this field's realistic range (launch
// timeouts are configured in whole seconds) - the wire field is raw
// milliseconds (SettingsCodexLaunchEntry.timeoutMillis), so this is the
// smallest formatter that round-trips the common case exactly.
function formatTimeoutMillis(ms: number): string {
  if (ms % 1000 === 0) return `${ms / 1000}s`;
  return `${ms}ms`;
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className={CLASS.row}>
      <span className={CLASS.label}>{label}</span>
      <span className={CLASS.value}>{value}</span>
    </div>
  );
}

function EnvRow({ envKeys }: { envKeys: string[] }) {
  return (
    <div className={CLASS.row}>
      <span className={CLASS.label}>Env</span>
      <span className={CLASS.value}>
        {envKeys.map((key) => (
          // Values are always redacted to KEY=… regardless of actual
          // content - the wire field already carries only key names, never
          // values (see SettingsCodexLaunchEntry's own doc comment).
          <span key={key}>{`${key}=… `}</span>
        ))}
      </span>
    </div>
  );
}

function CodexLaunchEntryCard({ entry }: { entry: SettingsCodexLaunchEntry }) {
  return (
    <div className={CLASS.entry}>
      <h3>{entry.id}</h3>
      <Row label="Binary" value={entry.binary || "codex"} />
      <Row label="Working dir" value={entry.workingDir || "(inherited)"} />
      <Row label="Listen" value={entry.listen || "ws://127.0.0.1:0"} />
      <Row label="Timeout" value={entry.timeoutMillis ? formatTimeoutMillis(entry.timeoutMillis) : "30s"} />
      {entry.envKeys && entry.envKeys.length > 0 && <EnvRow envKeys={entry.envKeys} />}
    </div>
  );
}

export interface CodexLaunchSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
  useOverview?: typeof useSettingsOverview;
}

export function CodexLaunchSection({ useOverview = useSettingsOverview }: CodexLaunchSectionProps) {
  const { data, loading, error, fetch } = useOverview();

  useEffect(() => {
    // fetch caches internally (overviewSeam.ts's own contract) - see
    // agents.tsx's identical effect for why depending on it honestly is
    // both correct and safe.
    void fetch();
  }, [fetch]);

  const entries = data?.codexLaunches ?? [];

  return (
    <div className={CLASS.root}>
      <h2>Codex launch config</h2>
      <p className={CLASS.help}>
        Codex launch entries are defined in <code>hub.toml</code> under <code>[[codex_launches]]</code>. This page shows
        the current configuration (read-only).
      </p>
      {loading && <p className={CLASS.help}>Loading…</p>}
      {error && <p className={CLASS.help}>Failed to load: {error}</p>}
      {!loading && !error && entries.length === 0 && (
        <div>
          <p className={CLASS.help}>No codex launch entries configured.</p>
          <p className={CLASS.help}>
            To add one, edit <code>hub.toml</code> and add a <code>[[codex_launches]]</code> section, for example:
          </p>
          <pre className={CLASS.example}>{EXAMPLE_TOML}</pre>
          <p className={CLASS.help}>Restart the hub after editing hub.toml.</p>
        </div>
      )}
      {!loading && !error && entries.map((entry) => <CodexLaunchEntryCard key={entry.id} entry={entry} />)}
    </div>
  );
}
