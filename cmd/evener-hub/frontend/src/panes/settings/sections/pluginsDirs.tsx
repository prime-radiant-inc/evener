import { LOCAL_HOST } from "../../../stores/hostRouting";
import { DirListSetting } from "./dirListSetting";

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
