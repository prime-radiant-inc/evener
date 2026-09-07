package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/envvars/userdirs"
)

func TestLoadProjectDocs_WalksFromGitRootToWorkingDir_InDepthOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)

	// Working directory is nested inside the repo.
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Instruction files at each level.
	_ = os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "a", "AGENTS.md"), []byte("A\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "a", "b", "AGENTS.md"), []byte("B\n"), 0o644)

	env := execenv.NewLocalExecutionEnvironment(nested)
	docs, truncated := LoadProjectDocs(env, "AGENTS.md")
	if truncated {
		t.Fatalf("did not expect truncation")
	}
	if got, want := len(docs), 3; got != want {
		t.Fatalf("docs: got %d want %d (%v)", got, want, docs)
	}
	if docs[0].Path != "AGENTS.md" {
		t.Fatalf("doc0 path: %q", docs[0].Path)
	}
	if docs[1].Path != filepath.Join("a", "AGENTS.md") {
		t.Fatalf("doc1 path: %q", docs[1].Path)
	}
	if docs[2].Path != filepath.Join("a", "b", "AGENTS.md") {
		t.Fatalf("doc2 path: %q", docs[2].Path)
	}
}

func TestLoadProjectDocs_TruncatesTo32KBAndAddsMarker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)

	huge := strings.Repeat("x", projectDocByteBudget+4096)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(huge), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := execenv.NewLocalExecutionEnvironment(root)
	docs, truncated := LoadProjectDocs(env, "AGENTS.md")
	if !truncated {
		t.Fatalf("expected truncation")
	}
	if got, want := len(docs), 1; got != want {
		t.Fatalf("docs: got %d want %d", got, want)
	}
	if !strings.Contains(docs[0].Content, projectDocTruncMark) {
		t.Fatalf("expected truncation marker, got:\n%s", docs[0].Content)
	}
}

func markGitRoot(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir .git: %v", err)
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, string(out))
		}
	}

	run("init")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644)
	run("add", "README.md")
	run("commit", "-m", "init")
}

func TestLoadUserDoc_ReadsTheConfigRootFile(t *testing.T) {
	t.Parallel()
	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "AGENTS.md"), []byte("PERSONAL\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	doc, ok := LoadUserDoc(filepath.Join(configRoot, "AGENTS.md"))
	if !ok {
		t.Fatal("expected the personal doc to load")
	}
	if doc.Path != filepath.Join(configRoot, "AGENTS.md") {
		t.Fatalf("path: %q", doc.Path)
	}
	if doc.Content != "PERSONAL\n" {
		t.Fatalf("content: %q", doc.Content)
	}
}

