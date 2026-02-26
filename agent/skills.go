package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/frontmatter"
)

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
func DiscoverSkills(env ExecutionEnvironment, extraDirs ...string) map[string]SkillMeta {
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
	if gr := gitRootOrEmpty(env, cwd); gr != "" {
		root = gr
	}

	dirs := dirsFromRootToCwd(root, cwd)
	out := map[string]SkillMeta{}

	for _, dir := range dirs {
		scanSkillsDir(filepath.Join(dir, "skills"), out)
	}
	for _, dir := range extraDirs {
		scanSkillsDir(dir, out)
	}

	return out
}

// scanSkillsDir scans a directory for skill subdirectories containing SKILL.md.
// Found skills are added to out, overwriting any existing entry with the same name.
func scanSkillsDir(dir string, out map[string]SkillMeta) {
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

// ResolveSkillContent looks up a skill by name in a skills map and returns its body content.
// Tries exact match first, then tries unnamespaced match (e.g., "tdd" matches "myplugin:tdd").
// Returns ("", nil) if not found.
func ResolveSkillContent(skills map[string]SkillMeta, name string) (string, error) {
	if meta, ok := skills[name]; ok {
		return LoadSkillBody(meta)
	}
	for key, meta := range skills {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 && parts[1] == name {
			return LoadSkillBody(meta)
		}
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
