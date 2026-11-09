package skill

import (
	"os"
	"path/filepath"
	"regexp"

	"primeradiant.com/evener/agent/execenv"
)

var slashAddressableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*(?::[A-Za-z0-9_][A-Za-z0-9_-]*)?$`)

// IsSlashAddressableName reports whether name is a canonical slash-addressable
// skill name. Names are ASCII and may have one plugin separator.
func IsSlashAddressableName(name string) bool {
	return len(name) <= 128 && slashAddressableNamePattern.MatchString(name)
}

// SkillMeta holds discovery-time metadata for a single skill.
type SkillMeta struct {
	Name         string         // from frontmatter (required)
	Description  string         // from frontmatter (required)
	AllowedTools []string       // from frontmatter (optional)
	Metadata     map[string]any // preserved descriptive frontmatter
	Dir          string         // absolute path to the skill directory
	SkillFile    string         // absolute path to SKILL.md
}

// ScanSkillsDir scans a directory for skill subdirectories containing SKILL.md.
// Found skills are added to out, overwriting any existing entry with the same name.
func ScanSkillsDir(dir string, out map[string]SkillMeta) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		candidate, err := skillDirectory(dir, entry)
		if err != nil || !candidate {
			continue
		}
		skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
		meta, ok := parseSkillFile(skillFile)
		if !ok {
			continue
		}
		out[meta.Name] = meta
	}
}

// parseSkillFile reads a SKILL.md, parses frontmatter, and extracts required fields.
// Returns (meta, true) on success or (zero, false) if the file is missing, unparseable,
// or lacks required fields.
func parseSkillFile(path string) (SkillMeta, bool) {
	data, err := execenv.ReadFileCapped(path)
	if err != nil {
		return SkillMeta{}, false
	}
	descriptor, _, err := Parse(data, path)
	if err != nil || descriptor.Unavailable {
		return SkillMeta{}, false
	}
	return descriptor.Meta, true
}
