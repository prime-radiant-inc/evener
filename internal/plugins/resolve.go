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

// PreviewForLaunch is ResolveForLaunch for inspection only. It readies the
// store the way a launch does, so a destination a launch would reject fails
// preview the same way, a store a launch could not publish into fails preview
// too, and an already published copy is the one preview describes. Exactly
// what it touches: it creates <Root>/bundled if that is missing, and for a
// requested bundled plugin not yet published it stages a marked copy there,
// reads it, and removes it before returning. It publishes nothing, and it
// reclaims nothing: collecting abandoned staging belongs to a launch. Removing
// what it staged is part of the promise, so a removal that fails is reported
// alongside whatever else went wrong; the marked directory stays in the store
// until a later launch's sweep reclaims it.
func (m *Manager) PreviewForLaunch(explicitDirs []string, enabledNames *[]string) (LaunchPluginResolution, error) {
	var scratch []string
	resolution, err := m.resolveForLaunch(explicitDirs, enabledNames, func(name string) (string, string, error) {
		dest, staging, err := m.prepareBundledStore(name, false)
		if err != nil {
			return "", "", err
		}
		if staging == nil {
			return dest, dest, nil
		}
		scratch = append(scratch, staging.dir)
		if err := os.CopyFS(staging.payload, mustSubFS(bundled.Plugins(), name)); err != nil {
			return "", "", fmt.Errorf("stage bundled plugin %s for preview: %w", name, err)
		}
		return staging.payload, dest, nil
	})
	for _, dir := range scratch {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			// A removal that fails partway takes the marker with it, and an
			// unmarked orphan is one no sweep will ever collect. Marking it
			// again is what leaves a later launch able to reclaim it.
			_ = os.WriteFile(filepath.Join(dir, stagingMarker), nil, 0o600)
			err = errors.Join(err, fmt.Errorf("remove staged bundled preview %s: %w", dir, cleanupErr))
		}
	}
	return resolution, err
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

	// Every path under an unresolved root is relative, so the store would be
	// whichever directory the process happens to be in: the registry would be
	// read from there, and an entry naming a requested plugin would answer the
	// request as an installed candidate before the bundled store was ever
	// consulted. Nothing derived from the root is read or created while it is
	// unresolved; explicit directories the caller named are its own business
	// and still resolve.
	rootErr := m.storeRootError()
	if rootErr != nil {
		resolution.Diagnostics = append(resolution.Diagnostics, LaunchPluginDiagnostic{
			Message: fmt.Sprintf("%v: installed and bundled plugins are unavailable", rootErr),
			Source:  LaunchPluginSourceBundled,
		})
	}

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

	if rootErr == nil {
		items, err := m.List()
		if err != nil {
			return resolution, err
		}
		for _, item := range items {
			if !item.Enabled {
				continue
			}
			// Deliberately do not filter item.Broken. List's validation is a
			// useful snapshot, but loading here gives Preview a structured
			// diagnostic and avoids turning a broken candidate into a
			// registry-level failure.
			add(item.InstallPath, item.InstallPath, LaunchPluginSourceInstalled, item.Marketplace, item.Version, item.Plugin)
		}
	}

	if enabledNames != nil {
		for _, name := range *enabledNames {
			if name == "" {
				resolution.SelectionErrors = append(resolution.SelectionErrors, PluginSelectionError{
					Name: name, Reason: "plugin name must not be empty",
				})
				continue
			}
			if !seen[name] && rootErr == nil {
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
// renaming it into place, and stagingMarker is the file inside it that proves
// this code created it. abandonedStaging is how stale such a directory must be
// before a later publish reclaims it: orders of magnitude longer than the copy
// it names, so a publish in flight is never disturbed.
const (
	stagingPrefix    = ".stage-"
	stagingMarker    = ".evener-staging"
	abandonedStaging = time.Hour
)

// bundledStaging is a private directory in the bundled store that a publish
// fills and then renames into place. The copy lives in payload, one level
// below the marked directory, so publishing renames the copy alone and the
// marker never lands inside a published plugin. digest is what the destination
// must hold, carried along so a publish that loses the rename can tell the
// winner's copy from foreign content.
type bundledStaging struct {
	dir     string
	payload string
	digest  string
}

// newBundledStaging opens a staging directory for base inside store and marks
// it as this code's to reclaim before anything is copied in, so a publish
// killed at any moment leaves an orphan a later sweep recognizes.
func newBundledStaging(store, base, digest string) (*bundledStaging, error) {
	dir, err := os.MkdirTemp(store, stagingPrefix+base+"-")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, stagingMarker), nil, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	payload := filepath.Join(dir, "payload")
	// Created with the staging directory's own mode: a published copy keeps
	// the private mode MkdirTemp gave it rather than CopyFS's 0o777 default.
	if err := os.Mkdir(payload, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &bundledStaging{dir: dir, payload: payload, digest: digest}, nil
}

// materializeBundledPlugin publishes the bundled plugin named name under the
// store root as <Root>/bundled/<name>-<digest>, where digest covers the
// plugin's embedded contents. A published directory is immutable: it is never
// rewritten or removed, so sessions already reading it are safe, and a new
// binary with different contents publishes beside it. Publication is atomic:
// the copy is staged in a private directory and renamed into place, and a
// concurrent publisher that loses the rename adopts the winner's copy.
func (m *Manager) materializeBundledPlugin(name string) (string, error) {
	dest, staging, err := m.prepareBundledStore(name, true)
	if err != nil {
		return "", err
	}
	if staging == nil {
		return dest, nil
	}
	defer func() { _ = os.RemoveAll(staging.dir) }()
	if err := os.CopyFS(staging.payload, mustSubFS(bundled.Plugins(), name)); err != nil {
		return "", fmt.Errorf("materialize bundled plugin %s: %w", name, err)
	}
	if err := os.Rename(staging.payload, dest); err != nil {
		// A concurrent publisher may have taken the destination first. Its
		// copy is adopted only when it is the copy this publish would have
		// made; anything else there is a conflict, reported as one.
		winner, adoptErr := publishedBundledCopy(dest, staging.digest)
		if adoptErr != nil {
			return "", adoptErr
		}
		if winner {
			return dest, nil
		}
		return "", fmt.Errorf("publish bundled plugin %s: %w", name, err)
	}
	return dest, nil
}

// prepareBundledStore readies <Root>/bundled to hold the bundled plugin named
// name. It returns the published destination and, when nothing is published
// there yet, a private staging directory to fill; staging is nil for a copy
// that is already published. Creating that directory is what proves the store
// can be published into, so a launch and a preview that share this preparation
// fail identically on a store neither can write. reclaim asks for the
// abandoned-staging sweep: a launch asks on every call, including the calls
// that adopt a published copy, and a preview never does.
func (m *Manager) prepareBundledStore(name string, reclaim bool) (string, *bundledStaging, error) {
	digest, err := bundledPluginDigest(name)
	if err != nil {
		return "", nil, err
	}
	// The resolver rejects an unresolved root before it reads or builds
	// anything; this is the same guard on the function that does the creating,
	// for any caller that arrives here directly. Checked after the digest so a
	// name that is not bundled at all still reports fs.ErrNotExist rather than
	// a store complaint.
	if err := m.storeRootError(); err != nil {
		return "", nil, fmt.Errorf("materialize bundled plugin %s: %w", name, err)
	}
	dest := m.bundledPluginPath(name, digest)
	published, err := publishedBundledCopy(dest, digest)
	if err != nil {
		return "", nil, err
	}
	store := filepath.Dir(dest)
	// A launch resolves plugins before the startup call that creates the user
	// config tree privately, so any parent this is first to create gets that
	// call's own 0o700 rather than the store root's readable mode.
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(store, 0o755); err != nil {
		return "", nil, err
	}
	// Sweeping before the published check, not after: a publisher that lost a
	// rename and died leaves an orphan that only ever meets callers taking the
	// published path.
	if reclaim {
		m.reclaimAbandonedStaging(store)
	}
	if published {
		return dest, nil, nil
	}
	staging, err := newBundledStaging(store, filepath.Base(dest), digest)
	if err != nil {
		return "", nil, fmt.Errorf("stage bundled plugin %s: %w", name, err)
	}
	return dest, staging, nil
}

// publishedBundledCopy reports whether dest already holds the published copy
// of the plugin digest names. Only a real directory counts: a file or a
// symlink there was not published by this code, and loading through it would
// report an unrelated directory as the bundled plugin. Only the right contents
// count as well: <name>-<digest> is a claim about what is inside, so a
// directory that hashes to anything else is somebody else's, whether it took
// the name in a publish race or was edited after it was published. Nothing is
// removed or rewritten either way — the caller reports the conflict and
// publishes nothing.
func publishedBundledCopy(dest, digest string) (bool, error) {
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
	found, err := digestFS(os.DirFS(dest))
	if err != nil {
		return false, fmt.Errorf("read the bundled plugin at %s: %w", dest, err)
	}
	if found != digest {
		return false, fmt.Errorf("bundled plugin path %s holds conflicting content", dest)
	}
	return true, nil
}

// reclaimAbandonedStaging removes staging directories orphaned by a publish
// that was killed before its rename. Staging lives in the store so the rename
// stays on one filesystem, so nothing else would ever collect them. Only this
// code's own staging is swept: a real directory, wearing the prefix, holding
// the marker a publish writes before it copies anything. Anything else at that
// name is someone else's data, however old it is.
func (m *Manager) reclaimAbandonedStaging(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), stagingPrefix) {
			continue
		}
		staging := filepath.Join(dir, entry.Name())
		if _, err := os.Lstat(filepath.Join(staging, stagingMarker)); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil || m.now().Sub(info.ModTime()) < abandonedStaging {
			continue
		}
		// Best effort: a concurrent publisher may be reclaiming the same orphan.
		_ = os.RemoveAll(staging)
	}
}

// storeRootError reports why the plugin store cannot be used. DefaultRoot
// returns "" when there is no XDG_CONFIG_HOME and no home directory, and every
// path built from that root would be relative to the process's working
// directory. cmdutil owns evener's config-root fallback and already depends on
// this package, so there is no fallback to share here.
func (m *Manager) storeRootError() error {
	if m.Root == "" {
		return errors.New("no plugin store root is configured")
	}
	return nil
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
	digest, err := digestFS(mustSubFS(src, name))
	if err != nil {
		return "", fmt.Errorf("digest bundled plugin %s: %w", name, err)
	}
	return digest, nil
}

// digestFS summarizes a whole tree: every name in walk order, and the length
// and contents of every file. A published copy is a copy of the embedded tree,
// so reading the copy back through os.DirFS digests to the value the embedded
// tree did, and a destination can be held to the digest its name promises.
func digestFS(fsys fs.FS) (string, error) {
	sum := sha256.New()
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			_, _ = fmt.Fprintf(sum, "dir %s\x00", path)
			return nil
		}
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(sum, "file %s %d\x00", path, len(content))
		_, _ = sum.Write(content)
		return nil
	})
	if err != nil {
		return "", err
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
