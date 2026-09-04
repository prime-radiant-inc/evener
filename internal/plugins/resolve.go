package plugins

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
func (m *Manager) ResolveForLaunch(ctx context.Context, explicitDirs []string, enabledNames *[]string) (LaunchPluginResolution, error) {
	return m.resolveForLaunch(ctx, explicitDirs, enabledNames, func(name string) (bundledCandidate, error) {
		path, warnings, err := m.materializeBundledPlugin(ctx, name)
		return bundledCandidate{loadPath: path, path: path, warnings: warnings}, err
	})
}

// PreviewForLaunch is ResolveForLaunch for inspection only. It readies the
// store the way a launch does, so a destination a launch would reject fails
// preview the same way, a store a launch could not publish into fails preview
// too, and an already published copy is the one preview describes. Exactly
// what it touches: it creates <Root>/bundled if that is missing, it takes the
// store lock while it works there, it moves a destination holding content this
// build did not publish to the sibling slot kept for it exactly as a launch
// would, and for a requested bundled plugin not yet published it stages a
// marked copy there, reads it, and removes it before returning. It publishes
// nothing, and it reclaims nothing: collecting abandoned staging belongs to a
// launch. Removing what it staged is part of the promise, so a removal that
// fails is reported as a diagnostic on the inventory it returns rather than as
// an error that would throw the inventory away; the marked directory stays in
// the store until a later launch's sweep reclaims it.
func (m *Manager) PreviewForLaunch(ctx context.Context, explicitDirs []string, enabledNames *[]string) (LaunchPluginResolution, error) {
	var scratch []string
	resolution, err := m.resolveForLaunch(ctx, explicitDirs, enabledNames, func(name string) (bundledCandidate, error) {
		dest, staging, warnings, err := m.prepareBundledStore(ctx, name, previewBundledStore)
		if err != nil {
			return bundledCandidate{path: dest, warnings: warnings}, err
		}
		if staging == nil {
			return bundledCandidate{loadPath: dest, path: dest, warnings: warnings}, nil
		}
		// Preview publishes nothing, so the store lock is only wanted for the
		// staging that fills; reading the copy back needs nothing from the
		// store, and removing a private directory disturbs nobody.
		defer staging.release()
		// The loop that removes staged copies runs on a normal return, so a
		// panic on the way there would leave this one behind. Handing the
		// payload back is what puts it in the loop's hands; until then it is
		// this closure's to remove, the way a publish removes its own.
		handed := false
		defer func() {
			if !handed {
				_ = os.RemoveAll(staging.dir)
			}
		}()
		scratch = append(scratch, staging.dir)
		if err := copyBundledPayload(staging.payload, mustSubFS(bundled.Plugins(), name)); err != nil {
			return bundledCandidate{path: dest, warnings: warnings}, fmt.Errorf("stage bundled plugin %s for preview: %w", name, err)
		}
		handed = true
		return bundledCandidate{loadPath: staging.payload, path: dest, warnings: warnings}, nil
	})
	for _, dir := range scratch {
		if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
			// A removal that fails partway takes the marker with it, and an
			// unmarked orphan is one no sweep will ever collect. Marking it
			// again is what leaves a later launch able to reclaim it.
			_ = os.WriteFile(filepath.Join(dir, stagingMarker), nil, 0o600)
			// A diagnostic rather than an error: the inventory the caller
			// asked for is complete, and failing the whole preview over the
			// cleanup after it would leave the caller with nothing to show.
			resolution.Diagnostics = append(resolution.Diagnostics, LaunchPluginDiagnostic{
				Path:    dir,
				Message: fmt.Sprintf("remove staged bundled preview %s: %v", dir, cleanupErr),
				Source:  LaunchPluginSourceBundled,
			})
		}
	}
	return resolution, err
}

// bundledCandidate is where a requested bundled plugin can be loaded from, the
// path to report for it, and whatever readying the store had to do that the
// caller should hear about.
type bundledCandidate struct {
	loadPath string
	path     string
	warnings []string
}

