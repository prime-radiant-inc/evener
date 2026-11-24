//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/identifier"
)

func walkProjectFiles(ctx context.Context, root string, opts ProjectFilesOptions, budget *GlobBudget) (ProjectFilesResult, error) {
	if strings.TrimSpace(root) == "" {
		return ProjectFilesResult{}, fs.ErrInvalid
	}
	absRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return ProjectFilesResult{}, err
	}
	canonical, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return ProjectFilesResult{}, err
	}
	canonical = filepath.Clean(canonical)

	maskedRoots := buildProjectFilesMaskedRoots(opts.HomeDir)
	if isProjectPathMasked(absRoot, maskedRoots) || isProjectPathMasked(canonical, maskedRoots) {
		return ProjectFilesResult{}, nil
	}

	baseFd, err := unix.Open(canonical, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return ProjectFilesResult{}, err
	}
	defer func() { _ = unix.Close(baseFd) }()

	canonical = filepath.Clean(browseBasePath(baseFd, canonical))
	if isProjectPathMasked(canonical, maskedRoots) {
		return ProjectFilesResult{}, nil
	}

	var cache *ignoreMatcherCache
	if opts.IgnoreCache != nil {
		cache = opts.IgnoreCache.cache
	}

	fsys := cancelFS{
		ctx: ctx,
		fsys: &secureDirFS{
			baseFd:   baseFd,
			basePath: canonical,
			budget:   budget,
			ctx:      ctx,
		},
	}

	queue := []string{"."}
	ignores := &ignoreSet{}
	var out []ProjectFileEntry
	if truncated, aerr := loadAncestorProjectIgnores(ctx, canonical, maskedRoots, budget, cache, ignores); aerr != nil {
		return ProjectFilesResult{}, aerr
	} else if truncated {
		return ProjectFilesResult{Entries: out, Truncated: true}, nil
	}

	for head := 0; head < len(queue); head++ {
		if cerr := ctx.Err(); cerr != nil {
			return classifyWalkCtxErr(out, cerr)
		}
		if berr := budget.listing(true); berr != nil {
			return ProjectFilesResult{Entries: out, Truncated: true}, nil
		}
		dir := queue[head]
		queue[head] = ""

		entries, rerr := fs.ReadDir(fsys, dir)
		if rerr != nil {
			if _, ok := errors.AsType[*globBudgetError](rerr); ok {
				return ProjectFilesResult{Entries: out, Truncated: true}, nil
			}
			if cerr := ctx.Err(); cerr != nil {
				return classifyWalkCtxErr(out, cerr)
			}
			if errors.Is(rerr, context.DeadlineExceeded) {
				return ProjectFilesResult{Entries: out, Truncated: true}, nil
			}
			if errors.Is(rerr, context.Canceled) {
				return ProjectFilesResult{}, rerr
			}
			if dir == "." {
				return ProjectFilesResult{}, rerr
			}
			continue
		}

		// Load this directory's .gitignore before filtering any of its children.
		for _, d := range entries {
			if d.Name() != ".gitignore" {
				continue
			}
			if d.Type()&fs.ModeSymlink != 0 || d.IsDir() {
				break
			}
			ignoreRel := ".gitignore"
			if dir != "." {
				ignoreRel = dir + "/.gitignore"
			}
			ignoreAbs := filepath.Join(canonical, filepath.FromSlash(ignoreRel))
			if isProjectPathMasked(ignoreAbs, maskedRoots) {
				break
			}
			matcher, rules, _, ierr := readIgnoreRules(ctx, fsys, canonical, ignoreRel, budget, cache)
			if ierr != nil {
				res, handled, cerr := classifyIgnoreReadErr(ctx, out, ierr)
				if handled {
					return res, nil
				}
				return ProjectFilesResult{}, cerr
			}
			if matcher != nil {
				ignores.dirs = append(ignores.dirs, ignoreDir{rel: dir, matcher: matcher, rules: rules})
			}
			break
		}

		for _, d := range entries {
			if cerr := ctx.Err(); cerr != nil {
				return classifyWalkCtxErr(out, cerr)
			}
			name := d.Name()
			if name == "" || name == "." || name == ".." {
				continue
			}
			if d.Type()&fs.ModeSymlink != 0 {
				continue
			}
			isDir := d.IsDir()
			if !isDir && !d.Type().IsRegular() {
				continue
			}
			if name == ".git" {
				continue
			}
			if !opts.IncludeDotfiles && strings.HasPrefix(name, ".") {
				continue
			}
			rel := name
			if dir != "." {
				rel = dir + "/" + name
			}
			abs := filepath.Join(canonical, filepath.FromSlash(rel))
			if isProjectPathMasked(abs, maskedRoots) {
				continue
			}
			if ignores.matches(rel, isDir) {
				continue
			}
			if len(out) >= opts.Limits.MaxPaths {
				return ProjectFilesResult{Entries: out, Truncated: true}, nil
			}
			out = append(out, ProjectFileEntry{Path: rel, IsDir: isDir})
			if isDir {
				queue = append(queue, rel)
			}
		}
	}

	return ProjectFilesResult{Entries: out}, nil
}

