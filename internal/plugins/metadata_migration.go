package plugins

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/envvars/userdirs"
)

// migrationBundledLockWait bounds the migration's wait for the bundled lock.
// Failing it is a retryable recovery state, not a reason to block a plugin
// operation: the old scan stays in force.
const migrationBundledLockWait = 2 * time.Second

var migrationMove = moveNoReplace

// maxGitPointerBytes bounds the .git pointer file the migration reads. A
// larger file is not a pointer this migration can classify, so it is left in
// place and the store stays unverified rather than reading it whole.
const maxGitPointerBytes = 64 << 10

// migrateManagedGitMetadata moves only entries named .git out of the managed
// content roots. It never moves a pointer's target, an ordinary symlink
// target, or any other content path. A .git file whose gitdir target lies
// inside a managed root without a .git component is left in place and
// reported, which keeps the store unverified and the old scan active.
func (m *Manager) migrateManagedGitMetadata() []string {
	problems := m.reclaimPendingArchiveMarkers()
	for _, root := range m.managedRoots() {
		entries, readProblems := collectGitEntries(root.path)
		problems = append(problems, readProblems...)
		sort.Slice(entries, func(i, j int) bool {
			return pathDepth(entries[i]) > pathDepth(entries[j])
		})
		var moved []string
		for _, entry := range entries {
			if isUnderMoved(entry, moved) {
				continue
			}
			info, err := os.Lstat(entry)
			if err != nil {
				problems = append(problems, "left a .git entry in place at "+rootRelative(root.path, entry)+": "+err.Error())
				continue
			}
			// A directory is moved only when it is provably Git administration.
			// Anything else named .git may be a marketplace or plugin namespace
			// (including a retained legacy name that nests one deeper), so it
			// stays and the store remains unverified.
			if info.IsDir() && !looksLikeGitAdministration(entry) {
				if rel, relErr := filepath.Rel(root.path, entry); relErr == nil {
					marker := m.managedGitJournalPath(root.name, rel)
					if recorded, readErr := os.ReadFile(marker); readErr == nil && string(recorded) == entry {
						// A complete copy plus a journal marker naming this exact
						// source proves the interrupted archive owns it, so finish
						// the removal and clear the journal.
						if err := os.RemoveAll(entry); err != nil {
							problems = append(problems, "removing "+rootRelative(root.path, entry)+": "+err.Error())
							continue
						}
						if err := os.Remove(marker); err != nil {
							problems = append(problems, "removing an archive marker: "+err.Error())
						}
						moved = append(moved, entry)
						continue
					}
				}
				problems = append(problems, "left a namespace directory named .git in place at "+rootRelative(root.path, entry))
				continue
			}
			if preservesManagedNamespace(root, entry) {
				problems = append(problems, "left a namespace directory named .git in place at "+rootRelative(root.path, entry))
				continue
			}
			if m.unsafeGitEntry(entry) {
				problems = append(problems, "left an ancestor-pointer .git entry in place at "+rootRelative(root.path, entry))
				continue
			}
			rel, err := filepath.Rel(root.path, entry)
			if err != nil {
				problems = append(problems, "could not locate a .git entry in "+root.name)
				continue
			}
			marker := m.managedGitJournalPath(root.name, rel)
			if err := migrationMove(entry, m.managedGitArchiveDir(root.name, rel), marker); err != nil {
				problems = append(problems, "moving "+rootRelative(root.path, entry)+": "+err.Error())
				continue
			}
			moved = append(moved, entry)
		}
	}
	return problems
}

// reclaimPendingArchiveMarkers removes journal markers whose recorded source
// no longer exists, so a later publication at that path is not treated as an
// interrupted cleanup. It walks only the journal tree, never archive payloads:
// an archived ref such as refs/heads/topic.pending records a commit id, not a
// source path.
func (m *Manager) reclaimPendingArchiveMarkers() []string {
	var problems []string
	journal := filepath.Join(m.Root, managedGitArchiveDirName, managedGitJournalDirName)
	if _, err := os.Stat(journal); os.IsNotExist(err) {
		return nil
	}
	err := filepath.Walk(journal, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), managedGitArchivePendingSuffix) {
			return nil
		}
		recorded, readErr := os.ReadFile(path)
		if readErr != nil {
			problems = append(problems, "reading an archive marker: "+readErr.Error())
			return nil //nolint:nilerr // the read failure is reported as a migration problem, not a walk error
		}
		if _, err := os.Lstat(string(recorded)); err == nil || !os.IsNotExist(err) {
			return nil
		}
		if err := os.Remove(path); err != nil {
			problems = append(problems, "removing an archive marker: "+err.Error())
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		problems = append(problems, "reading the metadata journal: "+err.Error())
	}
	return problems
}

// preservesManagedNamespace reports whether entry names a cache marketplace or
// plugin directory rather than Git metadata inside a revision. A marketplace
// or plugin name is a valid path component, so `.git` can be a real namespace
// there; archiving it would remove published content a registry entry and a
// saved session still name. Such a store stays unverified instead.
func preservesManagedNamespace(root managedRoot, entry string) bool {
	if root.name != cacheDirName {
		return false
	}
	rel, err := filepath.Rel(root.path, entry)
	if err != nil {
		return true
	}
	return strings.Count(rel, string(filepath.Separator)) <= 1
}

