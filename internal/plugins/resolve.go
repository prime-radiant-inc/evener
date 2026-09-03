package plugins

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

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
	return m.resolveForLaunch(explicitDirs, enabledNames, func(name string) (string, string, error) {
		path, err := m.materializeBundledPlugin(name)
		return path, path, err
	})
}

// PreviewForLaunch is ResolveForLaunch for inspection only. It prepares the
// store exactly as a launch does, so a destination a launch would reject fails
// preview the same way, a store a launch could not publish into fails preview
// too, and an already published copy is the one preview describes. What
// preview never does is publish: it reads the staging it filled and removes it
// before returning, so the store gains nothing.
func (m *Manager) PreviewForLaunch(explicitDirs []string, enabledNames *[]string) (LaunchPluginResolution, error) {
	var scratch []string
	defer func() {
		for _, dir := range scratch {
			_ = os.RemoveAll(dir)
		}
	}()
	return m.resolveForLaunch(explicitDirs, enabledNames, func(name string) (string, string, error) {
		dest, staging, err := m.prepareBundledStore(name)
		if err != nil {
			return "", "", err
		}
		if staging == "" {
			return dest, dest, nil
		}
		scratch = append(scratch, staging)
		if err := os.CopyFS(staging, mustSubFS(bundled.Plugins(), name)); err != nil {
			return "", "", fmt.Errorf("stage bundled plugin %s for preview: %w", name, err)
		}
		return staging, dest, nil
	})
}

// resolveForLaunch builds the inventory. bundledPath supplies, for a requested
// bundled plugin, the directory to load it from and the path to report; it
// returns fs.ErrNotExist when no bundled plugin has that name.
func (m *Manager) resolveForLaunch(explicitDirs []string, enabledNames *[]string, bundledPath func(name string) (loadPath, reportPath string, err error)) (LaunchPluginResolution, error) {
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

	add := func(loadPath, path string, source LaunchPluginSource, marketplace, registryVersion, registryName string) {
		instance, err := enabledLoad(loadPath)
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
		add(path, path, LaunchPluginSourceDirectory, "", "", "")
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
		add(item.InstallPath, item.InstallPath, LaunchPluginSourceInstalled, item.Marketplace, item.Version, item.Plugin)
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
				// unremarkable launch never picks them up. The lookup is by
				// embedded directory name while add keys the inventory by
				// manifest name; the two agree for every bundled plugin, and
				// TestBundledPluginsAreNamedAfterTheirDirectory keeps it that
				// way.
				if loadPath, path, err := bundledPath(name); err == nil {
					add(loadPath, path, LaunchPluginSourceBundled, "", "", name)
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

// stagingPrefix names the private directory a publish copies into before
// renaming it into place. abandonedStaging is how stale such a directory must
// be before a later publish reclaims it: orders of magnitude longer than the
// copy it names, so a publish in flight is never disturbed.
const (
	stagingPrefix    = ".stage-"
	abandonedStaging = time.Hour
)

// materializeBundledPlugin publishes the bundled plugin named name under the
// store root as <Root>/bundled/<name>-<digest>, where digest covers the
// plugin's embedded contents. A published directory is immutable: it is never
// rewritten or removed, so sessions already reading it are safe, and a new
// binary with different contents publishes beside it. Publication is atomic:
// the copy is staged in a private directory and renamed into place, and a
// concurrent publisher that loses the rename adopts the winner's copy.
func (m *Manager) materializeBundledPlugin(name string) (string, error) {
	dest, staging, err := m.prepareBundledStore(name)
	if err != nil {
		return "", err
	}
	if staging == "" {
		return dest, nil
	}
	if err := os.CopyFS(staging, mustSubFS(bundled.Plugins(), name)); err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("materialize bundled plugin %s: %w", name, err)
	}
	if err := os.Rename(staging, dest); err != nil {
		_ = os.RemoveAll(staging)
		if winner, statErr := publishedBundledCopy(dest); statErr == nil && winner {
			return dest, nil
		}
		return "", fmt.Errorf("publish bundled plugin %s: %w", name, err)
	}
	return dest, nil
}

// prepareBundledStore readies <Root>/bundled to hold the bundled plugin named
// name. It returns the published destination and, when nothing is published
// there yet, a private staging directory to fill; staging is empty for a copy
// that is already published. Creating that directory is what proves the store
// can be published into, so a launch and a preview that share this preparation
// fail identically on a store neither can write.
func (m *Manager) prepareBundledStore(name string) (dest, staging string, err error) {
	digest, err := bundledPluginDigest(name)
	if err != nil {
		return "", "", err
	}
	dest = m.bundledPluginPath(name, digest)
	published, err := publishedBundledCopy(dest)
	if err != nil {
		return "", "", err
	}
	if published {
		return dest, "", nil
	}
	store := filepath.Dir(dest)
	if err := os.MkdirAll(store, 0o755); err != nil {
		return "", "", err
	}
	m.reclaimAbandonedStaging(store)
	staging, err = os.MkdirTemp(store, stagingPrefix+filepath.Base(dest)+"-")
	if err != nil {
		return "", "", fmt.Errorf("stage bundled plugin %s: %w", name, err)
	}
	return dest, staging, nil
}

// publishedBundledCopy reports whether dest already holds a published copy.
// Only a real directory counts: a file or a symlink there was not published by
// this code, and loading through it would report an unrelated directory as the
// bundled plugin.
func publishedBundledCopy(dest string) (bool, error) {
	info, err := os.Lstat(dest)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("bundled plugin path %s is not a directory", dest)
	}
	return true, nil
}

// reclaimAbandonedStaging removes staging directories orphaned by a publish
// that was killed before its rename. Staging lives in the store so the rename
// stays on one filesystem, so nothing else would ever collect them. Only a
// real directory is swept: staging is always one, so anything else wearing the
// prefix came from elsewhere and is not this code's to remove.
func (m *Manager) reclaimAbandonedStaging(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), stagingPrefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || m.now().Sub(info.ModTime()) < abandonedStaging {
			continue
		}
		// Best effort: a concurrent publisher may be reclaiming the same orphan.
		_ = os.RemoveAll(filepath.Join(dir, entry.Name()))
	}
}

func (m *Manager) bundledPluginPath(name, digest string) string {
	return filepath.Join(m.Root, "bundled", name+"-"+digest)
}

// bundledPluginDigest identifies the embedded contents of the bundled plugin
// named name. It rejects names that are not a single path component and
// reports fs.ErrNotExist for names that are not bundled.
func bundledPluginDigest(name string) (string, error) {
	if err := validNameComponent("plugin", name); err != nil {
		return "", err
	}
	src := bundled.Plugins()
	if info, err := fs.Stat(src, name); err != nil || !info.IsDir() {
		return "", fs.ErrNotExist
	}
	sum := sha256.New()
	err := fs.WalkDir(mustSubFS(src, name), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			_, _ = fmt.Fprintf(sum, "dir %s\x00", path)
			return nil
		}
		content, err := fs.ReadFile(mustSubFS(src, name), path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(sum, "file %s %d\x00", path, len(content))
		_, _ = sum.Write(content)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("digest bundled plugin %s: %w", name, err)
	}
	return hex.EncodeToString(sum.Sum(nil))[:16], nil
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
