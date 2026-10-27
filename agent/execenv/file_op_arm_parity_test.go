//go:build linux || darwin

package execenv

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// Every file operation chooses one of three arms by hand: the sandboxed layer,
// the granted session-scratch layer, or the plain filesystem. The arms differ on
// purpose (reads skip the dead-anchor check, an edit checks writability before
// it reads, a raw read resolves through the write path on the plain arm). This
// table pins the typed outcome of each operation on each arm so a refactor of
// the dispatch cannot drift an arm's behavior unnoticed.

type armOutcome int

const (
	// outcomeAllowed: the operation succeeded.
	outcomeAllowed armOutcome = iota
	// outcomeDenied: a typed *sandbox.DeniedError.
	outcomeDenied
	// outcomeDeadAnchor: ErrWorkingDirectoryUnavailable, the named recovery.
	outcomeDeadAnchor
	// outcomeFailed: any other error (a missing file, a refused symlink hop).
	outcomeFailed
)

func classifyArm(err error) armOutcome {
	if err == nil {
		return outcomeAllowed
	}
	if _, ok := errors.AsType[*sandbox.DeniedError](err); ok {
		return outcomeDenied
	}
	if errors.Is(err, ErrWorkingDirectoryUnavailable) {
		return outcomeDeadAnchor
	}
	return outcomeFailed
}

type armOp struct {
	name string
	run  func(env *LocalExecutionEnvironment, path string) error
}

// armOps drive the eight dispatch sites through their public entries.
var armOps = []armOp{
	{"read", func(env *LocalExecutionEnvironment, p string) error {
		_, _, err := env.ReadFileAndBytesCtx(context.Background(), p, nil, nil)
		return err
	}},
	{"write", func(env *LocalExecutionEnvironment, p string) error {
		_, err := env.WriteFile(p, "new")
		return err
	}},
	{"edit", func(env *LocalExecutionEnvironment, p string) error {
		_, err := env.EditFileWithCtx(context.Background(), p, "old", "new", false, nil)
		return err
	}},
	{"rawRead", func(env *LocalExecutionEnvironment, p string) error {
		_, err := env.ReadFileRawCtx(context.Background(), p)
		return err
	}},
	{"rawWrite", func(env *LocalExecutionEnvironment, p string) error {
		return env.WriteFileRaw(p, []byte("new"), 0o644)
	}},
	{"remove", func(env *LocalExecutionEnvironment, p string) error {
		return env.RemovePath(p)
	}},
	{"rename", func(env *LocalExecutionEnvironment, p string) error {
		return env.RenamePath(p, p+".renamed")
	}},
}

// armScenario builds a fresh environment for one cell and returns the path the
// operation targets. Each cell gets its own environment so a mutation in one
// cell cannot change another.
type armScenario struct {
	name  string
	build func(t *testing.T) (env *LocalExecutionEnvironment, target string)
	want  map[string]armOutcome
}

func writeArmFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func allArmOps(o armOutcome) map[string]armOutcome {
	m := map[string]armOutcome{}
	for _, op := range armOps {
		m[op.name] = o
	}
	return m
}

func armWith(base map[string]armOutcome, override map[string]armOutcome) map[string]armOutcome {
	m := maps.Clone(base)
	maps.Copy(m, override)
	return m
}

func sandboxedArm(mode sandbox.Mode) func(t *testing.T) (*LocalExecutionEnvironment, string) {
	return func(t *testing.T) (*LocalExecutionEnvironment, string) {
		env, _, worktree := sandboxedEnv(t, mode)
		writeArmFixture(t, filepath.Join(worktree, "target.txt"))
		return env, "target.txt"
	}
}

// scratchArm is an unsandboxed environment whose session scratch has been
// allocated; targets beneath the scratch route through the granted layer.
func scratchArm(t *testing.T) (*LocalExecutionEnvironment, string) {
	t.Helper()
	env := NewLocalExecutionEnvironment(realTempDir(t))
	env.sandboxTmpBase = realTempDir(t)
	env.SetScratchIdentity("PARITY", "PARITY")
	t.Cleanup(func() { _ = env.EndSessionScratch() })
	if _, err := env.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(env.SessionScratchDir(), "target.txt")
	writeArmFixture(t, target)
	return env, target
}

