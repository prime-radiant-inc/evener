package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"
)

var (
	marketplaceReadFile        = os.ReadFile
	marketplaceMarshalIndent   = json.MarshalIndent
	marketplaceAtomicWriteFile = atomicWriteFile
	marketplaceGitClone        = gitClone
	marketplaceGitSparseClone  = gitSparseClone
	marketplaceGitPull         = gitPull
	marketplaceRemoveAll       = os.RemoveAll
	marketplaceRemove          = os.Remove
	marketplaceRename          = os.Rename
	marketplaceMkdirAll        = os.MkdirAll
	marketplaceAcquireLock     = acquireLock
	marketplaceStat            = os.Stat
	marketplaceLstat           = os.Lstat
)

// The two fixed scratch directories under the marketplaces dir: a fetch lands
// in staging before anything it would replace is touched, and the clone being
// replaced is renamed aside until the store files say it is no longer needed.
// Named once because a rename onto either of them is refused by name.
const (
	stagingCloneName = ".staging"
	asideCloneName   = ".old"
)

type MarketplaceRef struct {
	Source          Source    `json:"source"`
	InstallLocation string    `json:"installLocation"` //nolint:tagliatelle // matches Claude Code plugin/marketplace JSON schema
	LastUpdated     time.Time `json:"lastUpdated"`     //nolint:tagliatelle // matches Claude Code plugin/marketplace JSON schema
}

type Marketplaces map[string]MarketplaceRef

// catalogRoot is the directory holding .claude-plugin/marketplace.json for a
// registered marketplace. For a git-subdir source the manifest lives in the
// subdir under the clone root; otherwise it is InstallLocation itself.
func (m *Manager) catalogRoot(ref MarketplaceRef) string {
	if ref.Source.Kind == SourceGitSubdir {
		return filepath.Join(ref.InstallLocation, ref.Source.Path)
	}
	return ref.InstallLocation
}

// loadMarketplaces and saveMarketplaces are the only ways this package reaches
// known_marketplaces.json. Both derive the path through storePath, so
// ListMarketplaces — which reads without the store lock — refuses an
// unresolved root instead of handing back the working directory's file.
func (m *Manager) loadMarketplaces() (Marketplaces, error) {
	path, err := m.storePath(marketplacesFileName)
	if err != nil {
		return nil, err
	}
	data, err := marketplaceReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Marketplaces{}, nil
		}
		return nil, m.storeFileFailed(err, "reading %s", marketplacesFileName)
	}
	var mk Marketplaces
	if err := json.Unmarshal(data, &mk); err != nil {
		return nil, m.storeFileFailed(err, "parsing %s", marketplacesFileName)
	}
	if mk == nil {
		mk = Marketplaces{}
	}
	return mk, nil
}

// saveMarketplaces is the shared boundary every caller (ensureFetched,
// AddMarketplace, RefreshMarketplace, RemoveMarketplace, EditMarketplace,
// saveRename, SeedDefaultMarketplaces) writes known_marketplaces.json
// through, so scrubbing here once - atomicWriteFile's own error can name
// this machine's absolute plugin-store path directly - covers every present
// and future caller instead of relying on each one to wrap it. Callers that
// need the marketplace name in the message (saveFailed/
// storeChangeRollbackFailed) wrap this already-scrubbed error with it; no
// path can reach the wire either way.
func (m *Manager) saveMarketplaces(mk Marketplaces) error {
	path, err := m.storePath(marketplacesFileName)
	if err != nil {
		return err
	}
	body, err := marketplaceMarshalIndent(mk, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling marketplaces: %w", err)
	}
	if err := marketplaceAtomicWriteFile(path, append(body, '\n'), 0o644); err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: saving %s failed: %v\n", marketplacesFileName, err)
		return fmt.Errorf("saving %s failed; see the hub's log for detail", marketplacesFileName)
	}
	m.markStoreChanged(StoreChanged{Marketplaces: true})
	return nil
}

// fetchMarketplaceContainer clones/references src into destDir and returns the
// directory that contains .claude-plugin/marketplace.json.
func (m *Manager) fetchMarketplaceContainer(ctx context.Context, src Source, destDir string) (string, error) {
	switch src.Kind {
	case SourceDirectory:
		return src.Path, nil // referenced in place
	case SourceGitHub:
		url := "https://github.com/" + src.Repo + ".git"
		if err := marketplaceGitClone(ctx, url, destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return destDir, nil
	case SourceURL:
		if err := marketplaceGitClone(ctx, src.URL, destDir, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return destDir, nil
	case SourceGitSubdir:
		if err := marketplaceGitSparseClone(ctx, src.URL, destDir, src.Path, src.Ref, src.Sha); err != nil {
			return "", err
		}
		return filepath.Join(destDir, src.Path), nil
	default:
		return "", &pathFreeError{fmt.Errorf("%w %q", ErrMarketplaceSourceUnsupported, src.Kind)}
	}
}

// ensureFetched clones a registered-but-unfetched marketplace (empty
// InstallLocation) into its store dir and backfills InstallLocation. A directory
// source is always "fetched" (referenced in place). Safe to call repeatedly.
//
// The caller must already hold m.lockPath() — as Browse and catalogPlugin's
// callers (Install/Upgrade) do — so this does not lock internally. flock(2)
// locks are per open-file-description, not per-process, so a second internal
// acquireLock here would self-deadlock (spin until its own 30s timeout) when
// reached from Install/Upgrade, which already hold that same lock.
func (m *Manager) ensureFetched(ctx context.Context, name string) (MarketplaceRef, error) {
	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, err
	}
	ref, ok := mk[name]
	if !ok {
		return MarketplaceRef{}, fmt.Errorf("marketplace %q: %w", name, ErrMarketplaceNotFound)
	}
	if ref.InstallLocation != "" {
		return ref, nil
	}
	installLoc := ref.Source.Path // directory source: referenced in place
	if ref.Source.Kind != SourceDirectory {
		installLoc = m.marketplaceDir(name)
		_ = marketplaceRemoveAll(installLoc)
		if _, err := m.fetchMarketplaceContainer(ctx, ref.Source, installLoc); err != nil {
			// The fetch clears this directory before cloning into it, so a
			// failure leaves a partial clone under the marketplace's name
			// while the entry records no location. Nothing else removes that
			// directory, so the failed fetch does — as AddMarketplace does
			// with its staging directory.
			_ = marketplaceRemoveAll(installLoc)
			return MarketplaceRef{}, err
		}
	}
	ref.InstallLocation = installLoc
	ref.LastUpdated = m.now().UTC()
	mk[name] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		return MarketplaceRef{}, m.saveFailed(name, marketplacesFileName, err)
	}
	return ref, nil
}