// resolveForLaunch builds the inventory. bundledPath readies a requested
// bundled plugin; it returns fs.ErrNotExist when no bundled plugin has that
// name.
func (m *Manager) resolveForLaunch(ctx context.Context, explicitDirs []string, enabledNames *[]string, bundledPath func(name string) (bundledCandidate, error)) (LaunchPluginResolution, error) {
	resolution := LaunchPluginResolution{
		Candidates: []LaunchPluginCandidate{}, SelectedDirs: []string{},
		Diagnostics: []LaunchPluginDiagnostic{}, SelectionErrors: []PluginSelectionError{},
	}
	seen := make(map[string]bool)

	// An inventory is something a caller acts on: the hub validates plugins
	// and then detaches from the request context to finish the spawn, so an
	// inventory handed back after the caller gave up is a session started for
	// a client that has gone. Nothing here necessarily blocks long enough to
	// notice on its own — a copy already published has no lock to wait on —
	// so the context is read rather than waited on.
	if err := ctx.Err(); err != nil {
		return nothingForACallerThatLeft(err)
	}

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
		// Keyed by the manifest name the loader reports, which is what the
		// requested names below are matched against. A bundled plugin is
		// requested by its embedded directory name, and
		// TestBundledPluginsAreNamedAfterTheirDirectory pins that the two are
		// the same string for every plugin this ships.
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
			// A caller that has left hears that it left, not a registry
			// failure: this one is fail-soft to every caller — the hub
			// launches on it when nothing was explicitly selected — so
			// answering with it would start a session for a client that has
			// gone.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nothingForACallerThatLeft(ctxErr)
			}
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
				candidate, err := bundledPath(name)
				// Readying the store can move somebody's directory aside and
				// then fail at the next step. What it moved is theirs, so they
				// hear where it went whether or not the plugin resolved.
				for _, warning := range candidate.warnings {
					resolution.Diagnostics = append(resolution.Diagnostics, LaunchPluginDiagnostic{
						Name: name, Path: candidate.path, Message: warning, Source: LaunchPluginSourceBundled,
					})
				}
				switch {
				case err == nil:
					add(candidate.loadPath, candidate.path, LaunchPluginSourceBundled, "", "", name)
				case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
					// The caller left while the store was being readied. That
					// is the same answer as a caller who had already left when
					// this started, so it is given the same way: carried as
					// the error, with no diagnostic and no missing-candidate
					// blaming the plugin for the caller leaving.
					return nothingForACallerThatLeft(err)
				case !errors.Is(err, fs.ErrNotExist):
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
	// Loading a candidate reads its whole tree off disk and takes no context,
	// so a caller that left while the inventory was being built is only
	// noticed here — the last chance before the inventory is handed to
	// somebody who would start a session on it.
	if err := ctx.Err(); err != nil {
		return nothingForACallerThatLeft(err)
	}
	return resolution, nil
}

// nothingForACallerThatLeft is the answer to a cancellation seen anywhere
// while the inventory is being built: an empty inventory carrying the context
// error. Nothing partial goes back — an inventory is something a caller acts
// on, and this caller is no longer there to act.
func nothingForACallerThatLeft(err error) (LaunchPluginResolution, error) {
	return LaunchPluginResolution{
		Candidates: []LaunchPluginCandidate{}, SelectedDirs: []string{},
		Diagnostics: []LaunchPluginDiagnostic{}, SelectionErrors: []PluginSelectionError{},
	}, err
}

// stagingPrefix names the private directory a publish copies into before
// renaming it into place, and stagingMarker is the file inside it that proves
// this code created it. abandonedStaging is how stale such a directory must be
// before a later publish reclaims it: orders of magnitude longer than the copy
// it names, so a publish in flight is never disturbed. conflictSuffix names the
// single sibling slot a destination is moved to when it holds content this
// build did not publish; it is outside the staging namespace, so the sweep
// never collects it.
const (
	stagingPrefix    = ".stage-"
	stagingMarker    = ".evener-staging"
	abandonedStaging = time.Hour
	conflictSuffix   = ".conflict"
	// previousSuffix names where the slot's previous occupant waits while the
	// destination is moved in: replaced only once there is something to
	// replace it with.
	previousSuffix = ".previous"
	// bundledPublishLockWait is how long readying the store waits for the
	// store lock, the same wait every other mutation takes. Publishing is what
	// the launch came for, so it waits like one. bundledSweepLockWait is what
	// the sweep waits on its way past: housekeeping nobody is waiting on, so a
	// lock that is busy is somebody else's turn rather than something to queue
	// behind.
	bundledPublishLockWait = 30 * time.Second
	bundledSweepLockWait   = time.Second
)

