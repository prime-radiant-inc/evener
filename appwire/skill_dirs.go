package appwire

// MethodEvenerSettingsSkillDirsGet reads the host-resolved built-in skill
// roots that the Settings skill-directory reference cannot derive from the
// browser's own environment. Project roots depend on each session's working
// directory and are described by the UI instead.
const MethodEvenerSettingsSkillDirsGet = "evener/settings/skillDirs/get"

// SkillDirsResponse carries the absolute built-in skill roots the host scans.
// An empty field means the host could not resolve that root; it scans none.
type SkillDirsResponse struct {
	HomeSkillsDir string `json:"homeSkillsDir,omitempty"`
	UserSkillsDir string `json:"userSkillsDir,omitempty"`
}
