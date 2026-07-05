package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
)

func TestExpand_Arguments(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), "Hello $ARGUMENTS!", "world", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "Hello world!" {
		t.Errorf("got %q, want %q", got, "Hello world!")
	}
}

func TestExpand_ArgumentsEmpty(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), "Hello $ARGUMENTS!", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "Hello !" {
		t.Errorf("got %q, want %q", got, "Hello !")
	}
}

func TestExpand_Positional(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), "$1 and $2", "foo bar", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "foo and bar" {
		t.Errorf("got %q, want %q", got, "foo and bar")
	}
}

func TestExpand_PositionalMissingBecomesEmpty(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), "[$1][$2][$3]", "only-one", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "[only-one][][]" {
		t.Errorf("got %q, want %q", got, "[only-one][][]")
	}
}

func TestExpand_PositionalDoesNotMatchDoubleDigit(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	// $10 is not a supported placeholder (only $1..$9); it must survive
	// verbatim rather than being read as "$1" followed by a literal "0".
	got, err := Expand(context.Background(), "$10", "a b c d e f g h i j", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "$10" {
		t.Errorf("got %q, want %q (unchanged)", got, "$10")
	}
}

func TestExpand_PositionalQuotedArgs(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), `$1|$2|$3`, `foo "bar baz" qux`, env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "foo|bar baz|qux" {
		t.Errorf("got %q, want %q", got, "foo|bar baz|qux")
	}
}

func TestExpand_NoPlaceholders(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), "Just plain text.", "ignored args", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "Just plain text." {
		t.Errorf("got %q, want unchanged", got)
	}
}

func TestExpand_BacktickCommand(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), "Output: !`echo hello`", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "Output: hello" {
		t.Errorf("got %q, want %q", got, "Output: hello")
	}
}

func TestExpand_BacktickCommandUsesWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "!`ls`", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !strings.Contains(got, "marker.txt") {
		t.Errorf("got %q, want it to contain marker.txt (command should run in WorkingDirectory)", got)
	}
}

func TestExpand_BacktickCommandNonzeroExitStillSubstitutesStdout(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), "!`echo partial && exit 1`", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "partial" {
		t.Errorf("got %q, want %q (nonzero exit should not suppress captured stdout)", got, "partial")
	}
}

func TestExpand_BacktickCommandBounded(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), "!`yes x | head -c 40000`", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(got) <= maxInlineBytes {
		t.Fatalf("expected output to be bounded and marked, got %d bytes", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected a truncation marker, got suffix: %q", got[len(got)-60:])
	}
}

func TestExpand_AtFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("file contents here"), 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "See @notes.txt for details.", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "See file contents here for details." {
		t.Errorf("got %q", got)
	}
}

func TestExpand_AtFileSubdirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.txt"), []byte("nested"), 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "@sub/nested.txt", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "nested" {
		t.Errorf("got %q, want %q", got, "nested")
	}
}

func TestExpand_AtFileMissing(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), "@does-not-exist.txt", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if !strings.Contains(got, "does-not-exist.txt") || !strings.Contains(got, "error") {
		t.Errorf("expected an inline error marker naming the missing file, got %q", got)
	}
}

func TestExpand_AtFileBounded(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", maxInlineBytes+1000)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big), 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "@big.txt", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(got) >= len(big) {
		t.Fatalf("expected truncated output shorter than the %d-byte file, got %d bytes", len(big), len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("expected a truncation marker")
	}
}

func TestExpand_ArgumentsFlowIntoAtFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("target contents"), 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "Review @$1", "target.txt", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "Review target contents" {
		t.Errorf("got %q, want the argument-substituted path to be resolved and inlined", got)
	}
}

// TestExpand_CommandOutputNotRescannedForFile locks in the safety property
// documented on cmdOrFilePattern: a !`cmd` whose stdout happens to contain
// "@word" text must not have that text reinterpreted as a further @file
// reference, because both are resolved in one scan over the pre-expansion
// body, not by re-scanning substituted output.
func TestExpand_CommandOutputNotRescannedForFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "sentinel.txt"), []byte("SENTINEL-SHOULD-NOT-APPEAR"), 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "!`echo @sentinel.txt`", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "@sentinel.txt" {
		t.Errorf("got %q, want the literal text %q (not the file's contents)", got, "@sentinel.txt")
	}
	if strings.Contains(got, "SENTINEL-SHOULD-NOT-APPEAR") {
		t.Error("command output was re-scanned for @file references")
	}
}

// TestExpand_FileContentNotRescannedForCommand is the mirror-image safety
// property: a file whose contents happen to contain !`cmd` syntax must not
// have that syntax executed when the file is inlined via @file.
func TestExpand_FileContentNotRescannedForCommand(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "danger.txt"), []byte("!`echo pwned`"), 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "@danger.txt", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "!`echo pwned`" {
		t.Errorf("got %q, want the literal file contents unexecuted", got)
	}
	if strings.Contains(got, "pwned") && !strings.Contains(got, "echo pwned") {
		t.Error("file content was re-scanned and executed as a command")
	}
}

func TestExpand_ContextAlreadyCancelled(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Expand(ctx, "anything", "", env)
	if err == nil {
		t.Fatal("expected an error for an already-cancelled context")
	}
}
