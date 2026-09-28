package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/afero"

	"primeradiant.com/evener/cmd/evener-hub/internal/fsdurability"
)

// This file owns §4's custody-first quarantine of a corrupt operation-store file
// and the crash recovery around it. The order is the safety contract: boot first
// persists the quarantine intent plus the custody file beside the store (same
// atomic write discipline, mode 0600, never inside the replaceable store file),
// and only then renames the corrupt file aside with the boot timestamp, never
// deleting it. The intent names the corrupt file, the custody file and the boot
// timestamp before either rename lands, so a crash between the custody write and
// the rename, or between the rename and the replacement open, still boots
// covered: an intent with no matching aside file re-runs the rename, and an
// aside file with no complete custody fails startup, never serves.
//
// The durable `quarantineEpoch` lives in its own sidecar beside the store — never
// inside the store file, which a quarantine replaces — and the live epoch is the
// maximum of that sidecar and every custody file's own epoch, so a crash never
// advances the counter twice or loses an advance.

const (
	// quarantineIntentSuffix, quarantineEpochSuffix, quarantineCustodyInfix and
	// quarantineAsideInfix name the artifacts a quarantine leaves beside the
	// store. The store's own base name prefixes each, so a state directory
	// holding several stores' artifacts keeps them apart.
	quarantineIntentSuffix = ".quarantine-intent.json"
	quarantineEpochSuffix  = ".quarantine-epoch.json"
	quarantineCustodyInfix = ".custody-"
	quarantineAsideInfix   = ".quarantined-"
)

// quarantineIntent is the pending-work record §4 writes before either rename
// lands: the corrupt file, the custody file and the boot timestamp, so the
// recovery boot knows exactly what it was doing.
type quarantineIntent struct {
	CorruptFile     string    `json:"corruptFile"`
	CustodyFile     string    `json:"custodyFile"`
	AsideFile       string    `json:"asideFile"`
	QuarantinedAt   time.Time `json:"quarantinedAt"`
	QuarantineEpoch uint64    `json:"quarantineEpoch"`
}

// quarantineEpochFile is the sidecar's schema: the durable counter §4 requires
// be persisted outside the quarantined file.
type quarantineEpochFile struct {
	QuarantineEpoch uint64 `json:"quarantineEpoch"`
}

// QuarantineSignal is the operator-visible health signal §4 requires a
// quarantined store to boot with: it names the quarantined file, the custody
// file that snapshot it, and the epoch the quarantine advanced to.
type QuarantineSignal struct {
	QuarantinedFile string
	CustodyFile     string
	QuarantineEpoch uint64
}

