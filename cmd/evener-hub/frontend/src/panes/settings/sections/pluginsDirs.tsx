// pluginsDirs.tsx is a thin DirListSetting instantiation - the "Plugins"
// settings section (#13). Byte-identical to skillsDirs.tsx apart from the
// wireField/label/copy, per templates/partials/settings/plugins.html vs
// skills.html being structurally identical legacy files (parity-m7-
// settings.md §14). The section is host-scoped (component 07b):
// PluginsDirsHostScope hands DirListSetting the settings route's selected host.
import { LOCAL_HOST } from "../../../stores/hostRouting";
import { DirListSetting } from "./dirListSetting";
import { HostScopedSurface } from "./hostScopedSurface";

export interface PluginsDirsSectionProps {
  /** The host whose own plugin directories this section edits (component 07b).
   * Defaults to the local hub, so a direct render is today's local section. */
  host?: string;
}

export function PluginsDirsSection({ host = LOCAL_HOST }: PluginsDirsSectionProps) {
  return (
    <DirListSetting
      wireField="pluginDirs"
      label="Plugin directories"
      copy="Directories evener scans for plugins at launch. Applied to every spawn."
      host={host}
    />
  );
}

/** PluginsDirsHostScope is the Plugins settings section scoped to the settings
 * route's selected host: the one shared HostPicker plus PluginsDirsSection. */
export function PluginsDirsHostScope() {
  return <HostScopedSurface>{(host) => <PluginsDirsSection host={host} />}</HostScopedSurface>;
}
