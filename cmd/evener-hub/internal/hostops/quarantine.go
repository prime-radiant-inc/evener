package hostops

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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
// long as the custody file does: the signal describes that file, not a state a
// later pass clears, and the orphan gate that once held names closed was
// withdrawn (comp08).
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
	epoch  uint64
	signal *QuarantineSignal
	// rebuildStamp is the custody/aside timestamp the vanished-replacement
	// rebuild reads from: the pair with the highest validated quarantine epoch,
	// with the filename timestamp only as a tie-breaker, so a backward-moving
	// clock cannot make an older quarantine's pair win.
	rebuildStamp string
	rebuildEpoch uint64
	asides       []string
	// allocatorFloor is the highest controller-assigned id any existing custody
	// file for this store handed out (its high-water mark or one of its imported
	// ids). The replacement allocator and every later quarantine's ownership ids
	// start above it, so no id is ever reused across quarantine epochs.
	allocatorFloor uint64
	// custodyStamps and asideStamps are the timestamps of the custody and aside
	// files found; resolveStoreFS checks they are one-to-one (each custody file
	// either has its aside or is the one a pending intent names).
	custodyStamps map[string]bool
	asideStamps   map[string]bool
	// custodyEpochs maps each custody file's timestamp to the epoch it carries,
	// so a cleanup that removes some custody files can re-derive the
	// operator-visible signal over the survivors instead of handing back one
	// naming a file it just deleted.
	custodyEpochs map[string]uint64
	// sidecarEpoch is the durable counter the sidecar file carried on its own
	// (zero when there is none). resolveStoreFS persists the live epoch whenever
	// it exceeds it, so the counter cannot regress if the custody files are
	// cleaned away later.
	sidecarEpoch uint64
	// priorImports maps every id an existing custody file already handed out to
	// the entry that carried it, so a new fence import cannot reuse one under a
	// different identity.
	priorImports map[string]custodyFence
}

// scanQuarantineArtifacts reads the directory beside the store. Every custody
// file must be complete — the boot refuses to serve beside a custody file it
// cannot account for — and every aside file must be matched by the custody file
// of the same stamp (§4: "an aside file with no complete custody fails startup,
// never serves").
func scanQuarantineArtifacts(fs afero.Fs, storePath string) (quarantineArtifacts, error) {
	artifacts := quarantineArtifacts{
		custodyStamps: map[string]bool{},
		asideStamps:   map[string]bool{},
		custodyEpochs: map[string]uint64{},
		priorImports:  map[string]custodyFence{},
	}
	sidecarEpoch, found, err := readQuarantineEpochFile(fs, storePath)
	if err != nil {
		return artifacts, quarantineFailed(err)
	}
	if found {
		artifacts.epoch = sidecarEpoch
		artifacts.sidecarEpoch = sidecarEpoch
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
			artifacts.custodyStamps[stamp] = true
			artifacts.custodyEpochs[stamp] = custody.QuarantineEpoch
			fenceByID := make(map[string]custodyFence, len(custody.Fences))
			for _, fence := range custody.Fences {
				fenceByID[fence.RecordID] = fence
			}
			for _, row := range custody.RecordIDs {
				if fence, ok := fenceByID[row.RecordID]; ok {
					artifacts.priorImports[row.RecordID] = fence
					continue
				}
				artifacts.priorImports[row.RecordID] = custodyFence{RecordID: row.RecordID, Host: row.Host}
			}
			if artifacts.rebuildStamp == "" || custody.QuarantineEpoch > artifacts.rebuildEpoch ||
				(custody.QuarantineEpoch == artifacts.rebuildEpoch && stamp > artifacts.rebuildStamp) {
				artifacts.rebuildStamp = stamp
				artifacts.rebuildEpoch = custody.QuarantineEpoch
			}
			if custody.AllocatorHighWaterMark > artifacts.allocatorFloor {
				artifacts.allocatorFloor = custody.AllocatorHighWaterMark
			}
			for _, row := range custody.RecordIDs {
				allocated, err := parseAllocatorID(row.RecordID)
				if err != nil {
					return artifacts, quarantineFailed(fmt.Errorf("custody file %s names record id %q, which is not a controller-assigned id",
						custodyPath, row.RecordID))
				}
				if allocated > artifacts.allocatorFloor {
					artifacts.allocatorFloor = allocated
				}
			}
		case strings.HasPrefix(name, base+quarantineAsideInfix):
			stamp := strings.TrimPrefix(name, base+quarantineAsideInfix)
			if stamp == "" {
				return artifacts, quarantineFailed(fmt.Errorf("aside file %s carries no timestamp", name))
			}
			asidePath := filepath.Join(filepath.Dir(storePath), name)
			info, err := lstat(fs, asidePath)
			if err != nil {
				return artifacts, quarantineFailed(fmt.Errorf("stat aside file %s: %w", asidePath, err))
			}
			if err := rejectNonStoreFileKind(asidePath, info); err != nil {
				return artifacts, quarantineFailed(err)
			}
			if perm := info.Mode().Perm(); !ownerOnly(perm) {
				return artifacts, quarantineFailed(fmt.Errorf("%w: aside file %s has mode %04o",
					ErrStoreReadableBeyondOwner, asidePath, perm))
			}
			custodyPath := filepath.Join(filepath.Dir(storePath), base+quarantineCustodyInfix+stamp+".json")
			if _, err := readCustodyFile(fs, custodyPath, storePath); err != nil {
				// §4: an aside file with no complete custody fails startup.
				return artifacts, quarantineFailed(err)
			}
			artifacts.asideStamps[stamp] = true
			artifacts.asides = append(artifacts.asides, asidePath)
		}
	}
	return artifacts, nil
}

