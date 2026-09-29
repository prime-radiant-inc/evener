package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The pre-commit hook and its installer run for real here, inside a throwaway
// git repo, through real `git commit`. Most cases put a stand-in `biome` in
// each tree's node_modules/.bin: the hook's own job is routing staged files to
// the tree that owns them, re-staging, refusing, and chaining, and a stand-in
// that appends a marker line and records its cwd and arguments shows exactly
// which files each tree's Biome was handed. TestPreCommitHookFormatsWithTheRealFrontendBiome
// covers the real formatter when the frontend install is present.

const stubBiome = `#!/bin/sh
echo "$PWD $*" >> "$STUB_LOG"
for arg in "$@"; do
	case "$arg" in
	-*|format|check) ;;
	*) echo "// formatted" >> "$arg" ;;
	esac
done
`

func hookRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runIn(t, repo, "git", "init", "-q")
	runIn(t, repo, "git", "config", "user.email", "hook@example.com")
	runIn(t, repo, "git", "config", "user.name", "Hook Test")
	runIn(t, repo, "git", "config", "commit.gpgsign", "false")
	for _, name := range []string{"pre-commit", "install.sh"} {
		copyRepositoryFile(t, ".", repo, filepath.Join("scripts", "hooks", name), 0o755)
	}
	runIn(t, repo, "git", "config", "core.hooksPath", "scripts/hooks")
	return repo
}

func runIn(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	output, err := runInErr(dir, name, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return output
}

func runInErr(dir, name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	return string(output), err
}

func installStubBiome(t *testing.T, repo, tree, log string) {
	t.Helper()
	writeTestFile(t, filepath.Join(repo, tree, "node_modules", ".bin", "biome"), []byte(stubBiome), 0o755)
	t.Setenv("STUB_LOG", log)
}

func stageFile(t *testing.T, repo, path, content string) {
	t.Helper()
	writeTestFile(t, filepath.Join(repo, path), []byte(content), 0o644)
	runIn(t, repo, "git", "add", "--", path)
}

func committedContent(t *testing.T, repo, path string) string {
	t.Helper()
	return runIn(t, repo, "git", "show", "HEAD:"+path)
}

func TestPreCommitHookRoutesStagedFilesToTheirOwnTreesBiome(t *testing.T) {
	repo := hookRepo(t)
	log := filepath.Join(t.TempDir(), "biome.log")
	installStubBiome(t, repo, "mobile-native", log)
	installStubBiome(t, repo, "cmd/evener-hub/frontend", log)
	stageFile(t, repo, "mobile-native/src/a b.ts", "a\n")
	stageFile(t, repo, "mobile/src/m.tsx", "m\n")
	stageFile(t, repo, "cmd/evener-hub/frontend/src/w.ts", "w\n")
	stageFile(t, repo, "appwire-client/typescript/c.ts", "c\n")
	stageFile(t, repo, "docs/outside.ts", "outside\n")
	stageFile(t, repo, "mobile-native/src/notes.md", "notes\n")

	runIn(t, repo, "git", "commit", "-q", "-m", "x")

	for path, want := range map[string]string{
		"mobile-native/src/a b.ts":         "a\n// formatted\n",
		"mobile/src/m.tsx":                 "m\n// formatted\n",
		"cmd/evener-hub/frontend/src/w.ts": "w\n// formatted\n",
		"appwire-client/typescript/c.ts":   "c\n// formatted\n",
		"docs/outside.ts":                  "outside\n",
		"mobile-native/src/notes.md":       "notes\n",
	} {
		if got := committedContent(t, repo, path); got != want {
			t.Errorf("committed %s = %q, want %q", path, got, want)
		}
	}
	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(logged)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want one Biome run per tree, got %d:\n%s", len(lines), logged)
	}
	native, web := lines[0], lines[1]
	if !strings.Contains(native, "/mobile-native format --write ") {
		t.Errorf("mobile-native Biome not run from its own directory with format --write: %s", native)
	}
	if !strings.Contains(web, "/cmd/evener-hub/frontend check --write --linter-enabled=false ") {
		t.Errorf("frontend Biome not run from its own directory with check --write: %s", web)
	}
	for _, want := range []string{"mobile-native/src/a b.ts", "mobile/src/m.tsx"} {
		if !strings.Contains(native, want) {
			t.Errorf("mobile-native Biome was not handed %s: %s", want, native)
		}
	}
	for _, want := range []string{"frontend/src/w.ts", "appwire-client/typescript/c.ts"} {
		if !strings.Contains(web, want) {
			t.Errorf("frontend Biome was not handed %s: %s", want, web)
		}
	}
	if strings.Contains(native+web, "outside.ts") || strings.Contains(native+web, "notes.md") {
		t.Errorf("Biome was handed a file outside the governed trees:\n%s", logged)
	}
}