func classifyIgnoreReadErr(ctx context.Context, out []ProjectFileEntry, err error) (ProjectFilesResult, bool, error) {
	if _, ok := errors.AsType[*globBudgetError](err); ok {
		return ProjectFilesResult{Entries: out, Truncated: true}, true, nil
	}
	if cerr := ctx.Err(); cerr != nil {
		res, rerr := classifyWalkCtxErr(out, cerr)
		return res, rerr == nil, rerr
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ProjectFilesResult{Entries: out, Truncated: true}, true, nil
	}
	return ProjectFilesResult{}, false, err
}

// loadAncestorProjectIgnores loads .gitignore layers from the enclosing Git
// worktree root down through each strict parent directory of canonical (never
// canonical itself, which the BFS loop reads at ".").
func loadAncestorProjectIgnores(
	ctx context.Context,
	canonical string,
	maskedRoots []string,
	budget *GlobBudget,
	cache *ignoreMatcherCache,
	ignores *ignoreSet,
) (bool, error) {
	gitRoot, ok := structuralWorktreeRoot(canonical)
	if !ok || gitRoot == "" {
		return false, nil
	}
	if gitFd, err := unix.Open(gitRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0); err == nil {
		gitRoot = filepath.Clean(browseBasePath(gitFd, gitRoot))
		_ = unix.Close(gitFd)
	}
	relFromGit, err := filepath.Rel(gitRoot, canonical)
	if err != nil || relFromGit == "." || relFromGit == ".." || strings.HasPrefix(relFromGit, ".."+string(filepath.Separator)) {
		return false, nil //nolint:nilerr // non-relative gitRoot means canonical is not strictly beneath it
	}
	ancestors := DirsFromRootToCwd(gitRoot, canonical)
	for _, ancDir := range ancestors[:len(ancestors)-1] {
		if cerr := ctx.Err(); cerr != nil {
			res, rerr := classifyWalkCtxErr(nil, cerr)
			return res.Truncated, rerr
		}
		ancFd, oerr := unix.Open(ancDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if oerr != nil {
			continue
		}
		ancCanonical := filepath.Clean(browseBasePath(ancFd, ancDir))
		rel, rerr := filepath.Rel(ancCanonical, canonical)
		if rerr != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			_ = unix.Close(ancFd)
			continue
		}
		ignoreAbs := filepath.Join(ancCanonical, ".gitignore")
		if isProjectPathMasked(ancCanonical, maskedRoots) || isProjectPathMasked(ignoreAbs, maskedRoots) {
			_ = unix.Close(ancFd)
			continue
		}
		ancFS := cancelFS{
			ctx: ctx,
			fsys: &secureDirFS{
				baseFd:   ancFd,
				basePath: ancCanonical,
				budget:   budget,
				ctx:      ctx,
			},
		}
		matcher, rules, _, ierr := readIgnoreRules(ctx, ancFS, ancCanonical, ".gitignore", budget, cache)
		_ = unix.Close(ancFd)
		if ierr != nil {
			res, handled, cerr := classifyIgnoreReadErr(ctx, nil, ierr)
			if handled {
				return res.Truncated, nil
			}
			return false, cerr
		}
		if matcher != nil {
			ignores.dirs = append(ignores.dirs, ignoreDir{
				rel:            ".",
				ancestorPrefix: filepath.ToSlash(rel),
				matcher:        matcher,
				rules:          rules,
			})
		}
	}
	return false, nil
}

func classifyWalkCtxErr(out []ProjectFileEntry, err error) (ProjectFilesResult, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return ProjectFilesResult{Entries: out, Truncated: true}, nil
	}
	return ProjectFilesResult{}, err
}

func buildProjectFilesMaskedRoots(home string) []string {
	home = strings.TrimSpace(home)
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil && filepath.IsAbs(h) {
			home = h
		}
	}

	var homeCandidates []string
	if filepath.IsAbs(home) {
		cleanHome := filepath.Clean(home)
		homeCandidates = appendUniqueString(homeCandidates, cleanHome)
		if evalHome, err := filepath.EvalSymlinks(cleanHome); err == nil {
			homeCandidates = appendUniqueString(homeCandidates, filepath.Clean(evalHome))
		}
		if fd, err := unix.Open(cleanHome, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0); err == nil {
			if realHome := browseBasePath(fd, cleanHome); realHome != "" {
				homeCandidates = appendUniqueString(homeCandidates, filepath.Clean(realHome))
			}
			_ = unix.Close(fd)
		}
	}

	var masked []string
	addDenylistFor := func(h string) {
		for _, entry := range sandbox.DefaultDenylist(h) {
			if !filepath.IsAbs(entry) {
				continue
			}
			cleanEntry := sandbox.StripDataVolumeAlias(filepath.Clean(entry))
			masked = appendUniqueString(masked, cleanEntry)
			if resolved, err := filepath.EvalSymlinks(entry); err == nil {
				masked = appendUniqueString(masked, sandbox.StripDataVolumeAlias(filepath.Clean(resolved)))
			}
		}
	}

	if len(homeCandidates) == 0 {
		addDenylistFor("")
	} else {
		for _, h := range homeCandidates {
			addDenylistFor(h)
		}
	}
	return masked
}

func appendUniqueString(dst []string, v string) []string {
	if v == "" || slices.Contains(dst, v) {
		return dst
	}
	return append(dst, v)
}

func isProjectPathMasked(abs string, maskedRoots []string) bool {
	stripped := sandbox.StripDataVolumeAlias(filepath.Clean(abs))
	for _, m := range maskedRoots {
		if stripped == m || identifier.PathUnder(stripped, m) {
			return true
		}
	}
	return false
}