// collectGitEntries returns every entry named .git under root without following
// symlinks or descending into a .git entry. Read failures are reported, not
// ignored: a tree whose metadata cannot be inventoried is not clean.
func collectGitEntries(root string) ([]string, []string) {
	var entries, problems []string
	var walk func(dir string)
	walk = func(dir string) {
		items, err := os.ReadDir(dir)
		if err != nil {
			if !os.IsNotExist(err) {
				problems = append(problems, fmt.Sprintf("reading a managed content directory: %v", err))
			}
			return
		}
		for _, item := range items {
			path := filepath.Join(dir, item.Name())
			if isGitName(item.Name()) {
				entries = append(entries, path)
				continue
			}
			// Info rather than IsDir: a filesystem that reports DT_UNKNOWN makes
			// IsDir false for a real directory, which would hide a nested .git
			// and let a store be called clean without inventorying it.
			info, err := item.Info()
			if err != nil {
				problems = append(problems, fmt.Sprintf("reading a managed content entry: %v", err))
				continue
			}
			if info.IsDir() {
				walk(path)
			}
		}
	}
	walk(root)
	return entries, problems
}

// unsafeGitEntry reports whether a .git file or symlink names a target inside
// a managed content root at a path that is not itself Git metadata. Such a
// target may be an ancestor or mixed-content directory, so the entry is left
// in place and the store stays unverified rather than moving content.
func (m *Manager) unsafeGitEntry(entry string) bool {
	info, err := os.Lstat(entry)
	if err != nil {
		return true
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(entry)
		if err != nil {
			return true
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(entry), target)
		}
		if m.targetIsMixedManagedContent(filepath.Clean(target)) {
			return true
		}
		resolved, err := filepath.EvalSymlinks(entry)
		return err == nil && m.targetIsMixedManagedContent(resolved)
	}
	if !info.Mode().IsRegular() {
		return false
	}
	if info.Size() > maxGitPointerBytes {
		return true
	}
	file, err := os.Open(entry)
	if err != nil {
		return true
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxGitPointerBytes+1))
	if err != nil || len(data) > maxGitPointerBytes {
		return true
	}
	line, _, _ := strings.Cut(string(data), "\n")
	const prefix = "gitdir:"
	target, ok := strings.CutPrefix(line, prefix)
	if !ok {
		return false
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(entry), target)
	}
	target = filepath.Clean(target)
	return m.targetIsMixedManagedContent(target)
}

// targetIsMixedManagedContent reports whether target lies under any managed
// content root at a path that is not itself inside a .git entry.
func (m *Manager) targetIsMixedManagedContent(target string) bool {
	targets := []string{normalizeDataVolumeAlias(target)}
	if resolved, err := filepath.EvalSymlinks(target); err == nil && resolved != target {
		targets = append(targets, normalizeDataVolumeAlias(resolved))
	}
	for _, root := range m.managedRoots() {
		for _, candidate := range targets {
			if rel, ok := underRootByIdentity(candidate, root.path); ok && !hasGitComponent(rel) {
				return true
			}
		}
		spellings := []string{normalizeDataVolumeAlias(root.path)}
		if resolved, err := filepath.EvalSymlinks(root.path); err == nil && resolved != root.path {
			spellings = append(spellings, normalizeDataVolumeAlias(resolved))
		}
		for _, spelling := range spellings {
			for _, candidate := range targets {
				rel, err := filepath.Rel(spelling, candidate)
				if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
					continue
				}
				if !hasGitComponent(rel) {
					return true
				}
			}
		}
	}
	return false
}

// underRootByIdentity reports whether target is inside root by file identity,
// returning target's path relative to the matching ancestor. It catches APFS
// case or Unicode spellings that EvalSymlinks preserves and filepath.Rel cannot
// match; a missing target falls back to the lexical checks.
func underRootByIdentity(target, root string) (string, bool) {
	for path := filepath.Clean(target); ; path = filepath.Dir(path) {
		if samePathIdentity(path, root) {
			rel, _ := filepath.Rel(path, target)
			return rel, true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", false
		}
	}
}

func samePathIdentity(a, b string) bool {
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

// normalizeDataVolumeAlias collapses macOS's APFS data-volume firmlink spelling
// onto the plain one. EvalSymlinks does not collapse a firmlink, so a store
// reached by one spelling would otherwise look unrelated to a target named by
// the other.
func normalizeDataVolumeAlias(path string) string {
	return userdirs.StripDataVolumeAlias(path)
}

func pathDepth(path string) int {
	return strings.Count(filepath.Clean(path), string(filepath.Separator))
}

func isUnderMoved(path string, moved []string) bool {
	for _, parent := range moved {
		if _, under := pathUnder(parent, path); under && path != parent {
			return true
		}
		if path == parent {
			return true
		}
	}
	return false
}

func rootRelative(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return rel
}

// auditManagedContentRoots re-walks both entire roots after the move. Clean
// means no .git name and no read failure remains.
func (m *Manager) auditManagedContentRoots() (bool, []string) {
	var problems []string
	for _, root := range m.managedRoots() {
		entries, readProblems := collectGitEntries(root.path)
		problems = append(problems, readProblems...)
		for _, entry := range entries {
			problems = append(problems, "Git metadata remains at "+rootRelative(root.path, entry))
		}
	}
	return len(problems) == 0, problems
}