func TestFileOpArmParity(t *testing.T) {
	t.Parallel()
	scenarios := []armScenario{
		{
			name: "off",
			build: func(t *testing.T) (*LocalExecutionEnvironment, string) {
				root := realTempDir(t)
				writeArmFixture(t, filepath.Join(root, "target.txt"))
				return NewLocalExecutionEnvironment(root), "target.txt"
			},
			want: allArmOps(outcomeAllowed),
		},
		{
			// Mutations name the dead working directory; reads and the raw read
			// do not check for it and just fail on the missing file.
			name: "off, working directory deleted",
			build: func(t *testing.T) (*LocalExecutionEnvironment, string) {
				root := realTempDir(t)
				env := NewLocalExecutionEnvironment(root)
				if err := os.RemoveAll(root); err != nil {
					t.Fatal(err)
				}
				return env, "target.txt"
			},
			want: armWith(allArmOps(outcomeDeadAnchor), map[string]armOutcome{
				"read": outcomeFailed, "rawRead": outcomeFailed,
			}),
		},
		{
			name:  "sandboxed read-only",
			build: sandboxedArm(sandbox.ModeReadOnly),
			want: armWith(allArmOps(outcomeDenied), map[string]armOutcome{
				"read": outcomeAllowed, "rawRead": outcomeAllowed,
			}),
		},
		{
			name:  "sandboxed writable",
			build: sandboxedArm(sandbox.ModeWorkspaceWrite),
			want:  allArmOps(outcomeAllowed),
		},
		{
			// A target outside every writable root: writes are denied; reads of an
			// unmasked file stay allowed.
			name: "sandboxed writable, target outside the roots",
			build: func(t *testing.T) (*LocalExecutionEnvironment, string) {
				env, _, _ := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
				outside := filepath.Join(realTempDir(t), "target.txt")
				writeArmFixture(t, outside)
				return env, outside
			},
			want: armWith(allArmOps(outcomeDenied), map[string]armOutcome{
				"read": outcomeAllowed, "rawRead": outcomeAllowed,
			}),
		},
		{
			// The sandbox root is gone: mutations name the dead anchor; reads skip
			// that check and fail on the missing file.
			name: "sandboxed writable, root deleted",
			build: func(t *testing.T) (*LocalExecutionEnvironment, string) {
				env, _, worktree := sandboxedEnv(t, sandbox.ModeWorkspaceWrite)
				if err := os.RemoveAll(worktree); err != nil {
					t.Fatal(err)
				}
				return env, "target.txt"
			},
			want: armWith(allArmOps(outcomeDeadAnchor), map[string]armOutcome{
				"read": outcomeFailed, "rawRead": outcomeFailed,
			}),
		},
		{
			name:  "granted scratch",
			build: scratchArm,
			want:  allArmOps(outcomeAllowed),
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			t.Parallel()
			for _, op := range armOps {
				t.Run(op.name, func(t *testing.T) {
					env, target := sc.build(t)
					if got, want := classifyArm(op.run(env, target)), sc.want[op.name]; got != want {
						t.Fatalf("%s on %s arm: outcome %d, want %d", op.name, sc.name, got, want)
					}
				})
			}
		})
	}
}

// The granted scratch layer is fd-anchored and refuses a symlink hop out of
// the scratch root; the plain arm follows the same link. That difference is how
// the two arms are told apart from outside.
func TestFileOpArmParity_ScratchRefusesSymlinkHopPlainArmFollows(t *testing.T) {
	t.Parallel()
	outside := filepath.Join(realTempDir(t), "outside.txt")
	writeArmFixture(t, outside)

	plainRoot := realTempDir(t)
	if err := os.Symlink(outside, filepath.Join(plainRoot, "link.txt")); err != nil {
		t.Fatal(err)
	}
	plain := NewLocalExecutionEnvironment(plainRoot)

	scratchEnv, scratchTarget := scratchArm(t)
	link := filepath.Join(filepath.Dir(scratchTarget), "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	for _, op := range armOps {
		if op.name == "rename" || op.name == "remove" {
			// Both act on the link itself, not through it.
			continue
		}
		t.Run(op.name, func(t *testing.T) {
			writeArmFixture(t, outside)
			if got := classifyArm(op.run(plain, "link.txt")); got != outcomeAllowed {
				t.Fatalf("plain arm through a symlink: outcome %d, want allowed", got)
			}
			writeArmFixture(t, outside)
			if got := classifyArm(op.run(scratchEnv, link)); got != outcomeDenied {
				t.Fatalf("scratch arm through a symlink out of the scratch root: outcome %d, want denied", got)
			}
		})
	}
}

// A rename never crosses the session-scratch boundary in either direction.
func TestFileOpArmParity_RenameRefusesToCrossScratchBoundary(t *testing.T) {
	t.Parallel()
	env, scratchTarget := scratchArm(t)
	outsideDir := realTempDir(t)
	outside := filepath.Join(outsideDir, "file.txt")
	writeArmFixture(t, outside)

	if err := env.RenamePath(scratchTarget, filepath.Join(outsideDir, "moved.txt")); err == nil {
		t.Fatal("rename out of the scratch root was accepted")
	}
	if err := env.RenamePath(outside, filepath.Join(filepath.Dir(scratchTarget), "moved.txt")); err == nil {
		t.Fatal("rename into the scratch root was accepted")
	}
	for _, p := range []string{scratchTarget, outside} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("refused rename still moved %s: %v", p, err)
		}
	}
}
