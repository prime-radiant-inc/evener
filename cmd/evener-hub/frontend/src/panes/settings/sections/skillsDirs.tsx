// skillsDirs.tsx is a thin DirListSetting instantiation - the "Skills"
// settings section (#14). Byte-identical to pluginsDirs.tsx apart from the
// wireField/label/copy, per templates/partials/settings/skills.html vs
// plugins.html being structurally identical legacy files (parity-m7-
// settings.md §14). The section is host-scoped (component 07b): its
// SkillsDirsHostScope wrapper hands it the settings route's selected host, and
// DirListSetting reads that host's own global launch layer.
import { LOCAL_HOST } from "../../../stores/hostRouting";
import { DirListSetting } from "./dirListSetting";
import { HostScopedSurface } from "./hostScopedSurface";

export interface SkillsDirsSectionProps {
  /** The host whose own skill directories this section edits (component 07b).
   * Defaults to the local hub, so a direct render is today's local section. */
  host?: string;
}

export function SkillsDirsSection({ host = LOCAL_HOST }: SkillsDirsSectionProps) {
  return (
    <DirListSetting
      wireField="skillsDirs"
      label="Skill directories"
      copy="Directories evener scans for skills at launch. Applied to every spawn."
      host={host}
    />
  );
}

/** SkillsDirsHostScope is the Skills settings section scoped to the settings
 * route's selected host: the one shared HostPicker plus SkillsDirsSection. */
export function SkillsDirsHostScope() {
  return <HostScopedSurface>{(host) => <SkillsDirsSection host={host} />}</HostScopedSurface>;
}
