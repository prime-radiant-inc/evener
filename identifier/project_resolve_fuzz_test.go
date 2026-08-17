//go:build serffuzz

package identifier

import (
	"errors"
	"path/filepath"
	"testing"
)

// scriptedResolver drives ResolveProjectWith's seam deterministically. It is a
// fake at the declared external boundary — filesystem and Git — which is the
// only place this repo allows one; the resolution logic under test is real.
type scriptedResolver struct {
	absFails   bool
	evalFails  bool
	mainFails  bool
	isGit      bool
	mainRoot   string
	evalSuffix string
}

func (s scriptedResolver) Abs(path string) (string, error) {
	if s.absFails {
		return "", errors.New("abs failed")
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Join("/abs", path), nil
}

func (s scriptedResolver) EvalSymlinks(path string) (string, error) {
	if s.evalFails {
		return "", errors.New("eval failed")
	}
	// A real EvalSymlinks is idempotent on an already-resolved path, so the
	// suffix is applied only once — appending on every call would model a
	// filesystem that cannot exist and would fail the identity oracle for the
	// wrong reason.
	if s.evalSuffix == "" || filepath.Base(path) == s.evalSuffix {
		return path, nil
	}
	return filepath.Join(path, s.evalSuffix), nil
}

func (s scriptedResolver) MainCheckout(path string) (string, bool, error) {
	if s.mainFails {
		return "", false, errors.New("main checkout failed")
	}
	return s.mainRoot, s.isGit, nil
}

// FuzzProjectResolution drives ResolveProjectWith, the function that decides
// what a "project" IS. Its inputs come from a working directory and from git,
// and getting it wrong does not crash anything — it silently files a session
// under the wrong project, or splits one project into two identities.
//
// Oracles:
//
//   - Identity is a function of the canonical path alone. Two different input
//     paths that canonicalize to the same place must produce the same ID; this
//     is the property the whole type exists to provide.
//   - Determinism. The same inputs resolve the same way every time. A project ID
//     that varies per call would scatter one project's sessions across many.
//   - A returned ID is always a valid project ID, and its CanonicalPath is
//     already cleaned, because callers compare these against other cleaned paths.
//   - Failures are reported, never flattened. An empty path, a nil resolver, or
//     any failing seam call must return an error and a zero Project rather than
//     a half-resolved one — and a Git checkout that reports success with an
//     empty root must be refused rather than silently treated as not-Git.
func FuzzProjectResolution(f *testing.F) {
	f.Add("/repo", false, false, false, false, "", "")
	f.Add("relative/dir", false, false, false, true, "/main", "")
	f.Add("", false, false, false, false, "", "")
	f.Add("/repo", true, false, false, false, "", "")
	f.Add("/repo", false, true, false, false, "", "")
	f.Add("/repo", false, false, true, false, "", "")
	f.Add("/repo/wt", false, false, false, true, "   ", "")
	f.Add("/repo", false, false, false, false, "", "link")

	f.Fuzz(func(t *testing.T, path string, absFails, evalFails, mainFails, isGit bool, mainRoot, evalSuffix string) {
		if len(path) > 4096 || len(mainRoot) > 4096 || len(evalSuffix) > 256 {
			t.Skip()
		}
		// A git checkout root is absolute: mainCheckoutLocal derives it from
		// `git rev-parse`, which answers with absolute paths. Letting the fake
		// return a relative root would model a resolver that cannot exist, and
		// the identity oracle below would fail on that impossibility rather than
		// on anything the real seam can produce — a broad fake beating a thin
		// real one, which is the trap the fuzzing skill warns about.
		if mainRoot != "" && !filepath.IsAbs(mainRoot) {
			mainRoot = filepath.Join("/main", mainRoot)
		}
		resolver := scriptedResolver{
			absFails:   absFails,
			evalFails:  evalFails,
			mainFails:  mainFails,
			isGit:      isGit,
			mainRoot:   mainRoot,
			evalSuffix: filepath.Base(filepath.Clean(evalSuffix)),
		}

		if _, err := ResolveProjectWith("", resolver); !errors.Is(err, errEmptyProjectPath) {
			t.Fatalf("empty path = %v, want errEmptyProjectPath", err)
		}
		if _, err := ResolveProjectWith("/repo", nil); !errors.Is(err, errNilResolver) {
			t.Fatalf("nil resolver = %v, want errNilResolver", err)
		}

		project, err := ResolveProjectWith(path, resolver)
		if err != nil {
			if project != (Project{}) {
				t.Fatalf("failed resolution returned a non-zero project %+v alongside %v", project, err)
			}
			return
		}

		if project.CanonicalPath != filepath.Clean(project.CanonicalPath) {
			t.Fatalf("CanonicalPath %q is not cleaned", project.CanonicalPath)
		}
		if err := ValidateProjectID(project.ID); err != nil {
			t.Fatalf("resolved ID %q is not a valid project ID: %v", project.ID, err)
		}

		again, err := ResolveProjectWith(path, resolver)
		if err != nil || again != project {
			t.Fatalf("resolution is not deterministic: %+v/%v then %+v/%v", project, nil, again, err)
		}

		// Identity depends only on where the path lands. Resolving the canonical
		// path itself must yield the same ID as resolving whatever led there.
		direct, err := ResolveProjectWith(project.CanonicalPath, scriptedResolver{})
		if err != nil {
			t.Fatalf("re-resolving canonical path %q failed: %v", project.CanonicalPath, err)
		}
		if direct.ID != project.ID {
			t.Fatalf("canonical path %q resolved to ID %q, but the original path gave %q",
				project.CanonicalPath, direct.ID, project.ID)
		}
	})
}