// AddMarketplace fetches src, reads its marketplace.json for the name (unless
// name is given), and records it. Returns the stored ref.
//
// There is no refusal for an already-registered name, so calling it again with
// the same name and a different source re-sources that marketplace in place; a
// failed save leaves the previously recorded clone as it was.
func (m *Manager) AddMarketplace(ctx context.Context, name string, src Source) (MarketplaceRef, error) {
	release, err := m.lockStore(ctx, marketplaceAcquireLock, 30*time.Second)
	if err != nil {
		return MarketplaceRef{}, err
	}
	defer release()

	if src.Kind == SourceDirectory {
		// Before the staging clear below, which would destroy a source that
		// sits there — and before the fetch, which is what would leave this
		// marketplace registered against the store's own doomed files. The
		// name this would be recorded under is still unknown here, and the
		// rule does not need it.
		if err := m.refuseSourceInStore(src.Path); err != nil {
			return MarketplaceRef{}, err
		}
	}

	// The store lock is held throughout, so the file cannot change between
	// here and the save.
	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, err
	}

	// Fetch into a staging dir first so a bad marketplace never half-registers.
	staging := m.marketplaceDir(stagingCloneName)
	_ = marketplaceRemoveAll(staging)
	root, err := m.fetchMarketplaceContainer(ctx, src, staging)
	if err != nil {
		_ = marketplaceRemoveAll(staging)
		return MarketplaceRef{}, err
	}
	cat, err := ParseCatalog(root)
	if err != nil {
		_ = marketplaceRemoveAll(staging)
		return MarketplaceRef{}, fmt.Errorf("reading marketplace.json: %w", err)
	}
	if name == "" {
		name = cat.Name
	}
	if name == "" {
		_ = marketplaceRemoveAll(staging)
		return MarketplaceRef{}, errors.New("marketplace has no name and none was given")
	}
	if err := validNameComponent("marketplace", name); err != nil {
		_ = marketplaceRemoveAll(staging)
		return MarketplaceRef{}, err
	}

	installLoc := src.Path // directory source: in place
	aside := ""
	if src.Kind != SourceDirectory {
		installLoc = m.marketplaceDir(name)
		old, err := m.swapInClone(staging, installLoc)
		if err != nil {
			_ = marketplaceRemoveAll(staging)
			return MarketplaceRef{}, err
		}
		aside = old
	} else {
		_ = marketplaceRemoveAll(staging)
	}

	ref := MarketplaceRef{Source: src, InstallLocation: installLoc, LastUpdated: m.now().UTC()}
	mk[name] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		if src.Kind != SourceDirectory {
			if rollbackErr := undoCloneSwap(installLoc, aside); rollbackErr != nil {
				return MarketplaceRef{}, m.storeChangeRollbackFailed(name, err, rollbackErr)
			}
		}
		return MarketplaceRef{}, m.saveFailed(name, marketplacesFileName, err)
	}
	if aside != "" {
		_ = marketplaceRemoveAll(aside)
	}
	return ref, nil
}

// saveFailed scrubs a save failure's absolute plugin-store path -
// atomicWriteFile's own error text names its destination and temp file
// directly - before it reaches the RPC caller, logging the raw error
// server-side first. fileName is the store file saveErr's own caller was
// writing, not assumed: saveRename can fail saving either
// known_marketplaces.json or installed_plugins.json, and the wire error
// needs to name whichever one it was. saveErr wrapping errStoreBetweenNames
// (saveRename's own rollback-also-failed case) keeps that identity through
// %w - the sentinel's own text carries no path - so a caller can still
// errors.Is against it.
func (m *Manager) saveFailed(name, fileName string, saveErr error) error {
	_, _ = fmt.Fprintf(m.stderr(), "warning: saving marketplace %q failed: %v\n", name, saveErr)
	if errors.Is(saveErr, errStoreBetweenNames) {
		return &pathFreeError{fmt.Errorf("marketplace %q: saving %s failed; see the hub's log for detail: %w", name, fileName, errStoreBetweenNames)}
	}
	return &pathFreeError{fmt.Errorf("marketplace %q: saving %s failed; see the hub's log for detail", name, fileName)}
}

// storeChangeRollbackFailed reports that an operation on marketplace name
// failed (cause) and the rollback that tried to undo it also failed
// (rollbackErr), leaving the store changed rather than back as it was found
// - the shape AddMarketplace and EditMarketplace's own fail closure both hit.
// rollbackErr is nil when the failed rollback is inside cause: a move helper
// that could not put every directory back marks the state with
// errRenameRollbackIncomplete and joins its own undo error, which names the
// paths, before handing the error up.
// Both cause's and rollbackErr's own text can carry this
// machine's absolute plugin-store path (atomicWriteFile, os.RemoveAll and
// os.Rename all name it directly), so both go to the hub's log instead of
// the RPC caller. The one error a caller can still act on - cause wrapping
// errStoreBetweenNames (saveRename's own between-names failure, whose
// directory rollback then also failed) or errRenameRollbackIncomplete (a move
// helper that left the store between the two names) - keeps that identity
// through %w, because each sentinel's own text carries no path.
func (m *Manager) storeChangeRollbackFailed(name string, cause, rollbackErr error) error {
	if rollbackErr == nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q: %v\n", name, cause)
	} else {
		_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q: %v; rolling back failed too: %v\n", name, cause, rollbackErr)
	}
	for _, sentinel := range []error{errStoreBetweenNames, errRenameRollbackIncomplete} {
		if errors.Is(cause, sentinel) {
			return fmt.Errorf("marketplace %q's change could not be rolled back; see the hub's log for detail: %w", name, sentinel)
		}
	}
	return fmt.Errorf("marketplace %q's change could not be rolled back; see the hub's log for detail", name)
}

// editFailed scrubs a failed edit step's absolute plugin-store path before it
// reaches the RPC caller, logging the raw error server-side first. name is the
// marketplace the caller learns instead: every directory the step named - a
// rename or remove the filesystem refused, a directory source in the way of a
// move, a path the store could not even read - is for the hub's log, not the
// wire. The identity a caller can still act on survives: a cancellation or
// deadline, and the refusals marketplaceRefusalToWire classifies, are
// re-attached path-free rather than lost with err's text.
func (m *Manager) editFailed(name string, err error) error {
	_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q: %v\n", name, err)
	// A helper that already built a path-free, name-bearing error - saveFailed
	// names the store file that failed - is what the caller needs, not the
	// generic message. The fail closure returns it unchanged for the same
	// reason; the call sites before the closure exist (the lock, which runs
	// the migration, and the store reads) have to honour it here.
	if alreadyPathFree(err) {
		return err
	}
	if cause := editFailureIdentity(err); cause != nil {
		return fmt.Errorf("marketplace %q: the edit failed; see the hub's log for detail: %w", name, cause)
	}
	return fmt.Errorf("marketplace %q: the edit failed; see the hub's log for detail", name)
}