// copyBundledPayload writes an embedded plugin tree into a staging directory.
// Indirect because filling the staging directory is the only part of
// publishing that takes real time, and a test proving what a launch does about
// a caller who leaves during it has to be inside that window.
var copyBundledPayload = os.CopyFS

// bundledStaging is a private directory in the bundled store that a publish
// fills and then renames into place. The copy lives in payload, one level
// below the marked directory, so publishing renames the copy alone and the
// marker never lands inside a published plugin. digest is what the destination
// must hold, carried along so a publish that finds the destination taken can
// tell an identical copy from foreign content. release gives up the store lock
// the staging was opened under: the lock makes classifying the destination,
// setting a conflict aside and publishing one sequence, so it is held from the
// classification this staging was created on until the caller has renamed the
// copy into place or given up on it.
type bundledStaging struct {
	dir     string
	payload string
	digest  string
	release func()
	// setAside records that publication is taking a name whose previous
	// occupant was moved to the conflict slot. A publish that then fails owes
	// that occupant its name back: live sessions read the destination path for
	// hooks, skills and MCP server commands, so a path that simply vanishes
	// breaks them until some later launch happens to repair it.
	setAside bool
}

// newBundledStaging opens a staging directory for base inside store and marks
// it as this code's to reclaim before anything is copied in, so a publish
// killed at any moment leaves an orphan a later sweep recognizes. release is
// the store lock the caller holds, handed over for the staging to carry.
// stageBundledCopy makes the private directory a publish fills. Indirect
// because it is the step between freeing a name and having anything to put in
// it, and a test proving what a publish does when that step fails has to be
// inside that window.
var stageBundledCopy = newBundledStaging

func newBundledStaging(store, base, digest string, release func()) (*bundledStaging, error) {
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
	return &bundledStaging{dir: dir, payload: payload, digest: digest, release: release}, nil
}

// materializeBundledPlugin publishes the bundled plugin named name under the
// store root as <Root>/bundled/<name>-<digest>, where digest covers the
// plugin's embedded contents. A published directory is immutable: it is never
// rewritten or removed, so sessions already reading it are safe, and a new
// binary with different contents publishes beside it. Publication is atomic:
// the copy is staged in a private directory and renamed into place, and a
// concurrent publisher that loses the rename adopts the winner's copy. A
// destination holding anything else is set aside rather than adopted, and the
// publish goes ahead into the name it freed. It reports the published path and
// anything the caller should hear about getting there.
func (m *Manager) materializeBundledPlugin(ctx context.Context, name string) (string, []string, error) {
	dest, staging, warnings, err := m.prepareBundledStore(ctx, name, publishBundledStore)
	if err != nil {
		return "", warnings, err
	}
	if staging == nil {
		return dest, warnings, nil
	}
	// The lock goes last, so the staging directory is gone before another
	// publisher is let in to look at the store.
	defer staging.release()
	defer func() { _ = os.RemoveAll(staging.dir) }()
	// A publish that took a name by moving its occupant aside owes that
	// occupant the name back if it then fails: nothing else is going to put it
	// there, and a session reading the path for a hook, a skill or an MCP
	// server command would find nothing at all.
	setAside := staging.setAside
	failed := func(warnings []string, err error) (string, []string, error) {
		if setAside {
			warnings = append(warnings, restoreBundledConflict(dest)...)
		}
		return "", warnings, err
	}
	if err := copyBundledPayload(staging.payload, mustSubFS(bundled.Plugins(), name)); err != nil {
		return failed(warnings, fmt.Errorf("materialize bundled plugin %s: %w", name, err))
	}
	if err := os.Rename(staging.payload, dest); err != nil {
		// The store lock keeps every other publisher out, so a destination
		// that filled since it was classified was filled by something outside
		// this package. An identical copy is adopted; anything else is set
		// aside the way the classification would have, and the publish is
		// retried into the name that frees.
		state, classifyErr := classifyBundledDestination(dest, staging.digest)
		if classifyErr != nil {
			return failed(warnings, classifyErr)
		}
		if state == bundledDestinationPublished {
			return publishedForCaller(ctx, dest, warnings)
		}
		if state != bundledDestinationConflict {
			return failed(warnings, fmt.Errorf("publish bundled plugin %s: %w", name, err))
		}
		asideWarnings, asideErr := setAsideBundledConflict(dest)
		warnings = append(warnings, asideWarnings...)
		if asideErr != nil {
			return failed(warnings, asideErr)
		}
		setAside = true
		if err := os.Rename(staging.payload, dest); err != nil {
			return failed(warnings, fmt.Errorf("publish bundled plugin %s: %w", name, err))
		}
	}
	return publishedForCaller(ctx, dest, warnings)
}