// readQuarantineEpochFile reads the durable counter's sidecar: a regular file,
// owner-only, carrying exactly the sidecar schema. Anything else is refused, so
// a symlinked or hand-edited sidecar cannot move the counter the cursors pin.
func readQuarantineEpochFile(fs afero.Fs, storePath string) (uint64, bool, error) {
	sidecarPath := quarantineEpochPath(storePath)
	info, err := lstat(fs, sidecarPath)
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("stat quarantine epoch %s: %w", sidecarPath, err)
	}
	if err := rejectNonStoreFileKind(sidecarPath, info); err != nil {
		return 0, false, err
	}
	if perm := info.Mode().Perm(); !ownerOnly(perm) {
		return 0, false, fmt.Errorf("%w: %s has mode %04o", ErrStoreReadableBeyondOwner, sidecarPath, perm)
	}
	body, err := afero.ReadFile(fs, sidecarPath)
	if err != nil {
		return 0, false, fmt.Errorf("read quarantine epoch %s: %w", sidecarPath, err)
	}
	if err := checkQuarantineBytes(body); err != nil {
		return 0, false, fmt.Errorf("quarantine epoch %s: %w", sidecarPath, err)
	}
	var sidecar quarantineEpochFile
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&sidecar); err != nil {
		return 0, false, fmt.Errorf("decode epoch sidecar %s: %w", sidecarPath, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return 0, false, fmt.Errorf("epoch sidecar %s carries trailing data", sidecarPath)
	}
	if sidecar.QuarantineEpoch == 0 {
		// The writer only ever persists a counter above zero; an empty object or
		// a zero value is a sidecar this store did not write, and reading it as
		// "no quarantine yet" would hand a pre-quarantine cursor its old epoch
		// back.
		return 0, false, fmt.Errorf("epoch sidecar %s carries no quarantine epoch", sidecarPath)
	}
	return sidecar.QuarantineEpoch, true, nil
}

// stampOf extracts the timestamp token from a custody file path.
func stampOf(custodyPath, base string) string {
	name := filepath.Base(custodyPath)
	return strings.TrimSuffix(strings.TrimPrefix(name, base+quarantineCustodyInfix), ".json")
}

