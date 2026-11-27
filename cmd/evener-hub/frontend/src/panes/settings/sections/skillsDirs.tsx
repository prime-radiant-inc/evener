// skillsDirs.tsx is the Skills settings section: the editable extras list plus
// the full read-only discovery order. The host-scoped read below supplies the
// two built-in roots that cannot be derived from the browser's environment;
// bundled, project, and plugin sources are described here because they are
// fixed parts of discovery rather than host settings.
import {
  friendlyErrorMessage,
  hostRequest,
  isMethodNotFound,
  LOCAL_HOST,
  type SkillDirsResponse,
} from "@evener/appwire-client";
import { type ReactNode, useState } from "react";
import { connectedClientPort } from "../../../stores/connection";
import { PropertyList, type StackedPropertyListRow } from "../../../widgets";
import { DirListSetting } from "./dirListSetting";
import { HostScopedSurface } from "./hostScopedSurface";
import { RefreshErrorNotice, SectionGate } from "./sectionGate";
import { Code } from "./settingsField";
import styles from "./skillsDirs.module.css";
import { useHostScopedLoad } from "./useConnectedEffect";

const skillDirsClient = connectedClientPort("skillDirs");

export interface SkillsDirsSectionProps {
  /** The host whose own skill directories this section edits (component 07b).
   * Defaults to the local hub, so a direct render is today's local section. */
  host?: string;
}

function builtInDirValue(path: string | undefined, unavailable: boolean): ReactNode {
  if (path) return <Code>{path}</Code>;
  return unavailable ? "Unavailable on this host" : "Not resolved";
}

/** BuiltInSkillDirs is the read-only part of the discovery order: every
 * location the host scans without configuration, plus the heading that marks
 * where the editable extras join the order. The static rows stay visible when
 * the host cannot answer: an older hub lacks the read entirely, and a
 * transient error still leaves the discovery order useful. */
function BuiltInSkillDirs({ host }: { host: string }) {
  const [dirs, setDirs] = useState<SkillDirsResponse | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const { retryLoad } = useHostScopedLoad(
    host,
    async (blank, isCancelled) => {
      if (blank) {
        setDirs(null);
        setUnavailable(false);
        setError(null);
      }
      try {
        const client = skillDirsClient.requireClient();
        const response = await hostRequest(client, host, "evener/settings/skillDirs/get", {});
        if (!isCancelled()) {
          setDirs(response);
          setUnavailable(false);
          setError(null);
        }
        return true;
      } catch (err) {
        if (isMethodNotFound(err)) {
          if (!isCancelled()) {
            setDirs(null);
            setUnavailable(true);
            setError(null);
          }
          return true;
        }
        if (!isCancelled()) setError(friendlyErrorMessage(err));
        return false;
      }
    },
    [host],
  );
  const pending = dirs === null && error === null && !unavailable;
  const rows: StackedPropertyListRow[] = [
    {
      label: "Bundled",
      value: "Embedded in evener",
      help: "Skills shipped inside the evener binary.",
    },
    {
      label: "Home",
      value: builtInDirValue(dirs?.homeSkillsDir, unavailable),
      help: "The agent-standard skills directory in the host's home.",
    },
    {
      label: "User config",
      value: builtInDirValue(dirs?.userSkillsDir, unavailable),
      help: "Skills under the host's evener config root, after its global launch settings.",
    },
    {
      label: "Project",
      value: (
        <>
          <Code>.agents/skills</Code> then <Code>skills/</Code>
        </>
      ),
      help: (
        <>
          From the repository root down to each session's working directory. Deeper levels win, and <Code>skills/</Code>{" "}
          wins over <Code>.agents/skills/</Code> at the same level.
        </>
      ),
    },
  ];
  return (
    <div className={styles.builtins}>
      <h3>Built-in locations</h3>
      <p className={styles.help}>
        Session defaults, scanned automatically at launch in increasing precedence. Project and per-launch settings can
        add locations.
      </p>
      <SectionGate loading={pending} error={null}>
        <PropertyList layout="stacked" rows={rows} />
      </SectionGate>
      {error !== null ? <RefreshErrorNotice onRetry={retryLoad}>{error}</RefreshErrorNotice> : null}
      <h3>Extra directories</h3>
    </div>
  );
}

const PLUGIN_SKILLS_NOTE = (
  <p className={styles.help}>
    <strong>Plugin skills</strong> from installed plugins are scanned after the extra directories above.
  </p>
);

export function SkillsDirsSection({ host = LOCAL_HOST }: SkillsDirsSectionProps) {
  return (
    <DirListSetting
      wireField="skillsDirs"
      label="Skill directories"
      copy="Locations evener scans for skills at launch, in increasing precedence. Built-in locations are always scanned; extra directories are added between project skills and plugin skills."
      host={host}
      intro={<BuiltInSkillDirs host={host} />}
      outro={PLUGIN_SKILLS_NOTE}
      countLabel="extra"
      emptyMessage="No extra skill directories. Add one below."
    />
  );
}

/** SkillsDirsHostScope is the Skills settings section scoped to the settings
 * route's selected host: the one shared HostPicker plus SkillsDirsSection. */
export function SkillsDirsHostScope() {
  return <HostScopedSurface>{(host) => <SkillsDirsSection host={host} />}</HostScopedSurface>;
}