// restoreBundledConflict puts back what a publish set aside, after that publish
// failed to put anything in the name it freed. The destination is the one path
// live sessions hold — hooks, skills, MCP server commands — so leaving it
// empty is worse than leaving the mismatched copy that was there. Nothing is
// restored over a destination that is occupied again: under the store lock the
// only thing that could have filled it is something outside this package, and
// that is a conflict for the next launch to classify rather than something to
// overwrite here.
func restoreBundledConflict(dest string) []string {
	aside := dest + conflictSuffix
	if _, err := os.Lstat(dest); err == nil {
		return nil
	}
	if _, err := os.Lstat(aside); err != nil {
		return nil
	}
	if err := os.Rename(aside, dest); err != nil {
		return []string{fmt.Sprintf("publishing the bundled plugin failed and the copy set aside at %s could not be put back at %s: %v", aside, dest, err)}
	}
	return []string{fmt.Sprintf("publishing the bundled plugin failed, so the copy set aside at %s was put back at %s", aside, dest)}
}

// publishedForCaller hands back the published path only while the caller is
// still waiting for it. Copying the payload into the store and renaming it
// into place is the slow part of a launch, and none of it is work to abandon
// halfway — the copy is finished and stays published for the launches that
// follow — so the context is read on the way out instead, and a caller that
// left during it is told so rather than handed a plugin to start a session
// with.
func publishedForCaller(ctx context.Context, dest string, warnings []string) (string, []string, error) {
	if err := ctx.Err(); err != nil {
		return "", warnings, err
	}
	return dest, warnings, nil
}

// bundledStoreIntent is what a caller is entitled to do to the store on its
// way past. A launch publishes: it sweeps abandoned staging, and it moves a
// destination holding something else aside so the copy it publishes can take
// the name. A preview only describes: it stages a private copy to read and
// leaves everything already there exactly as it found it, because repairing is
// a launch's job and a preview that repaired would take a path live sessions
// read — for hooks, skills and MCP server commands — out from under them, with
// nothing published in its place.
type bundledStoreIntent int

const (
	previewBundledStore bundledStoreIntent = iota
	publishBundledStore
)