func TestLoadUserDoc_MissingOrBlankFileIsAbsent(t *testing.T) {
	t.Parallel()
	if _, ok := LoadUserDoc(filepath.Join(t.TempDir(), "AGENTS.md")); ok {
		t.Fatal("a missing file must not load")
	}
	blank := t.TempDir()
	if err := os.WriteFile(filepath.Join(blank, "AGENTS.md"), []byte("  \n\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, ok := LoadUserDoc(filepath.Join(blank, "AGENTS.md")); ok {
		t.Fatal("a blank file must not load")
	}
	if _, ok := LoadUserDoc(""); ok {
		t.Fatal("an empty path must not load")
	}
}

func TestLoadUserDoc_CollapsesTheHomeDirectoryToTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configRoot := filepath.Join(home, ".config", "evener")
	if err := os.MkdirAll(configRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "AGENTS.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	doc, ok := LoadUserDoc(filepath.Join(configRoot, "AGENTS.md"))
	if !ok {
		t.Fatal("expected the personal doc to load")
	}
	if doc.Path != "~/.config/evener/AGENTS.md" {
		t.Fatalf("path: %q, want the tilde-collapsed display path", doc.Path)
	}
}

// The path the loader is handed is the file it reads, whatever the process
// environment would resolve on its own: a hub whose launch config overrides
// XDG_CONFIG_HOME per launch hands its own concrete path to the session, and
// Settings and that session have to agree on the file.
func TestLoadUserDoc_ReadsTheGivenPathNotTheEnvironment(t *testing.T) {
	decoyConfigHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", decoyConfigHome)
	decoyRoot := userdirs.DefaultConfigRoot()
	if err := os.MkdirAll(decoyRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(decoyRoot, UserDocFile), []byte("DECOY\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	wanted := filepath.Join(t.TempDir(), UserDocFile)
	if err := os.WriteFile(wanted, []byte("EXPLICIT\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	doc, ok := LoadUserDoc(wanted)
	if !ok {
		t.Fatal("expected the explicitly named file to load")
	}
	if doc.Content != "EXPLICIT\n" {
		t.Fatalf("content = %q, want the file at the given path", doc.Content)
	}
}

func TestLoadInstructionDocs_PersonalDocComesFirst(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configRoot, "AGENTS.md"), []byte("PERSONAL\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := execenv.NewLocalExecutionEnvironment(root)
	docs, truncated := LoadInstructionDocs(env, filepath.Join(configRoot, "AGENTS.md"), "AGENTS.md")
	if truncated {
		t.Fatal("did not expect truncation")
	}
	if len(docs) != 2 {
		t.Fatalf("docs: got %d want 2 (%v)", len(docs), docs)
	}
	if docs[0].Path != filepath.Join(configRoot, "AGENTS.md") || docs[0].Content != "PERSONAL\n" {
		t.Fatalf("doc0 = %+v, want the personal doc first", docs[0])
	}
	if docs[1].Path != "AGENTS.md" || docs[1].Content != "ROOT\n" {
		t.Fatalf("doc1 = %+v, want the repo doc second", docs[1])
	}
}

func TestLoadInstructionDocs_MissingPersonalDocChangesNothing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(root)
	docs, _ := LoadInstructionDocs(env, filepath.Join(t.TempDir(), "AGENTS.md"), "AGENTS.md")
	if len(docs) != 1 || docs[0].Path != "AGENTS.md" {
		t.Fatalf("docs = %+v, want only the repo doc", docs)
	}
}

func TestLoadInstructionDocs_PersonalDocCountsAgainstTheSharedBudget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	half := strings.Repeat("r", projectDocByteBudget/2+1024)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte(half), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configRoot := t.TempDir()
	personal := strings.Repeat("p", projectDocByteBudget/2)
	if err := os.WriteFile(filepath.Join(configRoot, "AGENTS.md"), []byte(personal), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := execenv.NewLocalExecutionEnvironment(root)
	docs, truncated := LoadInstructionDocs(env, filepath.Join(configRoot, "AGENTS.md"), "AGENTS.md")
	if !truncated {
		t.Fatal("expected the repo doc to be truncated")
	}
	if len(docs) != 2 {
		t.Fatalf("docs: got %d want 2", len(docs))
	}
	if docs[0].Content != personal {
		t.Fatal("the personal doc must land intact; it is loaded first")
	}
	if !strings.Contains(docs[1].Content, projectDocTruncMark) {
		t.Fatalf("the repo doc must carry the truncation marker, got:\n%s", docs[1].Content[len(docs[1].Content)-80:])
	}
	if len(docs[0].Content)+len(docs[1].Content) > projectDocByteBudget+len(projectDocTruncMark)+2 {
		t.Fatalf("the two docs exceed the shared budget: %d", len(docs[0].Content)+len(docs[1].Content))
	}
}

func TestLoadInstructionDocs_OversizedPersonalDocIsTruncatedAlone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	markGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("ROOT\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	configRoot := t.TempDir()
	huge := strings.Repeat("p", projectDocByteBudget+4096)
	if err := os.WriteFile(filepath.Join(configRoot, "AGENTS.md"), []byte(huge), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(root)
	docs, truncated := LoadInstructionDocs(env, filepath.Join(configRoot, "AGENTS.md"), "AGENTS.md")
	if !truncated || len(docs) != 1 {
		t.Fatalf("docs = %d truncated = %v; an oversized personal doc consumes the whole budget", len(docs), truncated)
	}
	if !strings.Contains(docs[0].Content, projectDocTruncMark) {
		t.Fatal("expected the truncation marker on the personal doc")
	}
}