// editFailureIdentity returns the sentinels err wraps that a caller still has
// to be able to act on, joined, carrying none of err's own text - which can
// name an absolute plugin-store path. errors.Is keeps working for a
// cancellation, a deadline, and every refusal the wire layer classifies; the
// path itself stays in the hub's log.
func editFailureIdentity(err error) error {
	var found []error
	for _, sentinel := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		ErrMarketplaceSourceInStore,
		ErrMarketplaceExists,
		ErrMarketplaceNotFound,
		ErrInvalidName,
		errStoreBetweenNames,
		errRenameRollbackIncomplete,
		errStoreRootUnset,
		errStoreRootNotAbsolute,
		errLockContention,
	} {
		if errors.Is(err, sentinel) {
			found = append(found, sentinel)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return errors.Join(found...)
}

// migrationFailed scrubs a failed migration's absolute plugin-store paths - the
// marker an earlier run left, a store file a recovery could not read, and the
// directories a rollback could not put back - before the error reaches an RPC
// caller, logging the raw error server-side first. ListMarketplaces and
// RefreshMarketplace reach the migration through the store lock, so the scrub
// cannot live only in EditMarketplace's fail closure. from and to are the two
// names the caller learns instead; a helper that already built a path-free,
// name-bearing error passes through, and the sentinels editFailureIdentity
// preserves still say what state the store is in.
func (m *Manager) migrationFailed(from, to string, err error) error {
	_, _ = fmt.Fprintf(m.stderr(), "warning: migrating marketplace %q to %q: %v\n", from, to, err)
	if alreadyPathFree(err) {
		return err
	}
	base := fmt.Sprintf("marketplace %q could not be migrated to %q; see the hub's log for detail", from, to)
	if cause := editFailureIdentity(err); cause != nil {
		return &pathFreeError{fmt.Errorf("%s: %w", base, cause)}
	}
	return &pathFreeError{errors.New(base)}
}

// migrationFailedErr scrubs the migration failure a lock holder reaches before
// it hands the error to its own caller. A run recovers an earlier one's marker
// and reads the migration record before any marketplace is named, so those
// failures reach the wire without ever passing migrationFailed; every path they
// can name - the marker, the record, a store file - goes to the hub's log
// instead, and a result migrationFailed already scrubbed passes through.
func (m *Manager) migrationFailedErr(err error) error {
	_, _ = fmt.Fprintf(m.stderr(), "warning: migrating the plugin store's marketplace names: %v\n", err)
	if alreadyPathFree(err) {
		return err
	}
	base := "the plugin store's marketplace names could not be migrated; see the hub's log for detail"
	if cause := editFailureIdentity(err); cause != nil {
		return &pathFreeError{fmt.Errorf("%s: %w", base, cause)}
	}
	return &pathFreeError{errors.New(base)}
}

// storeFileFailed logs a store file's raw read, parse or write failure
// server-side and returns a path-free error naming the file, which is the part
// a caller can act on; the absolute path it was read or written at is the hub's
// log's. The result is marked path-free, so a caller that adds a marketplace's
// name around it keeps both.
func (m *Manager) storeFileFailed(err error, format string, args ...any) error {
	what := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(m.stderr(), "warning: %s: %v\n", what, err)
	return &pathFreeError{fmt.Errorf("%s; see the hub's log for detail", what)}
}

// pathFreeError marks an error whose text is already safe to hand an RPC
// caller: saveFailed builds one, naming the store file that failed and never
// the absolute path it was written at, and fetchMarketplaceContainer builds one
// for a source kind the caller sent and this build does not accept. editFailed
// returns it unchanged rather than scrubbing the detail the caller needs.
type pathFreeError struct{ err error }

func (e *pathFreeError) Error() string { return e.err.Error() }
func (e *pathFreeError) Unwrap() error { return e.err }

// pathFreeErrorType is pathFreeError's type, for the structural check below.
var pathFreeErrorType = reflect.TypeFor[*pathFreeError]()

// alreadyPathFree reports whether err was built path-free by a helper that knew
// the marketplace name, so a scrub would only drop the detail it carries. It
// walks only single-cause wrappers - which add context like the marketplace's
// name, never a path - and stops at the marker. errors.Unwrap returns nil for
// an errors.Join (and for a leaf), so a composite is never walked past: the
// migration joins saveFailed's path-free error with its rollback and marker
// failures, whose own text names directories under the store root, and passing
// that composite through would leak them. Nothing here compares text, so a
// root's spelling or a symlinked path cannot change the answer.
func alreadyPathFree(err error) bool {
	for err != nil {
		if reflect.TypeOf(err) == pathFreeErrorType {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}

// ListMarketplaces returns every registered marketplace, read from behind the
// migration barrier (loadMigratedMarketplaces) so that the names it hands back
// are the ones the store accepts today.
func (m *Manager) ListMarketplaces(ctx context.Context) (Marketplaces, error) {
	return m.loadMigratedMarketplaces(ctx, marketplaceAcquireLock)
}

// cloneRemovalFailed reports that removing marketplace name's clone from disk
// failed as a cleanup step whose own metadata change already applied - not a
// write failure itself, since RemoveMarketplace has already saved the
// unregistration by the time this runs. The error wraps
// ErrMarketplaceUnregisteredCloneRemains, so a caller can tell this
// applied-with-litter outcome from a plain refusal by errors.Is instead of
// assuming a non-nil error means the marketplace is still registered.
// removeErr's own text can carry this machine's absolute plugin-store path
// (os.RemoveAll returns a *fs.PathError that names it), so it goes to the
// hub's log instead of the RPC caller.
func (m *Manager) cloneRemovalFailed(name string, removeErr error) error {
	_, _ = fmt.Fprintf(m.stderr(), "warning: removing marketplace %q's clone failed: %v\n", name, removeErr)
	return fmt.Errorf("marketplace %q: %w; see the hub's log for detail", name, ErrMarketplaceUnregisteredCloneRemains)
}

// RemoveMarketplace unregisters name: the metadata save lands first, so a
// save failure is a plain refusal that leaves the marketplace registered and
// its clone untouched. Only once that save has landed does the clone's own
// removal run - a failure there is litter the hub's caller cannot undo
// (reported as ErrMarketplaceUnregisteredCloneRemains), but the marketplace
// itself is already gone from the listing. A retry after that litter finds
// no entry for name and reports the plain ErrMarketplaceNotFound a lookup
// miss always has: name is caller-controlled and unvalidated here, so
// deriving m.marketplaceDir(name) and touching the filesystem on a miss -
// name "" resolves to the marketplaces directory itself, ".." to its parent
// - is refused rather than attempted. Whoever wants the litter cleaned up
// retries some other way; this never mutates the filesystem on a miss.
func (m *Manager) RemoveMarketplace(ctx context.Context, name string) error {
	release, err := m.lockStore(ctx, marketplaceAcquireLock, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	mk, err := m.loadMarketplaces()
	if err != nil {
		return err
	}
	_, ok := mk[name]
	if !ok {
		return fmt.Errorf("marketplace %q: %w", name, ErrMarketplaceNotFound)
	}
	// Decide whether the clone is safe to sweep from the pre-removal registry.
	// This includes the removed record's directory source: legacy data may use
	// the canonical clone path as live source data, even though new writes refuse
	// sources inside the store.
	clone := m.marketplaceDir(name)
	present, protect := m.sweepDestroysSource(marketplaceProtectionPaths(mk), clone)
	delete(mk, name)
	if err := m.saveMarketplaces(mk); err != nil {
		return m.saveFailed(name, marketplacesFileName, err)
	}
	if present && !protect {
		if err := marketplaceRemoveAll(clone); err != nil {
			return m.cloneRemovalFailed(name, err)
		}
	}
	return nil
}

// sweepDestroysSource reports whether removing (or renaming away) the directory
// at clone would delete or break any path in sources — a registered
// marketplace's directory source or recorded install location, or an edit's
// incoming source. The store refuses such a source inside itself today, but
// refuseSourceInStore is enforced only by AddMarketplace and EditMarketplace,
// and the name migration neither re-checks it nor rewrites Source.Path, so a
// record predating that rule (or seeded by hand) survives every later write; a
// sweep must not be what finally deletes a live source.
//
// The model is what the sweep actually deletes: the tree at the clone path. A
// source below clone goes with it — even through a symlink, since removing the
// clone deletes the link — so the source is compared against the clone both as
// recorded and after symlinks are resolved. The clone's ancestors are resolved
// because the store root or its marketplaces directory can be a symlink and a
// legacy record can name the physical path, but a final symlink at the clone
// itself is not followed: RemoveAll removes the link, not its target. An
// ancestor of clone is not deleted by RemoveAll and must not be protected, or
// its stale clone would survive and hold the name.
//
// It reports the entry's presence as well as whether a source overlays it, and
// every inspection failure degrades to protecting the source (a warning, never
// an error): a path the filesystem will not consider — an over-long legacy name
// — counts as absent, so the caller must skip the sweep rather than call
// RemoveAll on a path it would only fail on.
func (m *Manager) sweepDestroysSource(sources []string, clone string) (present, protect bool) {
	present, err := pathPresentNoFollow(clone)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: checking marketplace clone %s: %v\n", clone, err)
		return false, true
	}
	if !present {
		return false, false
	}
	absClone, err := filepath.Abs(clone)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: resolving marketplace clone %s: %v\n", clone, err)
		return true, true
	}
	resolvedClone, err := resolveAncestors(clone)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: resolving marketplace clone %s: %v\n", clone, err)
		return true, true
	}
	underClone := func(path string) bool {
		return pathWithinDir(absClone, path) || pathWithinDir(resolvedClone, path)
	}
	for _, source := range sources {
		touches, err := sourceTouchesClone(source, underClone, 0)
		if err != nil {
			// A source the walk cannot inspect is one the sweep might still be
			// the thing that deletes: protect it rather than run the sweep blind.
			_, _ = fmt.Fprintf(m.stderr(), "warning: checking directory source %s: %v\n", source, err)
			return true, true
		}
		if touches {
			return true, true
		}
	}
	return true, false
}

// marketplaceProtectionPaths names the directory sources an operation must not
// delete or move out from under a marketplace. Records named in exclude — whose
// own paths the operation is intentionally relocating or replacing — are left
// out, so a record cannot protect the very directory it is having changed.
//
// Only Source.Path is taken, not InstallLocation: the name migration represents
// one marketplace under several alias names, and each alias carries the same
// install location, so protecting them would refuse the merge the migration
// exists to perform (TestMarketplaceNameMigration_MarksTheMergeItMakes and
// five siblings). A directory source is the only recorded path a supported
// operation never shares between names.
func marketplaceProtectionPaths(mk Marketplaces, exclude ...string) []string {
	skip := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		skip[name] = true
	}
	var out []string
	for name, ref := range mk {
		if skip[name] {
			continue
		}
		if ref.Source.Kind == SourceDirectory && ref.Source.Path != "" {
			out = append(out, ref.Source.Path)
		}
	}
	return out
}