// Quarantine returns the handle's quarantine signal — nil when no custody file
// exists for the store this handle reads. It stays non-nil across boots for as
// long as the custody file does, because the names that custody closed stay
// closed until `orphan-resolve` clears them.
func (s *Store) Quarantine() *QuarantineSignal {
	if s == nil {
		return nil
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()
	if s.cell.quarantine == nil {
		return nil
	}
	signal := *s.cell.quarantine
	return &signal
}

// quarantineIntentPath, quarantineEpochPath, quarantineCustodyPath and
// quarantineAsidePath name the artifacts beside the store at storePath.
func quarantineIntentPath(storePath string) string { return storePath + quarantineIntentSuffix }
func quarantineEpochPath(storePath string) string  { return storePath + quarantineEpochSuffix }
func quarantineCustodyPath(storePath, stamp string) string {
	return storePath + quarantineCustodyInfix + stamp + ".json"
}
func quarantineAsidePath(storePath, stamp string) string {
	return storePath + quarantineAsideInfix + stamp
}

// quarantineStamp renders a boot timestamp as a filename-safe, sortable token:
// the UTC instant at nanosecond resolution with the punctuation that does not
// survive a filesystem round trip removed.
func quarantineStamp(at time.Time) string {
	return at.UTC().Format("20060102T150405.000000000Z")
}

// quarantineArtifacts is the quarantine state a store's directory holds: the
// durable epoch, the newest complete custody file (the operator-visible signal),
// and every aside file the store was renamed to.
type quarantineArtifacts struct {
	epoch      uint64
	signal     *QuarantineSignal
	newestAsid string
	asides     []string
}

// scanQuarantineArtifacts reads the directory beside the store. Every custody
// file must be complete — the boot refuses to serve beside a custody file it
// cannot account for — and every aside file must be matched by the custody file
// of the same stamp (§4: "an aside file with no complete custody fails startup,
// never serves").
func scanQuarantineArtifacts(fs afero.Fs, storePath string) (quarantineArtifacts, error) {
	var artifacts quarantineArtifacts
	if body, err := afero.ReadFile(fs, quarantineEpochPath(storePath)); err == nil {
		var sidecar quarantineEpochFile
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&sidecar); err != nil {
			return artifacts, quarantineFailed(fmt.Errorf("decode epoch sidecar %s: %w", quarantineEpochPath(storePath), err))
		}
		artifacts.epoch = sidecar.QuarantineEpoch
	} else if !errors.Is(err, os.ErrNotExist) {
		return artifacts, fmt.Errorf("hostops: read quarantine epoch %s: %w", quarantineEpochPath(storePath), err)
	}

	names, err := readDirNames(fs, filepath.Dir(storePath))
	if err != nil {
		return artifacts, err
	}
	base := filepath.Base(storePath)
	for _, name := range names {
		switch {
		case strings.HasPrefix(name, base+quarantineCustodyInfix) && strings.HasSuffix(name, ".json"):
			stamp := strings.TrimSuffix(strings.TrimPrefix(name, base+quarantineCustodyInfix), ".json")
			if stamp == "" {
				return artifacts, quarantineFailed(fmt.Errorf("custody file %s carries no timestamp", name))
			}
			custodyPath := filepath.Join(filepath.Dir(storePath), name)
			custody, err := readCustodyFile(fs, custodyPath, storePath)
			if err != nil {
				return artifacts, quarantineFailed(err)
			}
			if custody.QuarantineEpoch > artifacts.epoch {
				artifacts.epoch = custody.QuarantineEpoch
			}
			if artifacts.signal == nil || custody.QuarantineEpoch > artifacts.signal.QuarantineEpoch ||
				(custody.QuarantineEpoch == artifacts.signal.QuarantineEpoch && stamp > stampOf(artifacts.signal.CustodyFile, base)) {
				artifacts.signal = &QuarantineSignal{
					QuarantinedFile: custody.QuarantinedFile,
					CustodyFile:     custodyPath,
					QuarantineEpoch: custody.QuarantineEpoch,
				}
			}
		case strings.HasPrefix(name, base+quarantineAsideInfix):
			stamp := strings.TrimPrefix(name, base+quarantineAsideInfix)
			if stamp == "" {
				return artifacts, quarantineFailed(fmt.Errorf("aside file %s carries no timestamp", name))
			}
			custodyPath := filepath.Join(filepath.Dir(storePath), base+quarantineCustodyInfix+stamp+".json")
			if _, err := readCustodyFile(fs, custodyPath, storePath); err != nil {
				// §4: an aside file with no complete custody fails startup.
				return artifacts, quarantineFailed(err)
			}
			artifacts.asides = append(artifacts.asides, filepath.Join(filepath.Dir(storePath), name))
			if stamp > artifacts.newestAsid {
				artifacts.newestAsid = stamp
			}
		}
	}
	return artifacts, nil
}

// stampOf extracts the timestamp token from a custody file path.
func stampOf(custodyPath, base string) string {
	name := filepath.Base(custodyPath)
	return strings.TrimSuffix(strings.TrimPrefix(name, base+quarantineCustodyInfix), ".json")
}

// quarantineFailed classifies a quarantine-path failure: it is a store-corrupt
// refusal (the load path's own class) that additionally names the incomplete
// custody, so a caller can tell "quarantine could not be performed" from "the
// store loaded".
func quarantineFailed(err error) error {
	return fmt.Errorf("%w: %w", ErrStoreCorrupt, err)
}

// custodyIncomplete wraps a construction failure as an incomplete-custody
// refusal.
func custodyIncomplete(err error) error {
	return quarantineFailed(fmt.Errorf("%w: %w", ErrQuarantineIncomplete, err))
}

