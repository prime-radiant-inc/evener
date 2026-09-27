package agent

import (
	"errors"
	"io/fs"
	"path/filepath"

	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/internal/plugins"
)

// loadSkillInvocationSource recovers fresh invocations whose selected plugin
// revision has been collected. Durable continuations retain their exact source.
func loadSkillInvocationSource(invocation skillInvocation, descriptor skill.Descriptor) (skill.LoadedSkill, []skill.Diagnostic, error) {
	loaded, diagnostics, err := skill.Load(descriptor)
	if invocation.Source != nil || !errors.Is(err, fs.ErrNotExist) || descriptor.CatalogName == descriptor.Meta.Name {
		return loaded, diagnostics, err
	}
	// Plugin discovery reads <plugin>/skills/<directory>/SKILL.md. Derive the
	// selected root from that recorded absolute source, not the process cwd or
	// today's plugin defaults.
	skillsDir := filepath.Dir(filepath.Dir(descriptor.Meta.SkillFile))
	if filepath.Base(skillsDir) != "skills" {
		return loaded, diagnostics, err
	}
	current, refreshErr := plugins.UpdatedInstallDir(filepath.Dir(skillsDir))
	if refreshErr != nil || current == "" {
		return loaded, diagnostics, errors.Join(err, refreshErr)
	}
	name, refreshErr := plugin.ManifestName(current)
	if refreshErr != nil || name+":"+descriptor.Meta.Name != descriptor.CatalogName {
		return loaded, diagnostics, errors.Join(err, refreshErr)
	}
	catalog := skill.Discover(nil, skill.DiscoverOptions{Plugins: []skill.PluginSource{{Name: name, Dir: current}}})
	replacement, refreshErr := catalog.ResolveExact(descriptor.CatalogName)
	if refreshErr != nil || replacement.Meta.Name != descriptor.Meta.Name || filepath.Dir(filepath.Dir(replacement.Meta.SkillFile)) != filepath.Join(current, "skills") {
		return loaded, diagnostics, errors.Join(err, refreshErr)
	}
	// Load reparses metadata, and the caller applies the current invocation
	// controls and source-bound authorization before admitting any content.
	return skill.Load(replacement)
}