// resolveAncestors canonicalizes every component of path except the last, so a
// symlinked store root or marketplaces directory is followed but a final
// symlink is not — RemoveAll and rename act on the link itself.
func resolveAncestors(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	dir, base := filepath.Split(abs)
	if dir == "" {
		return abs, nil
	}
	resolved, err := resolveForContainment(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, base), nil
}

// maxSymlinkHops bounds the source-link walk so a symlink cycle cannot spin.
const maxSymlinkHops = 64

// sourceTouchesClone reports whether deleting the tree at the clone would delete
// or break the recorded source: whether any location the source's path passes
// through sits at or beneath the clone. It walks the path as written —
// resolving `.`/`..` in traversal order and following symlinks at each
// component — because canonicalizing first would erase a `..` (or a link
// target's `..`) that needs the clone to exist, and a full EvalSymlinks would
// hide a hop that dips into the clone and back out.
func sourceTouchesClone(source string, underClone func(string) bool, depth int) (bool, error) {
	if depth > maxSymlinkHops {
		// Deeper than any sane chain: assume the sweep holds a link the source
		// needs and protect.
		return true, nil
	}
	raw, err := absoluteUncleaned(source)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", source, err)
	}
	root := filepath.VolumeName(raw) + string(filepath.Separator)
	current := root
	for comp := range strings.SplitSeq(strings.TrimPrefix(raw, root), string(filepath.Separator)) {
		switch comp {
		case "", ".":
			continue
		case "..":
			// Applied to the location the path has actually reached, so a
			// `..` after a link into the clone walks back out of the clone.
			current = filepath.Dir(current)
			continue
		}
		next := filepath.Join(current, comp)
		if underClone(next) {
			return true, nil
		}
		info, err := marketplaceLstat(next)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || pathCannotExist(err) {
				// A component that is not there cannot be destroyed, and
				// nothing after it can resolve through it either.
				return false, nil
			}
			return false, fmt.Errorf("checking %s: %w", next, err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			current = next
			continue
		}
		target, err := os.Readlink(next)
		if err != nil {
			return false, fmt.Errorf("reading link %s: %w", next, err)
		}
		if !filepath.IsAbs(target) {
			// Kept uncleaned: the target's own `.`/`..` components matter.
			target = filepath.Dir(next) + string(filepath.Separator) + target
		}
		// The link itself, and the path it names, both go if the clone holds
		// them — the final target alone would miss a hop back out of the clone.
		if underClone(next) || underClone(target) {
			return true, nil
		}
		// The target is a path in its own right: its own links and `..`
		// components can lead through the clone even where this path's do not.
		touches, err := sourceTouchesClone(target, underClone, depth+1)
		if err != nil {
			return false, err
		}
		if touches {
			return true, nil
		}
		// Continue with the directory the link resolves to, so components after
		// it apply to the location they actually see.
		resolved, err := filepath.EvalSymlinks(next)
		if err != nil {
			return false, nil
		}
		current = resolved
	}
	return false, nil
}

// absoluteUncleaned makes path absolute without canonicalizing it, so a `.` or
// `..` component survives for the traversal walk.
func absoluteUncleaned(path string) (string, error) {
	// A path may be written with the alternate separator (on Windows, `/` in a
	// `C:/...` path); normalize it without canonicalizing `.`/`..`, which must
	// survive for the walk.
	path = filepath.FromSlash(path)
	if filepath.IsAbs(path) {
		return path, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return wd + string(filepath.Separator) + path, nil
}

// sourceTouchesPath reports whether a directory source at source depends on the
// directory at path — it sits at, beneath, or resolves through it — so that
// moving or removing that directory would break the source.
func (m *Manager) sourceTouchesPath(source, path string) (bool, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, fmt.Errorf("resolving %s: %w", path, err)
	}
	resolvedPath, err := resolveAncestors(path)
	if err != nil {
		return false, err
	}
	under := func(p string) bool {
		return pathWithinDir(absPath, p) || pathWithinDir(resolvedPath, p)
	}
	return sourceTouchesClone(source, under, 0)
}