// signalAfterRemoving re-derives the operator-visible signal over the custody
// files a cleanup left behind: the highest-epoch survivor, with the filename
// timestamp only as a tie-breaker, or nil when none is left. The scan's signal
// may name a file the cleanup just removed, and handing that back would make
// Store.Quarantine() and the boot log name a custody file that no longer exists
// — asserting a quarantine the store does not carry. The re-scan keeps a real
// survivor visible: a custody file that is still on disk may still be the
// evidence the operator needs to resolve the names it closed.
func (a quarantineArtifacts) signalAfterRemoving(storePath string, removed ...string) *QuarantineSignal {
	best := ""
	for stamp, epoch := range a.custodyEpochs {
		if slices.Contains(removed, stamp) {
			continue
		}
		if best == "" || epoch > a.custodyEpochs[best] || (epoch == a.custodyEpochs[best] && stamp > best) {
			best = stamp
		}
	}
	if best == "" {
		return nil
	}
	return &QuarantineSignal{
		QuarantinedFile: storePath,
		CustodyFile:     quarantineCustodyPath(storePath, best),
		QuarantineEpoch: a.custodyEpochs[best],
	}
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
	if epoch > artifacts.sidecarEpoch {
		// The counter never regresses: the custody files prove an epoch above the
		// sidecar's own value (which may have been lost), so it is persisted
		// again here — before any path can return — and survives the custody
		// artifacts' later cleanup.
		if err := writeQuarantineEpoch(fs, path, epoch, faults); err != nil {
			return snapshot{}, 0, nil, err
		}
	}

	intent, haveIntent, err := readQuarantineIntent(fs, path)
	if err != nil {
		return snapshot{}, 0, nil, err
	}
	// The artifacts must be one-to-one: every custody file either has its aside
	// on disk or is the one a pending intent names. A custody file with neither
	// is a quarantine that lost its rename and its intent — the corrupt bytes it
	// custodied cannot be shown to exist, so the boot refuses rather than
	// serving beside an unproven snapshot. The one exception is a store that
	// demonstrably serves: when the path loads cleanly, the corrupted bytes are
	// gone for good and the custody cannot keep any name closed that the served
	// store does not carry, so it is not evidence of anything — it is removed on
	// the clean path below (its durable epoch was persisted to the sidecar just
	// above), because leaving it would refuse every later boot over this rule.
	pendingStamp := ""
	if haveIntent {
		if stamp, ok := strings.CutPrefix(intent.AsideFile, path+quarantineAsideInfix); ok {
			pendingStamp = stamp
		}
	}
	var orphans []string
	var orphanStamps []string
	storeLoads := false
	for stamp := range artifacts.custodyStamps {
		if artifacts.asideStamps[stamp] || stamp == pendingStamp {
			continue
		}
		if !storeLoads {
			if _, err := lstat(fs, path); err == nil {
				if _, err := loadFS(fs, path); err == nil {
					storeLoads = true
				}
			}
		}
		if !storeLoads {
			return snapshot{}, 0, nil, quarantineFailed(fmt.Errorf(
				"%w: custody file %s has neither its aside file nor a pending intent",
				ErrQuarantineIncomplete, quarantineCustodyPath(path, stamp)))
		}
		orphans = append(orphans, quarantineCustodyPath(path, stamp))
		orphanStamps = append(orphanStamps, stamp)
	}
	if haveIntent {
		custody, err := readCustodyFile(fs, intent.CustodyFile, path)
		if err != nil {
			return snapshot{}, 0, nil, quarantineFailed(err)
		}
		asideExists := false
		if _, err := lstat(fs, intent.AsideFile); err == nil {
			asideExists = true
		} else if !errors.Is(err, os.ErrNotExist) {
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
		_, statErr := lstat(fs, path)
		switch {
		case errors.Is(statErr, os.ErrNotExist):
			// The pending rename has nothing to rename — the corrupt file is gone
			// (renamed by an earlier boot, or removed) — and the replacement the
			// intent owes has not been written.
			return replacementFromCustody(fs, path, intent.CustodyFile, custody, artifacts.allocatorFloor, epoch, signal, faults)
		case statErr != nil:
			return snapshot{}, 0, nil, fmt.Errorf("hostops: stat store %s: %w", path, statErr)
		default:
			state, loadErr := loadFS(fs, path)
			switch {
			case loadErr == nil:
				// A clean store stands at the path: serve it as-is. The pending
				// rename is never re-run over a healthy file — that would set a
				// good store aside under a stale intent — so the intent is cleared
				// and the counter persisted.
				if err := clearQuarantineIntent(fs, path, faults); err != nil {
					return snapshot{}, 0, nil, err
				}
				removed := slices.Clone(orphanStamps)
				if !asideExists {
					// The intent's own custody is orphaned by definition here (its
					// aside is missing), so it goes with the intent.
					if err := removeQuarantineArtifact(fs, intent.CustodyFile, faults); err != nil {
						return snapshot{}, 0, nil, err
					}
					removed = append(removed, pendingStamp)
				}
				for _, orphan := range orphans {
					if err := removeQuarantineArtifact(fs, orphan, faults); err != nil {
						return snapshot{}, 0, nil, err
					}
				}
				if len(removed) > 0 {
					signal = artifacts.signalAfterRemoving(path, removed...)
				}
				if err := writeQuarantineEpoch(fs, path, epoch, faults); err != nil {
					return snapshot{}, 0, nil, err
				}
				return state, epoch, signal, nil
			case errors.Is(loadErr, ErrStoreCorrupt):
				if !asideExists {
					// The covered window: the rename never landed, so the corrupt
					// file at the path is this intent's own quarry. Re-run the
					// rename and complete the replacement from the intent's custody.
					if err := renameStoreAside(fs, path, intent.AsideFile, faults); err != nil {
						return snapshot{}, 0, nil, err
					}
					return replacementFromCustody(fs, path, intent.CustodyFile, custody, artifacts.allocatorFloor, epoch, signal, faults)
				}
				// The intent's own rename already landed and a different
				// corruption stands at the path: the intent is stale. Clear it and
				// quarantine this file in its own custody-first order below, never
				// moving it aside under the stale intent.
				if err := clearQuarantineIntent(fs, path, faults); err != nil {
					return snapshot{}, 0, nil, err
				}
			default:
				return snapshot{}, 0, nil, loadErr
			}
		}
	} else if _, err := lstat(fs, path); errors.Is(err, os.ErrNotExist) && artifacts.rebuildStamp != "" {
		// The store file is gone but an aside and its complete custody remain: a
		// completed quarantine whose replacement vanished. Rebuild the
		// replacement from the custody the newest quarantine took — the pair with
		// the highest validated epoch, not the newest filename timestamp.
		custodyPath := quarantineCustodyPath(path, artifacts.rebuildStamp)
		custody, err := readCustodyFile(fs, custodyPath, path)
		if err != nil {
			return snapshot{}, 0, nil, quarantineFailed(err)
		}
		if custody.QuarantineEpoch > epoch {
			epoch = custody.QuarantineEpoch
		}
		if signal == nil {
			signal = &QuarantineSignal{
				QuarantinedFile: custody.QuarantinedFile,
				CustodyFile:     custodyPath,
				QuarantineEpoch: custody.QuarantineEpoch,
			}
		}
		return replacementFromCustody(fs, path, custodyPath, custody, artifacts.allocatorFloor, epoch, signal, faults)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return snapshot{}, 0, nil, fmt.Errorf("hostops: stat store %s: %w", path, err)
	}

	state, loadErr := loadFS(fs, path)
	if loadErr == nil {
		if len(orphans) > 0 {
			for _, orphan := range orphans {
				if err := removeQuarantineArtifact(fs, orphan, faults); err != nil {
					return snapshot{}, 0, nil, err
				}
			}
			signal = artifacts.signalAfterRemoving(path, orphanStamps...)
		}
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
	custody, err := custodyFromStore(decoded, path, epoch+1, time.Now().UTC(), artifacts.allocatorFloor, artifacts.priorImports)
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
	replacement, err := replacementState(custody, artifacts.allocatorFloor)
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

// replacementFromCustody finishes a quarantine whose custody file is already on
// disk: it writes the replacement store derived from that custody, clears the
// pending intent the quarantine left (a no-op when there is none), and persists
// the epoch. Every crash window that lands between the custody write and the
// replacement's last durable write funnels through here, so all of them open the
// same state from the same evidence.
func replacementFromCustody(fs afero.Fs, path, custodyPath string, custody custodyFile, floor uint64, epoch uint64, signal *QuarantineSignal, faults storeFaults) (snapshot, uint64, *QuarantineSignal, error) {
	state, err := replacementState(custody, floor)
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
	if err := refuseNonRegularTarget(fs, aside); err != nil {
		return err
	}
	if err := fs.Rename(path, aside); err != nil {
		return fmt.Errorf("hostops: rename corrupt store aside: %w", err)
	}
	// The aside preserves the corrupt bytes, never a wider mode: the corrupt file
	// passed the loader's owner-only rule, but a hand-made file need not have, and
	// the quarantine is not the place to widen what the store refuses to serve.
	if err := fs.Chmod(aside, 0o600); err != nil {
		return fmt.Errorf("hostops: set aside mode: %w", err)
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

// removeQuarantineArtifact removes one quarantine file whose referent is gone —
// an orphaned custody file beside a cleanly serving store — and syncs the
// directory entry, so no later boot can be refused over it. A missing file is
// not an error: the caller may race an operator's own cleanup.
func removeQuarantineArtifact(fs afero.Fs, artifactPath string, faults storeFaults) error {
	if _, err := lstat(fs, artifactPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("hostops: stat quarantine artifact %s: %w", artifactPath, err)
	}
	if err := fs.Remove(artifactPath); err != nil {
		return fmt.Errorf("hostops: remove quarantine artifact %s: %w", artifactPath, err)
	}
	sync := syncDirFS
	if faults.syncDir != nil {
		sync = faults.syncDir
	}
	if err := sync(fs, filepath.Dir(artifactPath)); err != nil {
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
	info, err := lstat(fs, intentPath)
	if errors.Is(err, os.ErrNotExist) {
		return quarantineIntent{}, false, nil
	}
	if err != nil {
		return quarantineIntent{}, false, fmt.Errorf("hostops: stat quarantine intent %s: %w", intentPath, err)
	}
	if err := rejectNonStoreFileKind(intentPath, info); err != nil {
		return quarantineIntent{}, false, quarantineFailed(err)
	}
	if perm := info.Mode().Perm(); !ownerOnly(perm) {
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("%w: %s has mode %04o",
			ErrStoreReadableBeyondOwner, intentPath, perm))
	}
	raw, err := afero.ReadFile(fs, intentPath)
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
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("quarantine intent %s carries trailing data", intentPath))
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
	// The binding is exact: the intent's aside and custody names must be the
	// ones this store's quarantine derives from one boot timestamp. An intent
	// that names another store's artifact — or mismatched stamps — is refused
	// rather than followed.
	stamp, ok := strings.CutPrefix(intent.AsideFile, path+quarantineAsideInfix)
	if !ok || stamp == "" || strings.ContainsRune(stamp, filepath.Separator) {
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("quarantine intent %s names the aside file %q, which is not this store's quarantine aside",
			intentPath, intent.AsideFile))
	}
	if want := quarantineCustodyPath(path, stamp); intent.CustodyFile != want {
		return quarantineIntent{}, false, quarantineFailed(fmt.Errorf("quarantine intent %s names the custody file %q, want %q",
			intentPath, intent.CustodyFile, want))
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
	if err := refuseNonRegularTarget(fs, path); err != nil {
		return err
	}
	if err := fs.Rename(tempPath, path); err != nil {
		return fmt.Errorf("hostops: rename quarantine file: %w", err)
	}
	renamed = true
	if err := sync(fs, dir); err != nil {
		return &postRenameError{err: fmt.Errorf("hostops: sync directory behind %s: %w", filepath.Base(path), err)}
	}
	return nil
}

// refuseNonRegularTarget refuses a rename that would replace something other
// than a regular file (or nothing at all): the rename replaces the link itself,
// not what it points at, so an artifact path swapped for a symlink must not be
// silently clobbered — and a directory or fifo must not be replaced either.
func refuseNonRegularTarget(fs afero.Fs, path string) error {
	info, err := lstat(fs, path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("hostops: stat %s: %w", path, err)
	}
	return rejectNonStoreFileKind(path, info)
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
