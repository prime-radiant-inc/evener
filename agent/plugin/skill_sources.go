package plugin

import (
	"path/filepath"

	"primeradiant.com/evener/agent/skill"
)

// SkillSources selects plugin identities using manifests alone. The first valid
// manifest reserves its name even if unrelated components cannot be loaded.
func SkillSources(dirs []string) ([]skill.PluginSource, []skill.Diagnostic) {
	var sources []skill.PluginSource
	var diagnostics []skill.Diagnostic
	seen := make(map[string]string)
	for _, dir := range dirs {
		name, err := ManifestName(dir)
		if err != nil {
			diagnostics = append(diagnostics, skill.Diagnostic{Category: "unreadable_source", Source: dir, Message: err.Error()})
			continue
		}
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			resolved, err = filepath.Abs(resolved)
		}
		if err != nil {
			diagnostics = append(diagnostics, skill.Diagnostic{Category: "unreadable_source", Name: name, Source: dir, Message: err.Error()})
			continue
		}
		if previous, ok := seen[name]; ok {
			diagnostics = append(diagnostics, skill.Diagnostic{Category: "collision", Name: name, Source: previous, OtherSource: resolved, Message: "first plugin manifest reserves this name"})
			continue
		}
		seen[name] = resolved
		sources = append(sources, skill.PluginSource{Name: name, Dir: resolved})
	}
	return sources, diagnostics
}