func TestPreCommitHookFormatsNonASCIIAndGlobNamedFiles(t *testing.T) {
	repo := hookRepo(t)
	log := filepath.Join(t.TempDir(), "biome.log")
	installStubBiome(t, repo, "mobile-native", log)
	stageFile(t, repo, "mobile-native/src/café.ts", "a\n")
	stageFile(t, repo, "mobile-native/src/[id]&x.ts", "b\n")

	runIn(t, repo, "git", "commit", "-q", "-m", "x")

	for _, path := range []string{"mobile-native/src/café.ts", "mobile-native/src/[id]&x.ts"} {
		if got := committedContent(t, repo, path); !strings.HasSuffix(got, "// formatted\n") {
			t.Errorf("committed %s = %q, want it formatted", path, got)
		}
	}
}

func TestPreCommitHookLeavesMergeCommitsAlone(t *testing.T) {
	repo := hookRepo(t)
	installStubBiome(t, repo, "mobile-native", filepath.Join(t.TempDir(), "biome.log"))
	stageFile(t, repo, "docs/base.md", "base\n")
	runIn(t, repo, "git", "commit", "-q", "-m", "base")
	runIn(t, repo, "git", "checkout", "-q", "-b", "other")
	stageFile(t, repo, "mobile-native/src/brought.ts", "brought\n")
	stageFile(t, repo, "docs/base.md", "other\n")
	runIn(t, repo, "git", "commit", "-q", "--no-verify", "-m", "other")
	runIn(t, repo, "git", "checkout", "-q", "-")
	stageFile(t, repo, "docs/base.md", "mine\n")
	runIn(t, repo, "git", "commit", "-q", "-m", "mine")
	if output, err := runInErr(repo, "git", "merge", "--no-ff", "other"); err == nil || !strings.Contains(output, "CONFLICT") {
		t.Fatalf("expected a conflict; err = %v, output = %s", err, output)
	}
	stageFile(t, repo, "docs/base.md", "resolved\n")

	runIn(t, repo, "git", "commit", "-q", "-m", "merge")

	if got := committedContent(t, repo, "mobile-native/src/brought.ts"); got != "brought\n" {
		t.Errorf("merge commit rewrote a file it brought in: %q", got)
	}
}

func TestPreCommitHookIgnoresCommitsWithNoGovernedFiles(t *testing.T) {
	repo := hookRepo(t)
	stageFile(t, repo, "docs/readme.md", "hi\n")
	// No node_modules anywhere: nothing staged needs Biome, so nothing is required.
	runIn(t, repo, "git", "commit", "-q", "-m", "x")
}

func TestPreCommitHookRefusesAMissingInstallWithTheFixCommand(t *testing.T) {
	repo := hookRepo(t)
	stageFile(t, repo, "mobile-native/src/a.ts", "a\n")

	output, err := runInErr(repo, "git", "commit", "-q", "-m", "x")

	if err == nil {
		t.Fatalf("commit succeeded without a Biome install; output = %s", output)
	}
	if !strings.Contains(output, "(cd mobile-native && npm ci)") {
		t.Errorf("output does not name the fix command: %s", output)
	}
	if _, err := runInErr(repo, "git", "rev-parse", "--verify", "-q", "HEAD"); err == nil {
		t.Error("a commit was created despite the refusal")
	}
}