// EditMarketplace renames a registered marketplace and/or replaces its
// source (spec 2026-09-07 §3). The order is chosen so the one step that can
// take a long time or fail for reasons outside the store — fetching the new
// source — happens before anything on disk moves, and a failure at any later
// step, the marketplaces file's own save included, puts back everything the
// edit moved: the renamed directories, the re-keyed registry file, and the
// contents of a clone the new source was swapped into — the swap sets the old
// contents aside and they stay there until both files are saved. Restoring
// only the directory's NAME would leave the new source's files wearing the old
// one, which every later refresh would then serve under the old source's name,
// because RefreshMarketplace pulls the clone's own origin.
//
//  1. fetch a changed source into staging and parse its catalog (Add's own
//     staging discipline: a bad source never half-registers);
//  2. rename the clone directory and the plugin cache directory, and re-key
//     every <plugin>@old registry entry (its install path lives under the
//     renamed cache);
//  3. swap the staged clone into the (possibly renamed) install location, or
//     point a directory source at its path;
//  4. save the installed registry, then the marketplaces file.
//
// A same-name, same-source call is a no-op that returns the current ref.
// A git-backed marketplace's plugins are materialized under the cache; a
// directory-source marketplace's relative plugins are referenced in place
// inside it. A re-source moves neither, beyond the re-key a rename implies.
func (m *Manager) EditMarketplace(ctx context.Context, name, newName string, src *Source) (MarketplaceRef, error) {
	release, err := m.lockStore(ctx, marketplaceAcquireLock, 30*time.Second)
	if err != nil {
		// Taking the store lock migrates the names first, and a read failure
		// there names the absolute store file it could not parse.
		return MarketplaceRef{}, m.editFailed(name, err)
	}
	defer release()

	mk, err := m.loadMarketplaces()
	if err != nil {
		return MarketplaceRef{}, m.editFailed(name, err)
	}
	ref, ok := mk[name]
	if !ok {
		return MarketplaceRef{}, fmt.Errorf("marketplace %q: %w", name, ErrMarketplaceNotFound)
	}
	renaming := newName != "" && newName != name
	resourcing := src != nil && *src != ref.Source
	if !renaming && !resourcing {
		return ref, nil
	}
	if renaming {
		if err := validNameComponent("marketplace", newName); err != nil {
			return MarketplaceRef{}, err
		}
		if _, taken := mk[newName]; taken {
			return MarketplaceRef{}, fmt.Errorf("marketplace %q: %w", newName, ErrMarketplaceExists)
		}
	}
	if resourcing {
		if src.Kind == SourceDirectory {
			// Before the staging directory is cleared, which is itself one of
			// the paths this refuses.
			if err := m.refuseSourceInStore(src.Path); err != nil {
				return MarketplaceRef{}, m.editFailed(name, err)
			}
			if renaming {
				// The rename moves the clone and the plugin cache, so a source
				// that lives inside either moves too while Source.Path would
				// still name the old location — leaving the marketplace
				// recorded against a path that is gone. Refuse rather than save
				// that.
				for _, moved := range []struct{ what, path string }{
					{"clone", m.marketplaceDir(name)},
					{"plugin cache", filepath.Join(m.cacheDir(), name)},
				} {
					touches, err := m.sourceTouchesPath(src.Path, moved.path)
					if err != nil {
						return MarketplaceRef{}, m.editFailed(name, err)
					}
					if touches {
						return MarketplaceRef{}, fmt.Errorf("marketplace %q: a directory source inside its own %s cannot be combined with a rename to %q", name, moved.what, newName)
					}
				}
			}
		}
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return MarketplaceRef{}, m.editFailed(name, err)
	}
	if renaming {
		// With the other refusals, not beside the rename it guards: nothing
		// the fetch does can put a leftover under the new name, so a rename
		// this check is going to refuse need not pay for a clone first.
		if err := m.refuseLeftoversUnder(newName, reg); err != nil {
			return MarketplaceRef{}, err
		}
	}

	// 1. The network step, before anything on disk moves.
	staging := m.marketplaceDir(stagingCloneName)
	if resourcing {
		_ = marketplaceRemoveAll(staging)
		root, err := m.fetchMarketplaceContainer(ctx, *src, staging)
		if err != nil {
			_ = marketplaceRemoveAll(staging)
			// The failure can name the staging directory the fetch wrote
			// into, which is this machine's absolute plugin-store path, so it
			// is scrubbed here as every later step's failure is.
			return MarketplaceRef{}, m.editFailed(name, err)
		}
		if _, err := ParseCatalog(root); err != nil {
			_ = marketplaceRemoveAll(staging)
			return MarketplaceRef{}, m.editFailed(name, fmt.Errorf("reading marketplace.json: %w", err))
		}
	}
	// From here until the files are saved, a failure runs undo in reverse
	// and sweeps staging. Every step reports what it could not put back: a
	// rollback that fails leaves a directory under a name nothing records,
	// and the error the edit returns is the only place that can say which.
	var undo []func() error
	// swappedIn holds the install location once swapInClone has replaced its
	// contents with the new source's, and asideClone the directory the swap
	// renamed those old contents to. The old clone outlives the swap for
	// exactly this window, because a name alone cannot undo it: the new
	// source's files renamed back to the old name would be served under the old
	// source's name by every later refresh (RefreshMarketplace pulls the
	// clone's own origin). So a failure removes the swapped-in clone and
	// renames the old contents back into it; only once both files are saved is
	// the aside copy no longer a rollback source, and it goes with staging.
	swappedIn, asideClone := "", ""
	undoSwap := func() error {
		if swappedIn == "" {
			return nil
		}
		return undoCloneSwap(swappedIn, asideClone)
	}
	fail := func(err error) (MarketplaceRef, error) {
		// Before undo, which moves the install location back under its old
		// name: the contents have to be in it first.
		swapErr, undoErr := undoSwap(), runUndo(undo)
		_ = marketplaceRemoveAll(staging)
		rollbackErr := errors.Join(swapErr, undoErr)
		// A rollback that could not put every directory back leaves the store
		// changed rather than back as it was found. That can be this call's
		// own rollback (swapErr/undoErr) or the move helper's, which ran
		// before it handed the error up and marked the state with
		// errRenameRollbackIncomplete. Either way err and rollbackErr can
		// carry this machine's absolute plugin-store path, so
		// storeChangeRollbackFailed logs them and the caller learns only
		// which marketplace was left half-edited.
		if rollbackErr != nil || errors.Is(err, errRenameRollbackIncomplete) {
			return MarketplaceRef{}, m.storeChangeRollbackFailed(name, err, rollbackErr)
		}
		// The store is back as it was found, so there is no state to report -
		// only the failure, which can still name an absolute plugin-store
		// path: a rename or remove the filesystem refused, a directory source
		// in the way of a move, a path the store could not read. Those go to
		// the hub's log and the caller learns only which marketplace's edit
		// failed, so no branch of this closure can leak one. A helper that
		// already built a path-free error (saveFailed, naming the store file)
		// passes through with its detail intact.
		if alreadyPathFree(err) {
			return MarketplaceRef{}, err
		}
		return MarketplaceRef{}, m.editFailed(name, err)
	}

	// 2. Rename on disk and in the registry. The registry as the edit found it
	// is kept because the re-key below replaces it wholesale and a later
	// failure has to write it back.
	target := name
	registryAsFound := reg
	if renaming {
		target = newName
		ref, reg, undo, err = m.moveMarketplace(name, newName, ref, mk, reg, registryKeyOwners(reg, mk), resourcing)
		if err != nil {
			return fail(err)
		}
	}

	// 3. Apply the new source into the install location. An old clone that a
	// directory source makes redundant goes only after the files say so.
	var afterSave []func()
	if resourcing {
		if src.Kind == SourceDirectory {
			if ref.Source.Kind != SourceDirectory {
				clone := m.marketplaceDir(target)
				// The old clone is redundant only where it is the marketplace's
				// own. Another record's source or install location that names
				// it, and the incoming source itself (a source can be a symlink
				// inside the clone), must all survive; the record being edited
				// is excluded, because its own old clone is what is redundant.
				sources := append(marketplaceProtectionPaths(mk, name), src.Path)
				present, protect := m.sweepDestroysSource(sources, clone)
				if present && !protect {
					afterSave = append(afterSave, func() { _ = marketplaceRemoveAll(clone) })
				}
			}
			_ = marketplaceRemoveAll(staging)
			ref.InstallLocation = src.Path
		} else {
			dest := m.marketplaceDir(target)
			// A swap replaces dest, deleting what was there when the aside copy
			// goes. If another record's source or install location names dest,
			// that data would go with it, so refuse the re-source rather than
			// overwrite it. The record being edited is excluded: re-sourcing it
			// is exactly what replaces its own old source.
			if _, protect := m.sweepDestroysSource(marketplaceProtectionPaths(mk, name), dest); protect {
				return fail(fmt.Errorf("marketplace %q: the new install location %s holds a directory source another marketplace records", name, dest))
			}
			aside, err := m.swapInClone(staging, dest)
			if err != nil {
				return fail(err)
			}
			swappedIn, asideClone = dest, aside
			if aside != "" {
				afterSave = append(afterSave, func() { _ = marketplaceRemoveAll(aside) })
			}
			ref.InstallLocation = dest
		}
		ref.Source = *src
		// LastUpdated tracks how fresh the catalog on disk is, so only the
		// branch that fetched one moves it; a rename moves no content.
		ref.LastUpdated = m.now().UTC()
	}

	// 4. Save both files. A failed save is what the undo above is for: the
	// file that survives it is the old one, and directories left under the
	// new name would be orphaned by the next refresh, which reclones the
	// recorded source at the recorded path.
	if renaming {
		// saveRename already returns a scrubbed error, naming whichever file
		// (known_marketplaces.json or installed_plugins.json) actually
		// failed, before fail decides whether the outer rollback also needs
		// reporting.
		if err := m.saveRename(mk, name, newName, ref, reg, registryAsFound); err != nil {
			return fail(err)
		}
	} else {
		mk[name] = ref
		if err := m.saveMarketplaces(mk); err != nil {
			return fail(m.saveFailed(name, marketplacesFileName, err))
		}
	}
	for _, fn := range afterSave {
		fn()
	}
	return ref, nil
}

// restoreRename puts one of an edit's renames back, naming both paths when it
// cannot: the directory is then left under a name no store file records, and
// nothing but this error says where it is.
func restoreRename(what, from, to string) error {
	if err := marketplaceRename(from, to); err != nil {
		return fmt.Errorf("restoring %s %s to %s: %w", what, from, to, err)
	}
	return nil
}

// runUndo runs a rename's undo steps in reverse and joins what they could not
// put back.
func runUndo(undo []func() error) error {
	var errs []error
	for _, fn := range slices.Backward(undo) {
		errs = append(errs, fn())
	}
	return errors.Join(errs...)
}

