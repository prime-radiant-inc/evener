package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/frontmatter"
)

var slashAddressableNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]*(?::[A-Za-z0-9_][A-Za-z0-9_-]*)?$`)

// IsSlashAddressableName reports whether name is a canonical slash-addressable
// skill name. Names are ASCII and may have one plugin separator.
func IsSlashAddressableName(name string) bool {
	return len(name) <= 128 && slashAddressableNamePattern.MatchString(name)
}

// SkillMeta holds discovery-time metadata for a single skill.
type SkillMeta struct {
	Name         string   // from frontmatter (required)
	Description  string   // from frontmatter (required)
	AllowedTools []string // from frontmatter (optional)
	Dir          string   // absolute path to the skill directory
	SkillFile    string   // absolute path to SKILL.md
}

// DiscoverSkills walks from git root to cwd looking for skills/ directories.
// Extra directories are scanned after the root→cwd walk, so they shadow
// project skills with the same name.
// Returns a deduplicated map[name]SkillMeta (later entries shadow earlier).
func DiscoverSkills(env execenv.ExecutionEnvironment, extraDirs ...string) map[string]SkillMeta {
	if env == nil {
		return nil
	}

	cwd := strings.TrimSpace(env.WorkingDirectory())
	if cwd == "" {
		return nil
	}
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = resolved
	}

	root := cwd
	if gr := execenv.GitRootOrEmpty(env, cwd); gr != "" {
		root = gr
	}

	dirs := execenv.DirsFromRootToCwd(root, cwd)
	out := map[string]SkillMeta{}

	for _, dir := range dirs {
		ScanSkillsDir(filepath.Join(dir, "skills"), out)
	}
	for _, dir := range extraDirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		ScanSkillsDir(dir, out)
	}

	return out
}

// ScanSkillsDir scans a directory for skill subdirectories containing SKILL.md.
// Found skills are added to out, overwriting any existing entry with the same name.
func ScanSkillsDir(dir string, out map[string]SkillMeta) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillFile := filepath.Join(dir, entry.Name(), "SKILL.md")
		meta, ok := parseSkillFile(skillFile)
		if !ok {
			continue
		}
		meta.Dir = filepath.Join(dir, entry.Name())
		meta.SkillFile = skillFile
		out[meta.Name] = meta
	}
}

// LoadSkillBody reads a SKILL.md and returns the markdown body (after frontmatter).
func LoadSkillBody(meta SkillMeta) (string, error) {
	data, err := os.ReadFile(meta.SkillFile)
	if err != nil {
		return "", fmt.Errorf("reading skill file: %w", err)
	}
	doc, err := frontmatter.Parse(string(data))
	if err != nil {
		return "", fmt.Errorf("parsing skill frontmatter: %w", err)
	}
	return doc.Body, nil
}

// CatalogEntries returns path-free skill metadata in canonical map-key order.
func CatalogEntries(skills map[string]SkillMeta) []SkillMeta {
	entries := make([]SkillMeta, 0, len(skills))
	for key, meta := range skills {
		entries = append(entries, SkillMeta{
			Name:         key,
			Description:  meta.Description,
			AllowedTools: append([]string(nil), meta.AllowedTools...),
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

// ResolveSkill resolves an exact catalog key, or a uniquely matching plugin
// suffix for an unqualified name.
func ResolveSkill(skills map[string]SkillMeta, name string) (catalogName string, meta SkillMeta, ok bool) {
	if meta, ok := skills[name]; ok {
		return name, meta, true
	}
	if strings.Contains(name, ":") {
		return "", SkillMeta{}, false
	}
	for key, candidate := range skills {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 || parts[1] != name {
			continue
		}
		if ok {
			return "", SkillMeta{}, false
		}
		catalogName, meta, ok = key, candidate, true
	}
	return catalogName, meta, ok
}

// ResolveSkillContent looks up a skill by name in a skills map and returns its body content.
// Tries exact match first, then tries unnamespaced match (e.g., "tdd" matches "myplugin:tdd").
// Returns ("", nil) if not found.
func ResolveSkillContent(skills map[string]SkillMeta, name string) (string, error) {
	_, meta, ok := ResolveSkill(skills, name)
	if ok {
		return LoadSkillBody(meta)
	}
	return "", nil
}

// parseSkillFile reads a SKILL.md, parses frontmatter, and extracts required fields.
// Returns (meta, true) on success or (zero, false) if the file is missing, unparseable,
// or lacks required fields.
func parseSkillFile(path string) (SkillMeta, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SkillMeta{}, false
	}
	doc, err := frontmatter.Parse(string(data))
	if err != nil || doc.Meta == nil {
		return SkillMeta{}, false
	}

	name, _ := doc.Meta["name"].(string)
	desc, _ := doc.Meta["description"].(string)
	if strings.TrimSpace(name) == "" || strings.TrimSpace(desc) == "" {
		return SkillMeta{}, false
	}
	if !IsSlashAddressableName(name) {
		return SkillMeta{}, false
	}

	var allowed []string
	if tools, ok := doc.Meta["allowed-tools"]; ok {
		if arr, ok := tools.([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					allowed = append(allowed, s)
				}
			}
		}
	}

	return SkillMeta{
		Name:         name,
		Description:  desc,
		AllowedTools: allowed,
	}, true
}
