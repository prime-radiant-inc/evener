package plugins

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	agentplugin "primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/internal/bundled"
)

// LaunchPluginSource identifies how a plugin was discovered for launch.
type LaunchPluginSource string

const (
	LaunchPluginSourceDirectory LaunchPluginSource = "directory"
	LaunchPluginSourceInstalled LaunchPluginSource = "installed"
	// LaunchPluginSourceBundled is a plugin shipped inside the binary,
	// materialized under the store root when a launch names it.
	LaunchPluginSourceBundled LaunchPluginSource = "bundled"
)

// LaunchPluginCandidate is the safe, display-oriented metadata for one valid
// plugin winner.
type LaunchPluginCandidate struct {
	Name, Version, Description string
	Source                     LaunchPluginSource
	Marketplace, Path          string
	Selected                   bool
	SkillCount, AgentCount     int
	CommandCount, HookCount    int
	MCPCount                   int
}

// LaunchPluginDiagnostic describes a candidate that could not be selected.
type LaunchPluginDiagnostic struct {
	Name    string             `json:"name,omitempty"`
	Path    string             `json:"path,omitempty"`
	Message string             `json:"message"`
	Source  LaunchPluginSource `json:"source,omitempty"`
}

// PluginSelectionError describes a requested plugin name that cannot be
// selected from the current launch inventory.
type PluginSelectionError struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// LaunchPluginResolution is the complete launch-time plugin inventory and
// effective selection.
type LaunchPluginResolution struct {
	Candidates      []LaunchPluginCandidate
	SelectedDirs    []string
	Diagnostics     []LaunchPluginDiagnostic
	SelectionErrors []PluginSelectionError
}

// ResolveForLaunch enumerates explicit plugin directories followed by globally
// enabled installed plugins. It loads each candidate with the same loader used
// by session startup, retaining invalid candidates as structured diagnostics.
func (m *Manager) ResolveForLaunch(explicitDirs []string, enabledNames *[]string) (LaunchPluginResolution, error) {
	resolution := LaunchPluginResolution{
		Candidates: []LaunchPluginCandidate{}, SelectedDirs: []string{},
		Diagnostics: []LaunchPluginDiagnostic{}, SelectionErrors: []PluginSelectionError{},
	}
	seen := make(map[string]bool)

	requested := make(map[string]bool)
	if enabledNames != nil {
		for _, name := range *enabledNames {
			if requested[name] {
				resolution.SelectionErrors = append(resolution.SelectionErrors, PluginSelectionError{
					Name: name, Reason: "duplicate name in selection",
				})
			}
			requested[name] = true
		}
	}

	add := func(path string, source LaunchPluginSource, marketplace, registryVersion, registryName string) {
		instance, err := enabledLoad(path)
		if err != nil {
			name := registryName
			message := err.Error()
			if source == LaunchPluginSourceDirectory {
				name = ""
			}
			resolution.Diagnostics = append(resolution.Diagnostics, LaunchPluginDiagnostic{
				Name: name, Path: path, Message: message, Source: source,
			})
			return
		}

		name := instance.Manifest.Name
		if seen[name] {
			resolution.Diagnostics = append(resolution.Diagnostics, LaunchPluginDiagnostic{
				Name:    name,
				Path:    path,
				Message: fmt.Sprintf("duplicate plugin name %q; keeping the first", name),
				Source:  source,
			})
			return
		}
		seen[name] = true

		version := instance.Manifest.Version
		if source == LaunchPluginSourceInstalled && registryVersion != "" {
			version = registryVersion
		}
		selected := enabledNames == nil || requested[name]
		resolution.Candidates = append(resolution.Candidates, LaunchPluginCandidate{
			Name: name, Version: version, Description: instance.Manifest.Description,
			Source: source, Marketplace: marketplace, Path: path, Selected: selected,
			SkillCount: len(instance.Skills), AgentCount: len(instance.Agents),
			CommandCount: len(instance.Commands), HookCount: countHooks(instance.Hooks),
			MCPCount: len(instance.MCPConfigs),
		})
		if selected {
			resolution.SelectedDirs = append(resolution.SelectedDirs, path)
		}
	}

	for _, path := range explicitDirs {
		add(path, LaunchPluginSourceDirectory, "", "", "")
	}

	items, err := m.List()
	if err != nil {
		return resolution, err
	}
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		// Deliberately do not filter item.Broken. List's validation is a useful
		// snapshot, but loading here gives Preview a structured diagnostic and
		// avoids turning a broken candidate into a registry-level failure.
		add(item.InstallPath, LaunchPluginSourceInstalled, item.Marketplace, item.Version, item.Plugin)
	}

	if enabledNames != nil {
		for _, name := range *enabledNames {
			if name == "" {
				resolution.SelectionErrors = append(resolution.SelectionErrors, PluginSelectionError{
					Name: name, Reason: "plugin name must not be empty",
				})
				continue
			}
			if !seen[name] {
				// Bundled plugins join the inventory only by request, so an
				// unremarkable launch never picks them up.
				if path, err := m.materializeBundledPlugin(name); err == nil {
					add(path, LaunchPluginSourceBundled, "", "", name)
				} else if !errors.Is(err, fs.ErrNotExist) {
					resolution.Diagnostics = append(resolution.Diagnostics, LaunchPluginDiagnostic{
						Name: name, Message: err.Error(), Source: LaunchPluginSourceBundled,
					})
				}
			}
			if !seen[name] {
				resolution.SelectionErrors = append(resolution.SelectionErrors, PluginSelectionError{
					Name: name, Reason: "no valid plugin candidate",
				})
			}
		}
	}
	return resolution, nil
}

// materializeBundledPlugin copies the bundled plugin named name out of the
// binary into <root>/bundled/<name>, replacing any earlier copy so the on-disk
// plugin always matches the running binary. Returns fs.ErrNotExist when no
// bundled plugin has that name.
func (m *Manager) materializeBundledPlugin(name string) (string, error) {
	if name == "" || name != filepath.Base(name) {
		return "", fs.ErrNotExist
	}
	src := bundled.Plugins()
	if info, err := fs.Stat(src, name); err != nil || !info.IsDir() {
		return "", fs.ErrNotExist
	}
	dest := filepath.Join(m.Root, "bundled", name)
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("replace bundled plugin %s: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.CopyFS(dest, mustSubFS(src, name)); err != nil {
		return "", fmt.Errorf("materialize bundled plugin %s: %w", name, err)
	}
	return dest, nil
}

func mustSubFS(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

func countHooks(hooks map[agentplugin.HookEvent][]agentplugin.RegisteredHook) int {
	count := 0
	for _, eventHooks := range hooks {
		count += len(eventHooks)
	}
	return count
}

// ValidateSelection returns one deterministic error for all invalid requested
// names. Candidate diagnostics are intentionally not validation failures.
func (r LaunchPluginResolution) ValidateSelection() error {
	if len(r.SelectionErrors) == 0 {
		return nil
	}
	errs := append([]PluginSelectionError(nil), r.SelectionErrors...)
	slices.SortFunc(errs, func(a, b PluginSelectionError) int { return cmp.Compare(a.Name, b.Name) })
	parts := make([]string, 0, len(errs))
	for _, item := range errs {
		parts = append(parts, fmt.Sprintf("%s: %s", item.Name, item.Reason))
	}
	return fmt.Errorf("enabled plugin selection is unavailable: %s", strings.Join(parts, "; "))
}
