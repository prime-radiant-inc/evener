// skillsDirs.tsx is a thin DirListSetting instantiation - the "Skills"
// settings section (#14). Byte-identical to pluginsDirs.tsx apart from the
// wireField/label/copy, per templates/partials/settings/skills.html vs
// plugins.html being structurally identical legacy files (parity-m7-
// settings.md §14).
import { DirListSetting } from "./dirListSetting";

export function SkillsDirsSection() {
  return (
    <DirListSetting
      wireField="skillsDirs"
      label="Skill directories"
      copy="Directories serf scans for skills at launch. Applied to every spawn."
    />
  );
}
