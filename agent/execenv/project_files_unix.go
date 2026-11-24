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
			matcher, _, ierr := readIgnoreRules(ctx, fsys, canonical, ignoreRel, budget, cache)
			if ierr != nil {
				if _, ok := errors.AsType[*globBudgetError](ierr); ok {
					return ProjectFilesResult{Entries: out, Truncated: true}, nil
				}
				if cerr := ctx.Err(); cerr != nil {
					return classifyWalkCtxErr(out, cerr)
				}
				if errors.Is(ierr, context.DeadlineExceeded) {
					return ProjectFilesResult{Entries: out, Truncated: true}, nil
				}
				return ProjectFilesResult{}, ierr
			}
			if matcher != nil {
				ignores.dirs = append(ignores.dirs, ignoreDir{rel: dir, matcher: matcher})
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
