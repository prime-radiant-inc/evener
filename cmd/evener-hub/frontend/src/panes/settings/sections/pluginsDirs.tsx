// pluginsDirs.tsx is a thin DirListSetting instantiation - the "Plugins"
// settings section (#13). Byte-identical to skillsDirs.tsx apart from the
// wireField/label/copy, per templates/partials/settings/plugins.html vs
// skills.html being structurally identical legacy files (parity-m7-
// settings.md §14).
import { DirListSetting } from "./dirListSetting";

export function PluginsDirsSection() {
  return (
    <DirListSetting
      wireField="pluginDirs"
      label="Plugin directories"
      copy="Directories serf scans for plugins at launch. Applied to every spawn."
    />
  );
}