// moveMarketplace renames a marketplace on disk and in the registry: the clone
// directory, when one is there — moved for a non-directory source, swept for a
// directory source, which never lives there; the plugin cache directory, when
// one is there; and every <plugin>@name registry entry, whose install path
// follows the cache. Neither store file is written. On success it returns the
// ref and registry as they are to be recorded, and the steps that put the
// directories back should a later step fail; a failure puts back what it had
// moved itself and reports what it could not.
func (m *Manager) moveMarketplace(name, newName string, ref MarketplaceRef, mk Marketplaces, reg Registry, owners map[string]string, resourcing bool) (MarketplaceRef, Registry, []func() error, error) {
	var undo []func() error
	fail := func(err error) (MarketplaceRef, Registry, []func() error, error) {
		if undoErr := runUndo(undo); undoErr != nil {
			return MarketplaceRef{}, Registry{}, nil, errors.Join(err, undoErr, errRenameRollbackIncomplete)
		}
		return MarketplaceRef{}, Registry{}, nil, err
	}
	oldDir, newDir := m.marketplaceDir(name), m.marketplaceDir(newName)
	// When the edit replaces the record's own source, that source is no longer
	// data to protect — the re-source is what removes it. Every other record's
	// directory source always is, and a pure rename keeps the edited record's
	// too, because the rename would move its directory out from under it.
	registeredSources := func() []string {
		if resourcing {
			return marketplaceProtectionPaths(mk, name)
		}
		return marketplaceProtectionPaths(mk)
	}
	if ref.Source.Kind != SourceDirectory {
		// The clone move follows a final symlink: pathPresent stats through it,
		// so the move refuses rather than blindly renaming an entry it cannot
		// read (TestEditMarketplace_TreatsOnlyAMissingPathAsAbsent).
		haveClone, err := pathPresent(oldDir)
		if err != nil {
			return fail(err)
		}
		// Whatever the entry records: a lazy fetch clears and refills this
		// directory before it writes an install location, so a fetch killed
		// mid-clone leaves one behind that only the move takes with the name.
		if haveClone {
			// The move takes the directory with the name. If another record's
			// source or install location names it, that move would strip the
			// data out from under a live record — as a hand-seeded or
			// pre-refuseSourceInStore store can arrange — so refuse instead.
			// The record being renamed is excluded: its own clone is what moves.
			_, displaced := m.sweepDestroysSource(marketplaceProtectionPaths(mk, name), oldDir)
			if displaced {
				return fail(fmt.Errorf("renaming marketplace clone %s would move a directory source another marketplace records", oldDir))
			}
			if err := marketplaceRename(oldDir, newDir); err != nil {
				return fail(fmt.Errorf("renaming marketplace clone: %w", err))
			}
			undo = append(undo, func() error { return restoreRename("marketplace clone", newDir, oldDir) })
		}
		// A recorded location says where the clone is, so it follows the
		// clone that moved; with none to move the entry is unfetched, and the
		// next fetch clears and clones under the new name as it would have
		// under the old. An entry that recorded no location keeps none,
		// whatever a failed fetch left at the canonical path.
		if ref.InstallLocation != "" {
			ref.InstallLocation = ""
			if haveClone {
				ref.InstallLocation = newDir
			}
		}
	} else {
		// A directory source never lives under the clone directory, so one
		// there is residue — the clone a git->directory re-source failed to
		// remove. Sweep it rather than moving it: moving it would park the
		// same unrecorded directory under the new name, which is still a name
		// nothing records it under. Nothing references it, so there is nothing
		// to put back and no undo step to add — unless some record's directory
		// source lives at or beneath it, which keeps the sweep from deleting a
		// live source. Presence is the entry's own, not a stat through a final
		// symlink: sweeping is what removes it either way, so an unreadable
		// link must still be cleared rather than block a directory rename.
		// The sweep here is destructive and runs before either store file is
		// saved, so it must keep every current source — the edited record's
		// included. A source beneath the old clone is then left in place rather
		// than deleted ahead of a commit that can still fail.
		present, protect := m.sweepDestroysSource(marketplaceProtectionPaths(mk), oldDir)
		if present && !protect {
			if err := marketplaceRemoveAll(oldDir); err != nil {
				return fail(fmt.Errorf("removing stale marketplace clone %s: %w", oldDir, err))
			}
		}
	}
	oldCache, newCache := filepath.Join(m.cacheDir(), name), filepath.Join(m.cacheDir(), newName)
	haveCache, err := pathPresent(oldCache)
	if err != nil {
		return fail(err)
	}
	if haveCache {
		// The cache move takes that directory with the name too. A source or
		// install location recorded inside it — a hand-seeded store can arrange
		// it, refuseSourceInStore only guards new operations — would be moved
		// out from under its record, so refuse rather than break it.
		if _, protect := m.sweepDestroysSource(registeredSources(), oldCache); protect {
			return fail(fmt.Errorf("renaming plugin cache %s would move a path a marketplace records", oldCache))
		}
		if err := marketplaceRename(oldCache, newCache); err != nil {
			return fail(fmt.Errorf("renaming plugin cache: %w", err))
		}
		undo = append(undo, func() error { return restoreRename("plugin cache", newCache, oldCache) })
	}
	return ref, rekeyRegistry(reg, owners, name, newName, oldCache, newCache), undo, nil
}

// errStoreBetweenNames marks the one rename failure that leaves the store
// between the old name and the new one rather than back at either: the
// marketplaces file records the old name while the registry keys the
// marketplace's plugins under the new one. Only saveRename can tell that
// half-state from the two it rolls back to, so a caller whose rename wrote a
// marker keeps it for that state (migrateMarketplaceName).
var errStoreBetweenNames = errors.New("the store is left between the two names")

// errRenameRollbackIncomplete marks a move helper's failure that could not put
// every directory back where it found it. A move writes neither store file, so
// a failure whose undo succeeded leaves the store at the old name with nothing
// left to resume; only one that could not leaves it between the two names. A
// rename that wrote a marker keeps it for this state alone
// (migrateMarketplaceName).
var errRenameRollbackIncomplete = errors.New("a failed move could not be put back completely")

// saveRename records a rename in both store files, the registry first: a
// marketplaces file naming a marketplace whose plugins are still keyed under
// the old name is the worse of the two half-states, and evener-doctor reports
// the other one — which now outlives a failed save only if the restore below
// fails too. When the marketplaces file's save fails, registryAsFound is
// written back, because the re-keyed entries name install paths under a cache
// directory the caller is about to rename back. That restore is itself a
// write that can fail, and only then is the store left inconsistent, so the
// error says so and carries errStoreBetweenNames, which is how a rename that
// wrote a marker knows the marker is still needed.
//
// The returned error is already scrubbed through saveFailed, naming whichever
// store file (known_marketplaces.json or installed_plugins.json) actually
// failed instead of the raw *fs.PathError, so no caller — EditMarketplace or
// a migration recovery reached from ListMarketplaces/List, which can surface
// its error over RPC just as directly — can forget to scrub before the
// absolute plugin-store path in the raw error reaches an RPC caller.
func (m *Manager) saveRename(mk Marketplaces, name, newName string, ref MarketplaceRef, reg, registryAsFound Registry) error {
	if err := m.saveRegistry(reg); err != nil {
		return m.saveFailed(name, registryFileName, err)
	}
	delete(mk, name)
	mk[newName] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		if restoreErr := m.saveRegistry(registryAsFound); restoreErr != nil {
			return m.saveFailed(name, marketplacesFileName, fmt.Errorf("saving %s failed (%w); restoring %s failed (%w), so %w: it still keys this marketplace's plugins under %q", marketplacesFileName, err, registryFileName, restoreErr, errStoreBetweenNames, newName))
		}
		return m.saveFailed(name, marketplacesFileName, err)
	}
	return nil
}