// resolveStoreFS loads the operation store at path, performing §4's custody-first
// quarantine when the file is corrupt and completing any quarantine a crash
// interrupted. It returns the state the handle serves, the live durable
// quarantine epoch, and the operator-visible signal (nil when no custody file
// exists).
func resolveStoreFS(fs afero.Fs, path string, faults storeFaults) (snapshot, uint64, *QuarantineSignal, error) {
	artifacts, err := scanQuarantineArtifacts(fs, path)
	if err != nil {
		return snapshot{}, 0, nil, err
	}
	epoch := artifacts.epoch
	signal := artifacts.signal

	intent, haveIntent, err := readQuarantineIntent(fs, path)
	if err != nil {
		return snapshot{}, 0, nil, err
	}
	if haveIntent {
		custody, err := readCustodyFile(fs, intent.CustodyFile, path)
		if err != nil {
			return snapshot{}, 0, nil, quarantineFailed(err)
		}
		// An intent with no matching aside file re-runs the rename; the custody
		// file is already written and complete, because it is written before the
		// rename.
		if _, err := lstat(fs, intent.AsideFile); errors.Is(err, os.ErrNotExist) {
			if _, err := lstat(fs, path); err == nil {
				if err := renameStoreAside(fs, path, intent.AsideFile, faults); err != nil {
					return snapshot{}, 0, nil, err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return snapshot{}, 0, nil, fmt.Errorf("hostops: stat store %s: %w", path, err)
			}
		} else if err != nil {
			return snapshot{}, 0, nil, fmt.Errorf("hostops: stat quarantine aside %s: %w", intent.AsideFile, err)
		}
		if custody.QuarantineEpoch > epoch {
			epoch = custody.QuarantineEpoch
		}
		if signal == nil || signal.QuarantineEpoch < custody.QuarantineEpoch {
			signal = &QuarantineSignal{
				QuarantinedFile: custody.QuarantinedFile,
				CustodyFile:     intent.CustodyFile,
				QuarantineEpoch: custody.QuarantineEpoch,
			}
		}
		// Between the rename and the replacement-store open: a store standing at
		// the path is the replacement the interrupted boot wrote (it loads
		// cleanly) or a later file; either way a clean store is served, and the
		// intent is cleared as completed. A missing store is not a clean one —
		// loadFS reads a missing file as an empty store — so the existence check
		// comes first and the missing case completes the replacement from the
		// custody file the intent named.
		_, statErr := lstat(fs, path)
		switch {
		case statErr == nil:
			if state, err := loadFS(fs, path); err == nil {
				if err := clearQuarantineIntent(fs, path, faults); err != nil {
					return snapshot{}, 0, nil, err
				}
				if err := writeQuarantineEpoch(fs, path, epoch, faults); err != nil {
					return snapshot{}, 0, nil, err
				}
				return state, epoch, signal, nil
			}
		case errors.Is(statErr, os.ErrNotExist):
			state, err := replacementState(custody, intent.CustodyFile)
			if err != nil {
				return snapshot{}, 0, nil, custodyIncomplete(err)
			}
			if err := writeReplacement(fs, path, state, faults); err != nil {
				return snapshot{}, 0, nil, err
			}
			if err := clearQuarantineIntent(fs, path, faults); err != nil {
				return snapshot{}, 0, nil, err
			}
			if err := writeQuarantineEpoch(fs, path, epoch, faults); err != nil {
				return snapshot{}, 0, nil, err
			}
			return state, epoch, signal, nil
		default:
			return snapshot{}, 0, nil, fmt.Errorf("hostops: stat store %s: %w", path, statErr)
		}
		// The store at the path is corrupt and the intent's own quarantine is
		// already covered (its rename landed and its custody is complete), so
		// the intent is stale: clear it and quarantine this file in its own
		// custody-first order below.
		if err := clearQuarantineIntent(fs, path, faults); err != nil {
			return snapshot{}, 0, nil, err
		}
	} else if _, err := lstat(fs, path); errors.Is(err, os.ErrNotExist) && artifacts.newestAsid != "" {
		// The store file is gone but an aside and its complete custody remain: a
		// completed quarantine whose replacement vanished. Rebuild the
		// replacement from the custody the quarantine already took.
		custodyPath := filepath.Join(filepath.Dir(path), filepath.Base(path)+quarantineCustodyInfix+artifacts.newestAsid+".json")
		custody, err := readCustodyFile(fs, custodyPath, path)
		if err != nil {
			return snapshot{}, 0, nil, quarantineFailed(err)
		}
		state, err := replacementState(custody, custodyPath)
		if err != nil {
			return snapshot{}, 0, nil, custodyIncomplete(err)
		}
		if err := writeReplacement(fs, path, state, faults); err != nil {
			return snapshot{}, 0, nil, err
		}
		if custody.QuarantineEpoch > epoch {
			epoch = custody.QuarantineEpoch
		}
		if err := writeQuarantineEpoch(fs, path, epoch, faults); err != nil {
			return snapshot{}, 0, nil, err
		}
		if signal == nil {
			signal = &QuarantineSignal{
				QuarantinedFile: custody.QuarantinedFile,
				CustodyFile:     custodyPath,
				QuarantineEpoch: custody.QuarantineEpoch,
			}
		}
		return state, epoch, signal, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return snapshot{}, 0, nil, fmt.Errorf("hostops: stat store %s: %w", path, err)
	}

	state, loadErr := loadFS(fs, path)
	if loadErr == nil {
		return state, epoch, signal, nil
	}
	if !errors.Is(loadErr, ErrStoreCorrupt) {
		return snapshot{}, 0, nil, loadErr
	}
	// A corrupt store file: custody-first quarantine. Extraction runs before any
	// write, so an incomplete snapshot leaves the directory untouched.
	decoded, err := readStoreForCustody(fs, path)
	if err != nil {
		return snapshot{}, 0, nil, custodyIncomplete(err)
	}
	custody, err := custodyFromStore(decoded, path, epoch+1, time.Now().UTC())
	if err != nil {
		return snapshot{}, 0, nil, custodyIncomplete(err)
	}
	custodyPath := quarantineCustodyPath(path, quarantineStamp(custody.CustodiedAt))
	asidePath := quarantineAsidePath(path, quarantineStamp(custody.CustodiedAt))
	if _, err := lstat(fs, asidePath); err == nil {
		return snapshot{}, 0, nil, custodyIncomplete(fmt.Errorf("the aside file %s already exists", asidePath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return snapshot{}, 0, nil, fmt.Errorf("hostops: stat quarantine aside %s: %w", asidePath, err)
	}
	custodyBytes, err := json.Marshal(custody)
	if err != nil {
		return snapshot{}, 0, nil, fmt.Errorf("hostops: marshal custody: %w", err)
	}
	pending := quarantineIntent{
		CorruptFile:     path,
		CustodyFile:     custodyPath,
		AsideFile:       asidePath,
		QuarantinedAt:   custody.CustodiedAt,
		QuarantineEpoch: custody.QuarantineEpoch,
	}
	intentBytes, err := json.Marshal(pending)
	if err != nil {
		return snapshot{}, 0, nil, fmt.Errorf("hostops: marshal quarantine intent: %w", err)
	}
	if err := writeQuarantineFile(fs, quarantineIntentPath(path), intentBytes, faults); err != nil {
		return snapshot{}, 0, nil, err
	}
	if faults.afterIntentWrite != nil {
		if err := faults.afterIntentWrite(); err != nil {
			return snapshot{}, 0, nil, err
		}
	}
	if err := writeQuarantineFile(fs, custodyPath, custodyBytes, faults); err != nil {
		return snapshot{}, 0, nil, err
	}
	if faults.afterCustodyWrite != nil {
		if err := faults.afterCustodyWrite(); err != nil {
			return snapshot{}, 0, nil, err
		}
	}
	if err := writeQuarantineEpoch(fs, path, custody.QuarantineEpoch, faults); err != nil {
		return snapshot{}, 0, nil, err
	}
	if err := renameStoreAside(fs, path, asidePath, faults); err != nil {
		return snapshot{}, 0, nil, err
	}
	if faults.afterRename != nil {
		if err := faults.afterRename(); err != nil {
			return snapshot{}, 0, nil, err
		}
	}
	replacement, err := replacementState(custody, custodyPath)
	if err != nil {
		return snapshot{}, 0, nil, custodyIncomplete(err)
	}
	if err := writeReplacement(fs, path, replacement, faults); err != nil {
		return snapshot{}, 0, nil, err
	}
	if faults.beforeIntentClear != nil {
		if err := faults.beforeIntentClear(); err != nil {
			return snapshot{}, 0, nil, err
		}
	}
	if err := clearQuarantineIntent(fs, path, faults); err != nil {
		return snapshot{}, 0, nil, err
	}
	signal = &QuarantineSignal{
		QuarantinedFile: custody.QuarantinedFile,
		CustodyFile:     custodyPath,
		QuarantineEpoch: custody.QuarantineEpoch,
	}
	return replacement, custody.QuarantineEpoch, signal, nil
}

// writeReplacement writes the replacement store's state with the store's own
// atomic write discipline.
func writeReplacement(fs afero.Fs, path string, state snapshot, faults storeFaults) error {
	renamed, err := saveFS(fs, path, state, faults)
	if err != nil {
		if renamed {
			// The rename landed: the replacement is the file's contents. A
			// directory-sync failure behind it is reported, but the state is
			// durable enough to serve and the next write converges the sync.
			return nil
		}
		return err
	}
	return nil
}

// renameStoreAside renames the corrupt store file aside with the boot timestamp,
// never deleting it. The directory entry the rename lands in is synced, so the
// aside file is durable before anything depends on it.
func renameStoreAside(fs afero.Fs, path, aside string, faults storeFaults) error {
	info, err := lstat(fs, path)
	if err != nil {
		return fmt.Errorf("hostops: stat store %s: %w", path, err)
	}
	if err := rejectNonStoreFileKind(path, info); err != nil {
		return err
	}
	if err := fs.Rename(path, aside); err != nil {
		return fmt.Errorf("hostops: rename corrupt store aside: %w", err)
	}
	sync := syncDirFS
	if faults.syncDir != nil {
		sync = faults.syncDir
	}
	if err := sync(fs, filepath.Dir(aside)); err != nil {
		return &postRenameError{err: fmt.Errorf("hostops: sync directory behind the quarantine rename: %w", err)}
	}
	return nil
}

// clearQuarantineIntent removes the pending-work record once the quarantine it
// describes is complete. The removal is itself made durable, so a crash cannot
// resurrect an intent whose work the next boot would redo.
func clearQuarantineIntent(fs afero.Fs, path string, faults storeFaults) error {
	intentPath := quarantineIntentPath(path)
	if _, err := lstat(fs, intentPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("hostops: stat quarantine intent %s: %w", intentPath, err)
	}
	if err := fs.Remove(intentPath); err != nil {
		return fmt.Errorf("hostops: remove quarantine intent %s: %w", intentPath, err)
	}
	sync := syncDirFS
	if faults.syncDir != nil {
		sync = faults.syncDir
	}
	if err := sync(fs, filepath.Dir(intentPath)); err != nil {
		return err
	}
	return nil
}

// readQuarantineIntent reads the pending quarantine, if any. An intent that is
// not the schema the writing boot emits — or that does not name this store — is
// itself incomplete custody: the boot cannot know what work it was in the middle
// of, so it refuses.
func readQuarantineIntent(fs afero.Fs, path string) (quarantineIntent, bool, error) {
	intentPath := quarantineIntentPath(path)
	raw, err := afero.ReadFile(fs, intentPath)
	if errors.Is(err, os.ErrNotExist) {
		return quarantineIntent{}, false, nil
	}
	if err != nil {
		return quarantineIntent{}, false, fmt.Errorf("hostops: read quarantine intent %s: %w", intentPath, err)
	}
	if err := checkQuarantineBytes(raw); err != nil {
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("quarantine intent %s: %w", intentPath, err))
	}
	var intent quarantineIntent
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&intent); err != nil {
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("decode quarantine intent %s: %w", intentPath, err))
	}
	switch {
	case intent.CorruptFile != path:
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("quarantine intent %s names the corrupt file %q, not %q",
			intentPath, intent.CorruptFile, path))
	case intent.CustodyFile == "":
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("quarantine intent %s names no custody file", intentPath))
	case intent.AsideFile == "":
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("quarantine intent %s names no aside file", intentPath))
	case intent.QuarantinedAt.IsZero():
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("quarantine intent %s carries no boot timestamp", intentPath))
	case intent.QuarantineEpoch == 0:
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("quarantine intent %s carries no quarantine epoch", intentPath))
	}
	return intent, true, nil
}