// prepareBundledStore readies <Root>/bundled to hold the bundled plugin named
// name. It returns the published destination and, when nothing is published
// there yet, a private staging directory to fill; staging is nil for a copy
// that is already published. Creating that directory is what proves the store
// can be published into, so a launch and a preview that share this preparation
// fail identically on a store neither can write. intent says which of the two
// is asking: only a publish sweeps abandoned staging (on every call, including
// the ones that adopt a published copy) and only a publish moves a conflicting
// destination aside.
func (m *Manager) prepareBundledStore(ctx context.Context, name string, intent bundledStoreIntent) (string, *bundledStaging, []string, error) {
	reclaim := intent == publishBundledStore
	digest, err := bundledPluginDigest(name)
	if err != nil {
		return "", nil, nil, err
	}
	// The resolver rejects an unresolved root before it reads or builds
	// anything; this is the same guard on the function that does the creating,
	// for any caller that arrives here directly. Checked after the digest so a
	// name that is not bundled at all still reports fs.ErrNotExist rather than
	// a store complaint.
	if err := m.storeRootError(); err != nil {
		return "", nil, nil, fmt.Errorf("materialize bundled plugin %s: %w", name, err)
	}
	dest := m.bundledPluginPath(name, digest)
	store := filepath.Dir(dest)
	// A launch resolves plugins before the startup call that creates the user
	// config tree privately, so any parent this is first to create gets that
	// call's own 0o700 rather than the store root's readable mode. They are
	// created before the lock so the lock file's parent is one of them, rather
	// than acquireLock creating the store root world-readable on its own.
	if err := os.MkdirAll(filepath.Dir(store), 0o700); err != nil {
		return "", nil, nil, err
	}
	if err := os.MkdirAll(store, 0o755); err != nil {
		return "", nil, nil, err
	}
	// An unlocked look first, because the answer is almost always a copy
	// already published: that costs a digest read, changes nothing, and no
	// other publisher can take a published copy away. Only that answer is
	// taken from it. Anything else, including a read that failed because a
	// concurrent publisher was moving a conflicting destination aside as this
	// walked it, falls through to the locked look, which is the one entitled
	// to an opinion.
	if state, err := classifyBundledDestination(dest, digest); err == nil && state == bundledDestinationPublished {
		// Read again on the way out: the digest walk above is the one part of
		// adopting that takes real time, and a caller that left during it is
		// no more entitled to a copy than one that had already left.
		if err := ctx.Err(); err != nil {
			return "", nil, nil, err
		}
		// A launch owes the store its sweep, but only when the store has
		// something to sweep, and the scan that answers that needs no lock.
		// Taking one on every launch would park a routine one behind an
		// auto-upgrade holding the store lock across git fetches.
		if reclaim && len(m.abandonedStaging(store)) > 0 {
			// Housekeeping for a launch that is otherwise done: it waits the
			// housekeeping wait, not the wait a publish is entitled to — the
			// wait acquireLock is given here is the whole of it. A lock it
			// does not get quickly is left to the next launch rather than
			// making this one queue behind whatever holds it. Whether the
			// orphans are really abandoned is decided again under the lock.
			if release, lockErr := acquireLock(ctx, m.lockPath(), bundledSweepLockWait); lockErr == nil {
				m.reclaimAbandonedStaging(store)
				release()
			}
			// A lock failure the sweep is entitled to ignore is also how the
			// caller's own cancellation arrives, and waiting for it is the
			// last thing this launch does. So the context is read once more
			// rather than handing back a copy nobody is waiting for.
			if err := ctx.Err(); err != nil {
				return "", nil, nil, err
			}
		}
		return dest, nil, nil, nil
	}
	// Everything from here writes to the store, or decides what to write, and
	// that is one sequence with the classification it acts on: without the
	// lock two launches both classify a mismatched destination, and the second
	// sets aside the copy the first published while deleting the copy the
	// first preserved.
	release, err := acquireLock(ctx, m.lockPath(), bundledPublishLockWait)
	if err != nil {
		return "", nil, nil, fmt.Errorf("stage bundled plugin %s: %w", name, err)
	}
	// Sweeping under the lock, and before the published check: staging that
	// looks abandoned belongs to a publisher that is merely slow unless this
	// holds the lock that publisher would be holding, and an orphan left by a
	// publisher that lost a rename and died only ever meets callers taking the
	// published path.
	if reclaim {
		m.reclaimAbandonedStaging(store)
	}
	state, err := classifyBundledDestination(dest, digest)
	if err != nil {
		release()
		return "", nil, nil, err
	}
	if state == bundledDestinationPublished {
		release()
		if err := ctx.Err(); err != nil {
			return "", nil, nil, err
		}
		return dest, nil, nil, nil
	}
	var warnings []string
	setAside := false
	if state == bundledDestinationConflict {
		if intent == previewBundledStore {
			// A preview says what a launch would do here and does none of it.
			warnings = append(warnings, fmt.Sprintf(
				"bundled plugin path %s holds content this build did not publish; a launch would set it aside at %s",
				dest, dest+conflictSuffix))
		} else {
			asideWarnings, err := setAsideBundledConflict(dest)
			warnings = append(warnings, asideWarnings...)
			if err != nil {
				release()
				return "", nil, warnings, err
			}
			setAside = true
		}
	}
	staging, err := stageBundledCopy(store, filepath.Base(dest), digest, release)
	if err != nil {
		// The destination is empty at this point and nothing is going to fill
		// it, so what was moved out of the name goes back into it.
		if setAside {
			warnings = append(warnings, restoreBundledConflict(dest)...)
		}
		release()
		return "", nil, warnings, fmt.Errorf("stage bundled plugin %s: %w", name, err)
	}
	staging.setAside = setAside
	return dest, staging, warnings, nil
}