// refuseLeftoversUnder rejects a rename onto a name that a removed
// marketplace's residue still occupies. Removing a marketplace drops its clone
// and its registration but never its plugin cache or its registry entries, and
// a failed edit can strand a clone, so all three can outlive the marketplace
// that made it. Renaming onto any of them would bury it under a live
// marketplace's name — the directories silently, the registry entries as ghost
// installs that shadow the ones being re-keyed — so each is named and refused
// before anything moves. This runs on every rename, not only when the
// marketplace being renamed has something of its own to move: an install-free
// one moves neither a cache nor a registry entry and would otherwise sail past.
//
// Each refusal carries ErrMarketplaceExists, because that is what it is: the
// name is taken, just not by anything the marketplaces file records. Clearing
// the residue it names is the caller's move, not the store's failure.
func (m *Manager) refuseLeftoversUnder(newName string, reg Registry) error {
	// Where an os.Rename LinkError would have said only "file exists". The
	// residue is named by the name it blocks, not by path: the absolute
	// plugin-store directory is the hub's log's, and what the caller has to
	// clear is the leftover under the name it asked for.
	cache := filepath.Join(m.cacheDir(), newName)
	haveCache, err := pathPresent(cache)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: checking the plugin cache for %q: %v\n", newName, err)
		return fmt.Errorf("the plugin cache for %q could not be checked for leftovers; see the hub's log for detail", newName)
	}
	if haveCache {
		return fmt.Errorf("the plugin cache for %q already exists; a removed marketplace left it behind and it must be deleted before %q can be reused: %w", newName, newName, ErrMarketplaceExists)
	}
	clone := m.marketplaceDir(newName)
	haveClone, err := pathPresent(clone)
	if err != nil {
		_, _ = fmt.Fprintf(m.stderr(), "warning: checking the marketplace clone for %q: %v\n", newName, err)
		return fmt.Errorf("the marketplace clone for %q could not be checked for leftovers; see the hub's log for detail", newName)
	}
	if haveClone {
		return fmt.Errorf("the marketplace clone for %q already exists; a removed marketplace left it behind and it must be deleted before %q can be reused: %w", newName, newName, ErrMarketplaceExists)
	}
	// A recorded name never carries '@' once the store is migrated
	// (lockStore), so a key ending in "@<newName>" is keyed under newName
	// itself — never the renamed marketplace's own, which key under its old
	// name — and is residue. Mid-migration a name not yet renamed can still
	// share the suffix; the migration reads that as taken and moves on to the
	// next numbered name.
	suffix := "@" + newName
	for key := range reg.Plugins {
		if strings.HasSuffix(key, suffix) {
			return fmt.Errorf("registry entry %s already exists; a removed marketplace left it behind and it must be removed before %q can be reused: %w", key, newName, ErrMarketplaceExists)
		}
	}
	return nil
}

// registryKeyOwner names the marketplace a registry key belongs to: the longest
// recorded name the key ends in as "@<name>". A plugin's own '@' sits before
// that one, so wid@get@acme is wid@get in acme — but a key can end in more than
// one recorded name at once: "<plugin>@x@y@z" ends in "@y@z" and in "@z". The
// longest wins, so migrating "z" leaves a marketplace recorded as "x@y@z" its
// keys, which migrating it later would otherwise no longer find. Whether a name
// matched is answered apart from the name itself, because the empty name is a
// recorded name a key ends in: "widget@" is widget in the marketplace called
// "".
func registryKeyOwner(key string, mk Marketplaces) (string, bool) {
	best, found := "", false
	for name := range mk {
		if strings.HasSuffix(key, "@"+name) && (!found || len(name) > len(best)) {
			best, found = name, true
		}
	}
	return best, found
}

// registryKeyOwners names, for every key in the registry, the marketplace it
// belongs to as the store was found. Ownership is taken once, before any
// rename: a rename rewrites keys, and the names it writes are recorded, so
// recomputing ownership later would let a key rewritten for one marketplace be
// claimed by a name the migration itself had just made — which is not a name
// the key was ever keyed under.
func registryKeyOwners(reg Registry, mk Marketplaces) map[string]string {
	owners := make(map[string]string, len(reg.Plugins))
	for key := range reg.Plugins {
		if owner, ok := registryKeyOwner(key, mk); ok {
			owners[key] = owner
		}
	}
	return owners
}

// rekeyRegistry moves every <plugin>@oldName entry to <plugin>@newName, where
// oldName is the longest recorded name the key ends in. An
// install path under oldCache follows the cache directory to newCache; with
// no oldCache, no cache directory moved and every path stays. Entries for
// other marketplaces, and paths outside the cache, are untouched. A
// <plugin>@newName key taken by an orphan — RemoveMarketplace drops a
// marketplace's registration but not its registry entries — is refused before
// this runs, by refuseLeftoversUnder. Should one reach here anyway, the copy
// pass runs first and the moved entries overwrite it, dropping the orphan
// rather than letting map order decide whether a ghost replaces a live install.
func rekeyRegistry(reg Registry, owners map[string]string, oldName, newName, oldCache, newCache string) Registry {
	out := Registry{Version: reg.Version, Plugins: make(map[string][]InstallEntry, len(reg.Plugins))}
	for key, entries := range reg.Plugins {
		if owner, ok := owners[key]; !ok || owner != oldName {
			out.Plugins[key] = entries
		}
	}
	for key, entries := range reg.Plugins {
		if owner, ok := owners[key]; !ok || owner != oldName {
			continue
		}
		plugin := strings.TrimSuffix(key, "@"+oldName)
		moved := make([]InstallEntry, 0, len(entries))
		for _, e := range entries {
			if oldCache != "" {
				if rel, under := pathUnder(oldCache, e.InstallPath); under {
					e.InstallPath = filepath.Join(newCache, rel)
				}
			}
			moved = append(moved, e)
		}
		movedKey := registryKey(plugin, newName)
		out.Plugins[movedKey] = moved
		// The key is new, so its owner is the name it was just keyed under:
		// recorded, so the steps after this one in the same pass agree on it.
		owners[movedKey] = newName
	}
	return out
}

// pathPresent reports whether path is there, and refuses to guess: only a
// missing path counts as absent, so any other stat error — no permission on a
// store directory, an I/O error — is the caller's to return. A caller that
// read one as "nothing here" would skip moving a directory it could not see,
// bury a leftover it could not check, or install over a clone it never
// inspected, and commit the store files as though it had.
//
// Stat follows links, so a dangling symlink reports its missing target rather
// than itself; the Lstat fallback is what sees the link. The name is occupied
// either way — a rename onto it fails, and renaming it away moves the link,
// not what it once pointed at.
func pathPresent(path string) (bool, error) {
	if _, err := marketplaceStat(path); err != nil {
		// A path the filesystem will not consider at all holds nothing, so it
		// counts as absent rather than failing the operation that asked: the
		// migration has to be able to look at a legacy name's directories in
		// order to rename it, and failing here would leave every lock-taking
		// operation failing on the store instead.
		if !errors.Is(err, fs.ErrNotExist) && !pathCannotExist(err) {
			return false, fmt.Errorf("checking %s: %w", path, err)
		}
		if _, err := marketplaceLstat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) || pathCannotExist(err) {
				return false, nil
			}
			return false, fmt.Errorf("checking %s: %w", path, err)
		}
	}
	return true, nil
}

// pathPresentNoFollow reports whether a directory entry exists at path without
// resolving a final symlink — the entry RemoveAll and rename act on. It is what
// a sweep needs: a clone link whose target is unreadable (a self-loop, a
// permission wall) is still an entry the sweep must clear, where pathPresent's
// stat-through-the-link would error and strand it. Only a missing entry is
// absent; any other lstat error is the caller's to return.
func pathPresentNoFollow(path string) (bool, error) {
	if _, err := marketplaceLstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) || pathCannotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("checking %s: %w", path, err)
	}
	return true, nil
}