// writeQuarantineEpoch persists the live quarantine epoch in its sidecar beside
// the store, with the same atomic protocol every durable write here uses. It is
// written only forward: a recovery boot that sees an older sidecar converges it
// to the epoch the custody files prove.
func writeQuarantineEpoch(fs afero.Fs, path string, epoch uint64, faults storeFaults) error {
	if current, err := readQuarantineEpochSidecar(fs, path); err == nil && current >= epoch {
		return nil
	}
	body, err := json.Marshal(quarantineEpochFile{QuarantineEpoch: epoch})
	if err != nil {
		return fmt.Errorf("hostops: marshal quarantine epoch: %w", err)
	}
	return writeQuarantineFile(fs, quarantineEpochPath(path), body, faults)
}

// readQuarantineEpochSidecar reads the durable counter's sidecar.
func readQuarantineEpochSidecar(fs afero.Fs, path string) (uint64, error) {
	raw, err := afero.ReadFile(fs, quarantineEpochPath(path))
	if err != nil {
		return 0, err
	}
	var sidecar quarantineEpochFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sidecar); err != nil {
		return 0, err
	}
	return sidecar.QuarantineEpoch, nil
}

// writeQuarantineFile writes one quarantine artifact — the intent, the custody
// file, the epoch sidecar — with §4's atomic write discipline: a temp file in
// the same directory, fsynced, renamed over the target, then the directory entry
// fsynced. The artifact is never inside the replaceable store file, and it is
// always mode 0600.
func writeQuarantineFile(fs afero.Fs, path string, data []byte, faults storeFaults) error {
	dir := filepath.Dir(path)
	sync := syncDirFS
	if faults.syncDir != nil {
		sync = faults.syncDir
	}
	if err := ensureStoreDir(fs, dir, sync); err != nil {
		return err
	}
	temp, err := afero.TempFile(fs, dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("hostops: create temp quarantine file: %w", err)
	}
	tempPath := temp.Name()
	renamed := false
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if !renamed {
			_ = fs.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("hostops: write temp quarantine file: %w", err)
	}
	if err := fs.Chmod(tempPath, 0o600); err != nil {
		return fmt.Errorf("hostops: set quarantine file mode: %w", err)
	}
	if err := temp.Sync(); err != nil && !fsdurability.SyncUnsupported(err) {
		return fmt.Errorf("hostops: sync temp quarantine file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("hostops: close temp quarantine file: %w", err)
	}
	temp = nil
	if err := fs.Rename(tempPath, path); err != nil {
		return fmt.Errorf("hostops: rename quarantine file: %w", err)
	}
	renamed = true
	if err := sync(fs, dir); err != nil {
		return &postRenameError{err: fmt.Errorf("hostops: sync directory behind %s: %w", filepath.Base(path), err)}
	}
	return nil
}

// checkQuarantineBytes applies the byte-level rules every quarantine artifact
// must carry: valid UTF-8, no unpaired surrogate escape, and no key named twice
// anywhere in the document. The artifacts have no canonical byte-level key set
// of their own — their schemas are checked by the strict decode that follows —
// but a document this check refuses is not one the writing boot produced.
func checkQuarantineBytes(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("the document is not valid UTF-8")
	}
	if err := rejectLoneSurrogateEscapes(raw); err != nil {
		return err
	}
	return validateRawFieldKeys(raw)
}
