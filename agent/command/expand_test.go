package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/execenv"
)

// recordingExecEnv wraps a real LocalExecutionEnvironment but records every
// command handed to ExecCommand instead of ever running it, so a test can
// assert a dangerous command was never executed rather than merely checking
// the rendered text. Every other method (ReadFile, WorkingDirectory, ...) is
// promoted unchanged from the embedded environment.
type recordingExecEnv struct {
	*execenv.LocalExecutionEnvironment
	execCommands []string
}

func (r *recordingExecEnv) ExecCommand(_ context.Context, command string, _ int, _ string, _ map[string]string) (execenv.ExecResult, error) {
	r.execCommands = append(r.execCommands, command)
	return execenv.ExecResult{}, nil
}

var _ execenv.ExecutionEnvironment = (*recordingExecEnv)(nil)

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

// TestExpand_AtFileEscapingWorkingDirectoryRefused locks in finding #2's
// containment guard: @file expansion runs with no hook/permission visibility
// (unlike the model's Read tool), so a directive whose path escapes the
// working directory is refused rather than read, even though the template
// itself authored the directive.
func TestExpand_AtFileEscapingWorkingDirectoryRefused(t *testing.T) {
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "@../../../../etc/passwd", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if strings.Contains(got, "root:") {
		t.Fatalf("got %q, want /etc/passwd NOT to be read", got)
	}
	if !strings.Contains(got, "refusing") {
		t.Errorf("got %q, want a refusal marker for a path escaping the working directory", got)
	}
}

// TestExpand_AtFileBinaryDegradesGracefully proves a binary file inlined via
// @file degrades to a descriptive marker instead of splicing raw (possibly
// NUL-containing) bytes into the prompt.
func TestExpand_AtFileBinaryDegradesGracefully(t *testing.T) {
	dir := t.TempDir()
	binary := []byte{0x89, 0x50, 0x4E, 0x47, 0x00, 0x01, 0x02, 0xFF, 0xFE, 0x00}
	if err := os.WriteFile(filepath.Join(dir, "data.bin"), binary, 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "@data.bin", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if strings.ContainsRune(got, 0) {
		t.Fatalf("got %q, want no raw NUL bytes spliced into the prompt", got)
	}
	if !strings.Contains(got, "data.bin") {
		t.Errorf("got %q, want a marker naming the binary file", got)
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

// --- @file boundary and trailing-punctuation handling (finding #3) ---

// TestExpand_AtFileEmailNotTreatedAsFile proves a mid-token "@" (the shape of
// an email address) is left as inert literal text rather than being resolved
// as a file lookup for "example.com".
func TestExpand_AtFileEmailNotTreatedAsFile(t *testing.T) {
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	got, err := Expand(context.Background(), "contact foo@example.com", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "contact foo@example.com" {
		t.Errorf("got %q, want the email address left unchanged", got)
	}
}

// TestExpand_AtFileParenthesizedResolvesWithoutTrailingParen proves a
// parenthesized mention resolves the file path without swallowing the
// closing paren into the lookup.
func TestExpand_AtFileParenthesizedResolvesWithoutTrailingParen(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "readme.md"), []byte("README BODY"), 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "(see @docs/readme.md)", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "(see README BODY)" {
		t.Errorf("got %q, want %q", got, "(see README BODY)")
	}
}

// TestExpand_AtFileTrailingPeriodResolvesWithoutPunctuation proves a
// sentence-ending period after a mention resolves the file path without the
// period, and the period survives in the output as literal text.
func TestExpand_AtFileTrailingPeriodResolvesWithoutPunctuation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("NOTES BODY"), 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "per @notes.txt.", "", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got != "per NOTES BODY." {
		t.Errorf("got %q, want %q", got, "per NOTES BODY.")
	}
}

// TestExpand_ArgumentsDoNotOpenAtFileDirective locks in the finding-#1 fix:
// a directive's interior is resolved from the RAW template only, so argument
// text substituted into a "@$1"-shaped template can never turn into a live
// file read. (This used to be the intentional "@$1 reads the arg's path"
// feature; it was greenfield and is dropped rather than hardened, per
// review.) Since args never reach the directive interior, the literal digit
// template "$1" is what gets looked up — which doesn't exist here — rather
// than the caller-supplied "target.txt".
func TestExpand_ArgumentsDoNotOpenAtFileDirective(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("target contents"), 0644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(dir)
	got, err := Expand(context.Background(), "Review @$1", "target.txt", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if strings.Contains(got, "target contents") {
		t.Errorf("got %q, want the argument NOT to open the @ directive (args are inert inside directive spans)", got)
	}
}

// --- Injection safety property (finding #1, CRITICAL) ---
//
// argument text (from $ARGUMENTS/$1..$9) must never be able to open a NEW
// !`cmd`/@file directive that the template author didn't write. Expand must
// locate directive spans ONCE, over the raw pre-substitution template; an
// argument can only ever land inside an already-resolved literal segment,
// never inside a directive's interior.

// TestExpand_ArgumentsCannotOpenBacktickDirective is the load-bearing RCE
// regression test: a template with no backtick syntax of its own must not
// execute a shell command just because the caller's argument text happens to
// look like one. It proves this with a recording fake, not just by checking
// the rendered text, so a "looks safe but still shelled out" regression
// can't slip through.
func TestExpand_ArgumentsCannotOpenBacktickDirective(t *testing.T) {
	env := &recordingExecEnv{LocalExecutionEnvironment: execenv.NewLocalExecutionEnvironment(t.TempDir())}
	got, err := Expand(context.Background(), "Hi $ARGUMENTS", "!`echo PWNED`", env)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if len(env.execCommands) != 0 {
		t.Fatalf("ExecCommand was invoked with %v; argument text must never be executed", env.execCommands)
	}
	if got != "Hi !`echo PWNED`" {
		t.Errorf("got %q, want the literal, unexecuted argument text %q", got, "Hi !`echo PWNED`")
	}
}

// TestExpand_ArgumentsCannotOpenAtFileDirective is the file-exfiltration
// counterpart: argument text shaped like an @file reference (absolute, or
// path-traversing) must not be read, because the template itself
// ("review $ARGUMENTS") never opened an @ directive at that position.
func TestExpand_ArgumentsCannotOpenAtFileDirective(t *testing.T) {
	dir := t.TempDir()
	env := execenv.NewLocalExecutionEnvironment(dir)
	for _, args := range []string{"@/etc/passwd", "@../../../../etc/passwd"} {
		got, err := Expand(context.Background(), "review $ARGUMENTS", args, env)
		if err != nil {
			t.Fatalf("Expand(%q): %v", args, err)
		}
		want := "review " + args
		if got != want {
			t.Errorf("Expand(args=%q) = %q, want %q (argument text must stay literal, never open an @file read)", args, got, want)
		}
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
