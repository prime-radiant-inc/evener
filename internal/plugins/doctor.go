package plugins

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Doctor finding levels.
const (
	LevelOK   = "OK"
	LevelWarn = "WARN"
	LevelFail = "FAIL"
)

// Doctor finding categories, one per §13 check group of the design spec.
const (
	catRegistry    = "registry"
	catMarketplace = "marketplace"
	catComponent   = "component"
	catAutoUpgrade = "autoupgrade"
	catEnvironment = "environment"
)

// marketplaceStaleAfter is how long since a git-backed marketplace's last
// fetch/pull before doctor flags it as stale. Directory sources have no
// "pull" and are never flagged for staleness.
const marketplaceStaleAfter = 30 * 24 * time.Hour

// DoctorFinding is one read-only plugin-store health check result. Level is
// one of LevelOK, LevelWarn, LevelFail.
type DoctorFinding struct {
	Level       string `json:"level"`
	Category    string `json:"category"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

// Doctor runs every read-only plugin-store health check (design spec §13):
// registry-vs-disk drift, marketplace health, per-plugin component validity,
// auto-upgrade sanity, and the environment (git, store writability). It never
// mutates store state.
//
// A returned error means the store's own bookkeeping (installed_plugins.json
// or known_marketplaces.json) failed to parse — the same failure every other
// Manager verb would hit. A per-plugin or per-marketplace problem is never
// returned as an error; it becomes a FAIL finding instead, so the rest of the
// report is still useful.
func (m *Manager) Doctor() ([]DoctorFinding, error) {
	reg, err := LoadRegistry(m.registryPath())
	if err != nil {
		return nil, err
	}
	mk, err := m.loadMarketplaces()
	if err != nil {
		return nil, err
	}

	var findings []DoctorFinding
	knownPaths := map[string]bool{}

	keys := make([]string, 0, len(reg.Plugins))
	for key := range reg.Plugins {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entries := reg.Plugins[key]
		if len(entries) == 0 {
			continue
		}
		e := entries[0]
		knownPaths[filepath.Clean(e.InstallPath)] = true
		findings = append(findings, m.doctorEntry(key, e)...)
	}

	findings = append(findings, m.doctorOrphanCacheDirs(knownPaths)...)

	names := make([]string, 0, len(mk))
	for name := range mk {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		findings = append(findings, m.doctorMarketplace(name, mk[name]))
	}

	findings = append(findings, m.doctorEnvironment()...)
	return findings, nil
}

// doctorEntry checks one registry entry: install-path drift, on-disk version
// drift, component validity (enabled entries only), and auto-upgrade sanity.
// A missing or non-directory install path is the one blocking problem — it
// short-circuits the rest, since there is nothing left at that path to check.
func (m *Manager) doctorEntry(key string, e InstallEntry) []DoctorFinding {
	info, err := os.Stat(e.InstallPath)
	if err != nil {
		return []DoctorFinding{{
			Level: LevelFail, Category: catRegistry,
			Message:     fmt.Sprintf("%s: install path %s is inaccessible: %v", key, e.InstallPath, err),
			Remediation: fmt.Sprintf("run `serf plugin remove %s` to drop the orphaned entry, or `serf plugin upgrade %s` to re-materialize it", key, key),
		}}
	}
	if !info.IsDir() {
		return []DoctorFinding{{
			Level: LevelFail, Category: catRegistry,
			Message:     fmt.Sprintf("%s: install path %s is not a directory", key, e.InstallPath),
			Remediation: fmt.Sprintf("run `serf plugin remove %s` to drop the orphaned entry", key),
		}}
	}

	var findings []DoctorFinding

	if diskVer := pluginManifestVersion(e.InstallPath); diskVer != "" && diskVer != e.Version {
		// A source that can never upgrade (install.go's stagePlugin/Upgrade
		// short-circuit) would make "run upgrade" a permanent no-op, so it
		// gets an honest alternative instead of a remediation that can never
		// clear the warning.
		remediation := fmt.Sprintf("run `serf plugin upgrade %s` to resync the registry", key)
		if sourceCannotUpgrade(e.Source) {
			remediation = fmt.Sprintf("%s's plugin.json was edited in place, which is expected for a directory source; run `serf plugin remove %s` then `serf plugin install %s` to resync the recorded version", key, key, key)
		}
		findings = append(findings, DoctorFinding{
			Level: LevelWarn, Category: catRegistry,
			Message:     fmt.Sprintf("%s: registry version %q does not match on-disk plugin.json version %q", key, e.Version, diskVer),
			Remediation: remediation,
		})
	}

	if e.Enabled {
		if err := validatePluginDir(e.InstallPath); err != nil {
			findings = append(findings, DoctorFinding{
				Level: LevelFail, Category: catComponent,
				Message:     fmt.Sprintf("%s: %v", key, err),
				Remediation: fmt.Sprintf("fix the plugin's manifest/components, or run `serf plugin disable %s`", key),
			})
		} else {
			findings = append(findings, DoctorFinding{
				Level: LevelOK, Category: catComponent,
				Message: key + ": loads cleanly",
			})
		}
	}

	if e.AutoUpgrade && sourceCannotUpgrade(e.Source) {
		findings = append(findings, DoctorFinding{
			Level: LevelWarn, Category: catAutoUpgrade,
			Message:     key + ": auto-upgrade is on but the source is a directory reference, which can never produce a new version",
			Remediation: fmt.Sprintf("turn off auto-upgrade for %s; it has no effect on an in-place directory source", key),
		})
	}

	return findings
}

// sourceCannotUpgrade mirrors UpdateAll's skip condition (install.go): a
// directory-kind source or a marketplace-relative "./subdir" copy is
// referenced or copied in place and has no git remote to fetch a new version
// from, so auto-upgrade can never do anything for it.
func sourceCannotUpgrade(src Source) bool {
	return src.Rel || src.Kind == SourceDirectory
}

// doctorOrphanCacheDirs walks cache/<marketplace>/<plugin>/<sha> and flags any
// sha-dir that no registry entry's InstallPath points at — left behind by a
// crash mid-install/upgrade, or a superseded dir awaiting the gc sweep (§12).
func (m *Manager) doctorOrphanCacheDirs(knownPaths map[string]bool) []DoctorFinding {
	marketplaceEnts, err := os.ReadDir(m.cacheDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return []DoctorFinding{{
			Level: LevelFail, Category: catRegistry,
			Message: fmt.Sprintf("reading cache directory %s: %v", m.cacheDir(), err),
		}}
	}

	var findings []DoctorFinding
	for _, mktEnt := range marketplaceEnts {
		if !mktEnt.IsDir() {
			continue
		}
		mktPath := filepath.Join(m.cacheDir(), mktEnt.Name())
		pluginEnts, err := os.ReadDir(mktPath)
		if err != nil {
			continue
		}
		for _, plugEnt := range pluginEnts {
			if !plugEnt.IsDir() {
				continue
			}
			plugPath := filepath.Join(mktPath, plugEnt.Name())
			shaEnts, err := os.ReadDir(plugPath)
			if err != nil {
				continue
			}
			for _, shaEnt := range shaEnts {
				if !shaEnt.IsDir() {
					continue
				}
				shaPath := filepath.Join(plugPath, shaEnt.Name())
				if knownPaths[filepath.Clean(shaPath)] {
					continue
				}
				findings = append(findings, DoctorFinding{
					Level: LevelWarn, Category: catRegistry,
					Message:     fmt.Sprintf("orphaned cache directory %s has no registry entry", shaPath),
					Remediation: "run `serf plugin gc` to reclaim disk space",
				})
			}
		}
	}
	return findings
}

// doctorMarketplace checks one known marketplace. An unfetched seed pointer
// (empty InstallLocation) is healthy by design — it is cloned lazily on first
// use. Otherwise the clone must exist, be a valid git repo (for non-directory
// sources), and parse as a catalog; staleness is flagged separately and only
// once the marketplace is otherwise healthy.
func (m *Manager) doctorMarketplace(name string, ref MarketplaceRef) DoctorFinding {
	if ref.InstallLocation == "" {
		return DoctorFinding{Level: LevelOK, Category: catMarketplace, Message: name + ": seeded, not yet fetched"}
	}

	// A directory source has nothing to fetch — RefreshMarketplace only bumps
	// its timestamp for one — so a broken pointer needs re-adding, not a
	// refresh.
	fixIt := fmt.Sprintf("run `serf plugin marketplace refresh %s`", name)
	if ref.Source.Kind == SourceDirectory {
		fixIt = fmt.Sprintf("the referenced directory is gone or invalid; run `serf plugin marketplace remove %s` and re-add it with a valid path", name)
	}

	info, err := os.Stat(ref.InstallLocation)
	if err != nil || !info.IsDir() {
		return DoctorFinding{
			Level: LevelFail, Category: catMarketplace,
			Message:     fmt.Sprintf("%s: install location %s is missing or not a directory", name, ref.InstallLocation),
			Remediation: fixIt,
		}
	}

	if ref.Source.Kind != SourceDirectory && gitAvailable() {
		if _, err := git(context.Background(), ref.InstallLocation, "rev-parse", "--git-dir"); err != nil {
			return DoctorFinding{
				Level: LevelFail, Category: catMarketplace,
				Message:     fmt.Sprintf("%s: %s is not a valid git repository", name, ref.InstallLocation),
				Remediation: fixIt,
			}
		}
	}

	if _, err := ParseCatalog(m.catalogRoot(ref)); err != nil {
		return DoctorFinding{
			Level: LevelFail, Category: catMarketplace,
			Message:     fmt.Sprintf("%s: marketplace.json invalid: %v", name, err),
			Remediation: fixIt,
		}
	}

	if ref.Source.Kind != SourceDirectory {
		if age := m.now().Sub(ref.LastUpdated); age > marketplaceStaleAfter {
			return DoctorFinding{
				Level: LevelWarn, Category: catMarketplace,
				Message:     fmt.Sprintf("%s: last updated %s ago", name, age.Round(time.Hour)),
				Remediation: fmt.Sprintf("run `serf plugin marketplace refresh %s`", name),
			}
		}
	}

	return DoctorFinding{Level: LevelOK, Category: catMarketplace, Message: name + ": healthy"}
}

// doctorEnvironment checks the two preconditions every other operation
// depends on: git for fetch/clone/pull, and a writable store root.
func (m *Manager) doctorEnvironment() []DoctorFinding {
	var findings []DoctorFinding
	if gitAvailable() {
		findings = append(findings, DoctorFinding{Level: LevelOK, Category: catEnvironment, Message: "git is available on PATH"})
	} else {
		findings = append(findings, DoctorFinding{
			Level: LevelWarn, Category: catEnvironment,
			Message:     "git not found on PATH",
			Remediation: "install git; marketplace/plugin fetch, clone, and upgrade all shell out to it",
		})
	}

	switch exists, err := m.checkStoreWritable(); {
	case err != nil:
		findings = append(findings, DoctorFinding{
			Level: LevelFail, Category: catEnvironment,
			Message:     fmt.Sprintf("store root %s is not writable: %v", m.Root, err),
			Remediation: "check ownership and permissions on " + m.Root,
		})
	case !exists:
		findings = append(findings, DoctorFinding{
			Level: LevelOK, Category: catEnvironment,
			Message: fmt.Sprintf("store root %s does not exist yet (created on first plugin or marketplace operation)", m.Root),
		})
	default:
		findings = append(findings, DoctorFinding{Level: LevelOK, Category: catEnvironment, Message: fmt.Sprintf("store root %s is writable", m.Root)})
	}
	return findings
}

// checkStoreWritable probes store-root writability WITHOUT creating it —
// Doctor must never mutate store state. A root that does not exist yet (a
// fresh machine, before any plugin/marketplace operation has run) is reported
// via exists=false rather than treated as a failure. When the root does
// exist, it is probed with a throwaway temp file, without disturbing any real
// state.
func (m *Manager) checkStoreWritable() (exists bool, err error) {
	info, statErr := os.Stat(m.Root)
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return false, nil
		}
		return false, statErr
	}
	if !info.IsDir() {
		return true, fmt.Errorf("%s is not a directory", m.Root)
	}

	f, err := os.CreateTemp(m.Root, ".doctor-write-test-*")
	if err != nil {
		return true, err
	}
	name := f.Name()
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(name)
		return true, cerr
	}
	return true, os.Remove(name)
}

// RenderDoctorFindings renders findings as a human-readable report: an
// OK/WARN/FAIL summary line, then every finding grouped by category (in the
// order each category first appears) with its remediation, if any.
func RenderDoctorFindings(findings []DoctorFinding) string {
	var b strings.Builder
	var ok, warn, fail int
	for _, f := range findings {
		switch f.Level {
		case LevelOK:
			ok++
		case LevelWarn:
			warn++
		case LevelFail:
			fail++
		}
	}
	fmt.Fprintf(&b, "%d OK, %d WARN, %d FAIL\n", ok, warn, fail)

	var order []string
	byCategory := map[string][]DoctorFinding{}
	for _, f := range findings {
		if _, seen := byCategory[f.Category]; !seen {
			order = append(order, f.Category)
		}
		byCategory[f.Category] = append(byCategory[f.Category], f)
	}
	for _, cat := range order {
		fmt.Fprintf(&b, "\n[%s]\n", cat)
		for _, f := range byCategory[cat] {
			fmt.Fprintf(&b, "  %-4s %s\n", f.Level, f.Message)
			if f.Remediation != "" {
				fmt.Fprintf(&b, "       -> %s\n", f.Remediation)
			}
		}
	}
	return b.String()
}
