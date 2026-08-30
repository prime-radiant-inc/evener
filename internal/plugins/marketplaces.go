package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	marketplaceRename          = os.Rename
	marketplaceAcquireLock     = acquireLock
	marketplaceStat            = os.Stat
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

func (m *Manager) loadMarketplaces() (Marketplaces, error) {
	data, err := marketplaceReadFile(m.marketplacesFile())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Marketplaces{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", m.marketplacesFile(), err)
	}
	var mk Marketplaces
	if err := json.Unmarshal(data, &mk); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", m.marketplacesFile(), err)
	}
	if mk == nil {
		mk = Marketplaces{}
	}
	return mk, nil
}

func (m *Manager) saveMarketplaces(mk Marketplaces) error {
	body, err := marketplaceMarshalIndent(mk, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling marketplaces: %w", err)
	}
	return marketplaceAtomicWriteFile(m.marketplacesFile(), append(body, '\n'), 0o644)
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
		return "", fmt.Errorf("unsupported marketplace source %q", src.Kind)
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
			return MarketplaceRef{}, err
		}
	}
	ref.InstallLocation = installLoc
	ref.LastUpdated = m.now().UTC()
	mk[name] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		return MarketplaceRef{}, err
	}
	return ref, nil
}

// AddMarketplace fetches src, reads its marketplace.json for the name (unless
// name is given), and records it. Returns the stored ref.
func (m *Manager) AddMarketplace(ctx context.Context, name string, src Source) (MarketplaceRef, error) {
	release, err := marketplaceAcquireLock(ctx, m.lockPath(), 30*time.Second)
	if err != nil {
		return MarketplaceRef{}, err
	}
	defer release()

	// Fetch into a staging dir first so a bad marketplace never half-registers.
	staging := m.marketplaceDir(".staging")
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
	if src.Kind != SourceDirectory {
		installLoc = m.marketplaceDir(name)
		_ = marketplaceRemoveAll(installLoc)
		if err := marketplaceRename(staging, installLoc); err != nil {
			_ = marketplaceRemoveAll(staging)
			return MarketplaceRef{}, fmt.Errorf("installing marketplace clone: %w", err)
		}
	} else {
		_ = marketplaceRemoveAll(staging)
	}

	mk, err := m.loadMarketplaces()
	if err != nil {
		if src.Kind != SourceDirectory {
			_ = marketplaceRemoveAll(installLoc)
		}
		return MarketplaceRef{}, err
	}
	ref := MarketplaceRef{Source: src, InstallLocation: installLoc, LastUpdated: m.now().UTC()}
	mk[name] = ref
	if err := m.saveMarketplaces(mk); err != nil {
		if src.Kind != SourceDirectory {
			_ = marketplaceRemoveAll(installLoc)
		}
		return MarketplaceRef{}, err
	}
	return ref, nil
}

func (m *Manager) ListMarketplaces() (Marketplaces, error) { return m.loadMarketplaces() }

func (m *Manager) RemoveMarketplace(name string) error {
	release, err := marketplaceAcquireLock(context.Background(), m.lockPath(), 30*time.Second)
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
		if err := marketplaceRemoveAll(m.marketplaceDir(name)); err != nil {
			_, _ = fmt.Fprintf(m.stderr(), "warning: removing marketplace clone %s: %v\n", m.marketplaceDir(name), err)
		}
	}
	delete(mk, name)
	return m.saveMarketplaces(mk)
}

// recloneMarketplace replaces a marketplace clone whose git pull failed. The
// fresh clone is fully downloaded into a staging dir before the existing clone
// is touched (rename old aside → rename new in → remove old), so the current
// clone — possibly wedged, but the only local copy — is never lost to a failed
// download; a failed reclone leaves it exactly as it was. The caller must hold
// m.lockPath(), which also serializes use of the shared staging/aside dirs.
func (m *Manager) recloneMarketplace(ctx context.Context, ref MarketplaceRef) error {
	staging := m.marketplaceDir(".staging")
	_ = marketplaceRemoveAll(staging)
	if _, err := m.fetchMarketplaceContainer(ctx, ref.Source, staging); err != nil {
		_ = marketplaceRemoveAll(staging)
		return err
	}
	old := m.marketplaceDir(".old")
	_ = marketplaceRemoveAll(old)
	if err := marketplaceRename(ref.InstallLocation, old); err != nil {
		_ = marketplaceRemoveAll(staging)
		return fmt.Errorf("moving old clone aside: %w", err)
	}
	if err := marketplaceRename(staging, ref.InstallLocation); err != nil {
		// Put the old clone back so InstallLocation keeps pointing at a
		// real directory; a restore failure is reported over the rename one.
		if restoreErr := marketplaceRename(old, ref.InstallLocation); restoreErr != nil {
			return fmt.Errorf("installing fresh clone failed (%v); restoring old clone: %w", err, restoreErr)
		}
		_ = marketplaceRemoveAll(staging)
		return fmt.Errorf("installing fresh clone: %w", err)
	}
	_ = marketplaceRemoveAll(old)
	return nil
}

func (m *Manager) RefreshMarketplace(ctx context.Context, name string) error {
	release, err := marketplaceAcquireLock(ctx, m.lockPath(), 30*time.Second)
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
				return err
			}
			ref.InstallLocation = installLoc
		} else if pullErr := marketplaceGitPull(ctx, ref.InstallLocation); pullErr != nil {
			// A failed pull can mean the clone is wedged — e.g. a stale
			// .git/index.lock stranded by a killed git — and a plain retry
			// would then fail the same way forever. Self-heal with a staged
			// reclone; on failure it leaves the existing clone untouched.
			if recloneErr := m.recloneMarketplace(ctx, ref); recloneErr != nil {
				return fmt.Errorf("refreshing marketplace %q: git pull failed (%v); staged reclone failed: %w", name, pullErr, recloneErr)
			}
		}
	}
	ref.LastUpdated = m.now().UTC()
	mk[name] = ref
	return m.saveMarketplaces(mk)
}