// refuseSourceInStore rejects a directory source that names one of the two
// directories the store manages, or anything inside them. The marketplaces
// directory holds every marketplace's clone plus the two scratch names a fetch
// clears and a swap deletes, and the cache holds materialized plugins; all of
// it is rewritten by the next add, refresh or re-source, which never asks what
// some marketplace is sourced from. So a source pointed there is registered
// against files the store is about to destroy — the marketplace's own clone,
// which a re-source to a directory schedules for removal, is only the nearest
// case of it. An empty path names nothing at all and is refused with them.
//
// The rule is about the path alone, so it carries no marketplace name: an add
// has none to give until it has fetched the catalog this refuses to fetch. A
// caller that does know the name says so around the error.
func (m *Manager) refuseSourceInStore(path string) error {
	if path == "" {
		return fmt.Errorf("a directory source needs a path: %w", ErrMarketplaceSourceInStore)
	}
	candidate, err := resolveForContainment(path)
	if err != nil {
		return err
	}
	for _, dir := range []string{m.marketplacesDir(), m.cacheDir()} {
		resolved, err := resolveForContainment(dir)
		if err != nil {
			return err
		}
		if pathWithinDir(resolved, candidate) {
			return fmt.Errorf("%w (%s is inside %s)", ErrMarketplaceSourceInStore, path, dir)
		}
	}
	return nil
}

// resolveForContainment canonicalises a path for a comparison about disk:
// absolute always, and physical when the path exists, so a symlinked store root
// cannot hide a containment that the filesystem would honour. A path that does
// not exist is resolved through the nearest ancestor that does, with the rest
// appended: symlinks above a missing leaf are still how the filesystem would
// read the path, and a directory an earlier migration moved leaves its former
// path comparing as wherever the symlinks above it point rather than as its own
// lexical string. The result is still a path nothing is at, so there is nothing
// on disk for a later step to destroy.
func resolveForContainment(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", path, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	missing := ""
	for dir := abs; ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs, nil
		}
		missing = filepath.Join(filepath.Base(dir), missing)
		dir = parent
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, missing), nil
		}
	}
}

// pathUnder is where under dir a path sits, and whether it is under dir at
// all. Both are taken as the store recorded them — an install path against
// the cache directory it was joined from — so neither is resolved here, and a
// path that is dir itself is under it.
func pathUnder(dir, path string) (rel string, under bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

// pathWithinDir reports whether candidate is dir itself or sits beneath it.
// Both must already be resolved.
func pathWithinDir(dir, candidate string) bool {
	rel, err := filepath.Rel(dir, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// recloneMarketplace replaces a marketplace clone whose git pull failed. The
// fresh clone is fully downloaded into a staging dir before the existing clone
// is touched, so the current clone — possibly wedged, but the only local copy
// — is never lost to a failed download; a failed reclone leaves it exactly as
// it was. The caller must hold m.lockPath(), which also serializes use of the
// shared staging/aside dirs.
func (m *Manager) recloneMarketplace(ctx context.Context, ref MarketplaceRef) error {
	staging := m.marketplaceDir(stagingCloneName)
	_ = marketplaceRemoveAll(staging)
	// After a successful swap the staging dir no longer exists, so this defer
	// only ever sweeps a leftover from a failed path.
	defer func() { _ = marketplaceRemoveAll(staging) }()
	if _, err := m.fetchMarketplaceContainer(ctx, ref.Source, staging); err != nil {
		return err
	}
	old, err := m.swapInClone(staging, ref.InstallLocation)
	if err != nil {
		return err
	}
	if old != "" {
		_ = marketplaceRemoveAll(old)
	}
	return nil
}

// swapInClone replaces dest with the fully-downloaded staging dir: rename any
// existing dest aside, then rename staging in. A failed swap restores dest, and
// no path removes the old clone before the new one is in place. The caller must
// hold m.lockPath().
//
// It returns the path the old clone was set aside under, or "" when dest held
// nothing. That directory is the caller's: it is the only copy of what dest
// held, so a caller whose work can still fail keeps it until the work is
// committed and every other caller removes it at once.
func (m *Manager) swapInClone(staging, dest string) (string, error) {
	// The aside name is this swap's own scratch, so a directory already there
	// is residue a caller's best-effort cleanup failed to remove. It has to go
	// first: renaming dest onto a non-empty directory fails, which would leave
	// the marketplace unable to re-source until someone deleted it by hand.
	old := m.marketplaceDir(asideCloneName)
	_ = marketplaceRemoveAll(old)
	occupied, err := pathPresent(dest)
	if err != nil {
		return "", err
	}
	movedAside := false
	if occupied {
		if err := marketplaceRename(dest, old); err != nil {
			return "", fmt.Errorf("moving old clone aside: %w", err)
		}
		movedAside = true
	}
	if err := marketplaceRename(staging, dest); err != nil {
		if movedAside {
			// Put the old clone back so dest keeps pointing at a real
			// directory. If even that fails, .old still holds the only
			// local copy — deliberately NOT swept — and the error says so.
			// errRenameRollbackIncomplete marks the state a caller that
			// scrubs before the wire (EditMarketplace's fail closure) has to
			// report apart from a clean, restored failure.
			if restoreErr := marketplaceRename(old, dest); restoreErr != nil {
				return "", errors.Join(
					fmt.Errorf("installing fresh clone failed (%w); restoring old clone: %w", err, restoreErr),
					errRenameRollbackIncomplete,
				)
			}
		}
		return "", fmt.Errorf("installing fresh clone: %w", err)
	}
	if !movedAside {
		return "", nil
	}
	return old, nil
}

// undoCloneSwap reverses a swapInClone whose caller's later step failed: the
// swapped-in clone goes and the aside copy the swap displaced is renamed back
// into dest. A caller whose work might still fail keeps the aside path for
// exactly this, so the store keeps pointing at the clone the surviving store
// file records instead of at the source the failed step had already fetched.
func undoCloneSwap(dest, aside string) error {
	if err := marketplaceRemoveAll(dest); err != nil {
		return fmt.Errorf("removing the swapped-in clone %s: %w", dest, err)
	}
	if aside == "" {
		return nil
	}
	return restoreRename("old clone", aside, dest)
}

func (m *Manager) RefreshMarketplace(ctx context.Context, name string) error {
	release, err := m.lockStore(ctx, marketplaceAcquireLock, 30*time.Second)
	if err != nil {
		return err
	}
	defer release()
	mk, err := m.loadMarketplaces()
	if err != nil {
		return err
	}
	ref, ok := mk[name]
	if !ok {
		return fmt.Errorf("marketplace %q: %w", name, ErrMarketplaceNotFound)
	}
	if ref.Source.Kind != SourceDirectory {
		if ref.InstallLocation == "" {
			// Never fetched (seeded pointer): clone now — that is the refresh.
			installLoc := m.marketplaceDir(name)
			_ = marketplaceRemoveAll(installLoc)
			if _, err := m.fetchMarketplaceContainer(ctx, ref.Source, installLoc); err != nil {
				// As in ensureFetched: the fetch cleared this directory
				// before cloning into it, and a failure would leave a
				// partial clone under the marketplace's name.
				_ = marketplaceRemoveAll(installLoc)
				return err
			}
			ref.InstallLocation = installLoc
		} else if pullErr := marketplaceGitPull(ctx, ref.InstallLocation); pullErr != nil {
			// A failed pull can mean the clone is wedged — e.g. a stale
			// .git/index.lock stranded by a killed git — and a plain retry
			// would then fail the same way forever. Self-heal with a staged
			// reclone; on failure it leaves the existing clone untouched.
			// When the pull failed because the request itself was canceled,
			// skip the doomed reclone and surface the cancellation directly.
			if ctx.Err() != nil {
				return pullErr
			}
			if recloneErr := m.recloneMarketplace(ctx, ref); recloneErr != nil {
				return fmt.Errorf("refreshing marketplace %q: git pull failed (%w); staged reclone failed: %w", name, pullErr, recloneErr)
			}
		}
	}
	ref.LastUpdated = m.now().UTC()
	mk[name] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		return m.saveFailed(name, marketplacesFileName, err)
	}
	return nil
}