// bundledDestination is what a content-addressed destination turned out to
// hold.
type bundledDestination int

const (
	// bundledDestinationVacant: nothing is there to adopt.
	bundledDestinationVacant bundledDestination = iota
	// bundledDestinationPublished: the copy the digest names.
	bundledDestinationPublished
	// bundledDestinationConflict: a directory holding something else.
	bundledDestinationConflict
)

// classifyBundledDestination reports what is at dest. Only a real directory is
// considered at all: a file or a symlink there was not published by this code
// and is not this code's to move, and loading through it would report an
// unrelated directory as the bundled plugin, so it is an error. Among
// directories, <name>-<digest> is a claim about the contents: only a tree that
// hashes to digest is the published copy, and anything else is a conflict — a
// foreign directory that took the name, a copy this code published that has
// changed since, or a tree this code cannot read through to tell.
func classifyBundledDestination(dest, digest string) (bundledDestination, error) {
	info, err := os.Lstat(dest)
	if errors.Is(err, fs.ErrNotExist) {
		return bundledDestinationVacant, nil
	}
	if err != nil {
		return bundledDestinationVacant, err
	}
	if !info.IsDir() {
		return bundledDestinationVacant, fmt.Errorf("bundled plugin path %s is not a directory", dest)
	}
	found, err := digestFS(os.DirFS(dest))
	if err != nil {
		// A tree that cannot be read through is not a tree that hashes to
		// digest, so it is a conflict like any other: somebody else's
		// directory under this name (errIrregularContent), or one whose
		// permissions or media keep this from reading it. Treating that as a
		// failure instead would leave the store unusable for the plugin
		// forever — never launching, never moving the directory aside — while
		// setting it aside preserves it whole and needs nothing from it but
		// the parent's write bit.
		return bundledDestinationConflict, nil //nolint:nilerr // the read failure IS the classification: an unreadable tree is a mismatch, not a store failure
	}
	if found != digest {
		return bundledDestinationConflict, nil
	}
	return bundledDestinationPublished, nil
}

// setAsideBundledConflict frees a destination holding content this build did
// not publish, so publication can put the matching copy there rather than the
// store staying unusable for that plugin forever. The conflicting directory is
// moved, never deleted, to the single sibling slot kept beside the
// destination: one preserved copy per plugin, so a store that keeps meeting
// conflicts does not grow without bound. Callers hold the store lock, so
// nothing else this package runs is looking at the destination meanwhile. It
// reports what the caller should say about what it moved: nothing moves when
// the destination is already gone, which under the lock means something
// outside this package took it away.
func setAsideBundledConflict(dest string) ([]string, error) {
	aside := dest + conflictSuffix
	previous := aside + previousSuffix
	// A set-aside interrupted between its two renames left the copy it was
	// preserving under previous with the slot itself empty. That copy is the
	// only one there is, so putting it back comes before anything else: to
	// everything below, previous is an occupant already replaced, and this one
	// never was.
	if _, err := os.Lstat(aside); errors.Is(err, fs.ErrNotExist) {
		if _, err := os.Lstat(previous); err == nil {
			if err := os.Rename(previous, aside); err != nil {
				return nil, fmt.Errorf("restore the bundled plugin path %s: %w", previous, err)
			}
		}
	}
	// Whatever the slot holds is moved out of the way rather than deleted, so
	// a destination that turns out not to be movable leaves the copy already
	// preserved still preserved. Callers hold the store lock, so this name is
	// nobody else's; anything under it is residue from a publish that died
	// between the two renames below, and is the occupant being replaced.
	if err := os.RemoveAll(previous); err != nil {
		return nil, fmt.Errorf("clear the bundled plugin path %s: %w", previous, err)
	}
	occupied := true
	if err := os.Rename(aside, previous); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("set aside the bundled plugin path %s: %w", aside, err)
		}
		occupied = false
	}
	if err := os.Rename(dest, aside); err != nil {
		var warnings []string
		if occupied {
			// Nothing took the slot, so the copy that was in it goes back. If
			// it cannot go back, it is still on disk under a name only the
			// next set-aside looks at, and whoever is reading diagnostics is
			// the one who can do something about it.
			if restoreErr := os.Rename(previous, aside); restoreErr != nil {
				warnings = append(warnings, fmt.Sprintf("the bundled plugin copy set aside at %s could not be put back and is now at %s: %v", aside, previous, restoreErr))
			}
		}
		if errors.Is(err, fs.ErrNotExist) {
			return warnings, nil
		}
		return warnings, fmt.Errorf("set aside the bundled plugin path %s: %w", dest, err)
	}
	if occupied {
		// Only now, with the slot filled again, is what it held replaceable.
		// Best effort: a leftover is cleared by the next set-aside, and it is
		// outside the staging namespace, so no sweep will take it for staging.
		_ = os.RemoveAll(previous)
	}
	return []string{fmt.Sprintf("bundled plugin path %s held content this build did not publish; it was set aside at %s", dest, aside)}, nil
}