func TestPreCommitHookNeverSuggestsNpmCiThroughASymlink(t *testing.T) {
	repo := hookRepo(t)
	shared := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "mobile-native"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(shared, filepath.Join(repo, "mobile-native", "node_modules")); err != nil {
		t.Fatal(err)
	}
	stageFile(t, repo, "mobile-native/src/a.ts", "a\n")

	output, err := runInErr(repo, "git", "commit", "-q", "-m", "x")

	if err == nil {
		t.Fatalf("commit succeeded through a Biome-less symlinked install; output = %s", output)
	}
	if !strings.Contains(output, "never npm ci through it") || !strings.Contains(output, "rm mobile-native/node_modules") {
		t.Errorf("output does not warn about the symlink: %s", output)
	}
}

func TestPreCommitHookRefusesFilesWithUnstagedEdits(t *testing.T) {
	repo := hookRepo(t)
	log := filepath.Join(t.TempDir(), "biome.log")
	installStubBiome(t, repo, "mobile-native", log)
	stageFile(t, repo, "mobile-native/src/a.ts", "staged\n")
	stageFile(t, repo, "mobile-native/src/b.ts", "staged\n")
	writeTestFile(t, filepath.Join(repo, "mobile-native", "src", "a.ts"), []byte("staged\nunstaged\n"), 0o644)

	output, err := runInErr(repo, "git", "commit", "-q", "-m", "x")

	if err == nil {
		t.Fatalf("commit succeeded with a partially staged file; output = %s", output)
	}
	if !strings.Contains(output, "mobile-native/src/a.ts") || !strings.Contains(output, "unstaged edits") {
		t.Errorf("output does not name the partially staged file: %s", output)
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("Biome ran on a partially staged file")
	}
}

func TestPreCommitHookChainsOntoTheInstalledHook(t *testing.T) {
	repo := hookRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	existing := "#!/bin/sh\necho ran > " + marker + "\nexit ${EXISTING_HOOK_EXIT:-0}\n"
	writeTestFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit"), []byte(existing), 0o755)
	stageFile(t, repo, "docs/readme.md", "hi\n")

	runIn(t, repo, "git", "commit", "-q", "-m", "x")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the installed pre-commit hook did not run: %v", err)
	}

	t.Setenv("EXISTING_HOOK_EXIT", "1")
	stageFile(t, repo, "docs/second.md", "again\n")
	if output, err := runInErr(repo, "git", "commit", "-q", "-m", "y"); err == nil {
		t.Fatalf("a failing installed hook did not block the commit; output = %s", output)
	}
}