// abandonedStaging names the staging directories in dir a publish left behind
// when it was killed before its rename. Staging lives in the store so the
// rename stays on one filesystem, so nothing else would ever collect them.
// Only this code's own staging counts: a real directory, wearing the prefix,
// holding as a regular file the marker a publish writes before it copies
// anything, and old enough that no publish in flight could still be filling
// it. Anything else at that name is someone else's data, however old it is.
func (m *Manager) abandonedStaging(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var abandoned []string
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), stagingPrefix) {
			continue
		}
		staging := filepath.Join(dir, entry.Name())
		// The marker is the proof, so it has to be the file this code writes:
		// a directory or a symlink wearing that name is somebody else's
		// arrangement, and a symlink is not even about anything in the store.
		marker, err := os.Lstat(filepath.Join(staging, stagingMarker))
		if err != nil || !marker.Mode().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || m.now().Sub(info.ModTime()) < abandonedStaging {
			continue
		}
		abandoned = append(abandoned, staging)
	}
	return abandoned
}

// reclaimAbandonedStaging removes them, reading the store again so the decision
// it acts on is the one it made under the lock its caller holds: that lock is
// what tells staging nobody will come back for from staging a slow publisher
// is still filling.
func (m *Manager) reclaimAbandonedStaging(dir string) {
	for _, staging := range m.abandonedStaging(dir) {
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
	// A relative root is resolved the same way an absent one is: against
	// whatever directory the process happens to be in, which is somebody's
	// project rather than a store. Whether it came from a relative
	// XDG_CONFIG_HOME (which the spec says to ignore) or a hand-typed
	// --plugin-root, a store that moves with the working directory is not one
	// this can read or write.
	if !filepath.IsAbs(m.Root) {
		return fmt.Errorf("plugin store root %q is not an absolute path", m.Root)
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

// maxBundledFileBytes bounds what a single file in a bundled plugin may be
// before the tree holding it stops looking like a copy of one. Bundled plugins
// are manifests and markdown, orders of magnitude under this; the bound is
// there so a foreign directory that took the destination cannot make a launch
// read an arbitrarily large file to find that out.
const maxBundledFileBytes = 64 << 20

// errIrregularContent marks a tree that cannot be a copy of an embedded
// plugin: an entry that is neither a regular file nor a directory, or a file
// too large for anything this ships. It is a mismatch to classify, not a
// failure to read.
var errIrregularContent = errors.New("not a copy of an embedded plugin")

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
		// Decided from the directory entry, before anything is opened. An
		// embedded plugin is regular files and directories; a symlink reads as
		// whatever it points at today and can point somewhere else tomorrow, a
		// FIFO blocks the open until somebody writes to it, and a device is
		// not something to read at all.
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: %w", path, errIrregularContent)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxBundledFileBytes {
			return fmt.Errorf("%s: %w", path, errIrregularContent)
		}
		file, err := fsys.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		_, _ = fmt.Fprintf(sum, "file %s %d\x00", path, info.Size())
		// Streamed rather than read whole, and never further than the size the
		// entry declared: a file growing under the walk cannot be read past
		// the bound, and one that no longer matches its own size is not the
		// copy this is trying to recognize.
		read, err := io.Copy(sum, io.LimitReader(file, maxBundledFileBytes))
		if err != nil {
			return err
		}
		if read != info.Size() {
			return fmt.Errorf("%s: %w", path, errIrregularContent)
		}
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