func TestPreCommitHookDoesNotRunItselfForever(t *testing.T) {
	repo := hookRepo(t)
	data, err := os.ReadFile(filepath.Join("scripts", "hooks", "pre-commit"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit"), data, 0o755)
	if err := os.Remove(filepath.Join(repo, ".git", "hooks", "pre-commit")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(repo, "scripts", "hooks", "pre-commit"), filepath.Join(repo, ".git", "hooks", "pre-commit")); err != nil {
		t.Fatal(err)
	}
	stageFile(t, repo, "docs/readme.md", "hi\n")

	runIn(t, repo, "git", "commit", "-q", "-m", "x")
}

func TestPreCommitHookChainsOntoTheInstalledHookDuringAMerge(t *testing.T) {
	repo := hookRepo(t)
	marker := filepath.Join(t.TempDir(), "ran")
	stageFile(t, repo, "docs/base.md", "base\n")
	runIn(t, repo, "git", "commit", "-q", "-m", "base")
	runIn(t, repo, "git", "checkout", "-q", "-b", "other")
	stageFile(t, repo, "docs/other.md", "other\n")
	runIn(t, repo, "git", "commit", "-q", "-m", "other")
	runIn(t, repo, "git", "checkout", "-q", "-")
	stageFile(t, repo, "docs/mine.md", "mine\n")
	runIn(t, repo, "git", "commit", "-q", "-m", "mine")
	runIn(t, repo, "git", "merge", "--no-ff", "--no-commit", "other")
	writeTestFile(t, filepath.Join(repo, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\necho ran > "+marker+"\n"), 0o755)

	runIn(t, repo, "git", "commit", "-q", "-m", "merge")

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the installed pre-commit hook did not run for the merge commit: %v", err)
	}
}

func TestPreCommitHookFormatsWithTheRealFrontendBiome(t *testing.T) {
	biome, err := filepath.Abs("cmd/evener-hub/frontend/node_modules/.bin/biome")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(biome); err != nil {
		t.Skip("frontend install absent; run make test-web's preflight to enable this case")
	}
	frontendConfig, err := os.ReadFile("cmd/evener-hub/frontend/biome.jsonc")
	if err != nil {
		t.Fatal(err)
	}
	repo := hookRepo(t)
	// Biome's vcs.useIgnoreFile needs a .gitignore in the config's own directory.
	writeTestFile(t, filepath.Join(repo, "cmd", "evener-hub", "frontend", ".gitignore"), []byte("node_modules/\n"), 0o644)
	writeTestFile(t, filepath.Join(repo, "cmd", "evener-hub", "frontend", "biome.jsonc"), frontendConfig, 0o644)
	if err := os.MkdirAll(filepath.Join(repo, "cmd", "evener-hub", "frontend", "node_modules", ".bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(biome, filepath.Join(repo, "cmd", "evener-hub", "frontend", "node_modules", ".bin", "biome")); err != nil {
		t.Fatal(err)
	}
	stageFile(t, repo, "cmd/evener-hub/frontend/src/w.ts", "export const   x = {a:1,\n b:2}\n")

	runIn(t, repo, "git", "commit", "-q", "-m", "x")

	if got, want := committedContent(t, repo, "cmd/evener-hub/frontend/src/w.ts"), "export const x = { a: 1, b: 2 };\n"; got != want {
		t.Errorf("committed content = %q, want %q", got, want)
	}
}

func TestHooksInstallSetsHooksPathAndRefusesToBypass(t *testing.T) {
	t.Run("installs", func(t *testing.T) {
		repo := hookRepo(t)
		runIn(t, repo, "git", "config", "--unset", "core.hooksPath")
		runIn(t, repo, "scripts/hooks/install.sh")
		if got := strings.TrimSpace(runIn(t, repo, "git", "config", "core.hooksPath")); got != "scripts/hooks" {
			t.Errorf("core.hooksPath = %q, want scripts/hooks", got)
		}
	})
	t.Run("refuses a different hooksPath", func(t *testing.T) {
		repo := hookRepo(t)
		runIn(t, repo, "git", "config", "core.hooksPath", ".githooks")
		output, err := runInErr(repo, "scripts/hooks/install.sh")
		if err == nil || !strings.Contains(output, ".githooks") {
			t.Errorf("want a refusal naming .githooks; err = %v, output = %s", err, output)
		}
	})
	t.Run("refuses when another installed hook would stop running", func(t *testing.T) {
		repo := hookRepo(t)
		runIn(t, repo, "git", "config", "--unset", "core.hooksPath")
		writeTestFile(t, filepath.Join(repo, ".git", "hooks", "commit-msg"), []byte("#!/bin/sh\n"), 0o755)
		output, err := runInErr(repo, "scripts/hooks/install.sh")
		if err == nil || !strings.Contains(output, "commit-msg") {
			t.Errorf("want a refusal naming commit-msg; err = %v, output = %s", err, output)
		}
		if out, _ := runInErr(repo, "git", "config", "core.hooksPath"); strings.TrimSpace(out) != "" {
			t.Errorf("core.hooksPath was set despite the refusal: %s", out)
		}
	})
}
