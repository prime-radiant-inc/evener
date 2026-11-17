package execenv

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

// fixturePathEnv is the inherited environment the route tests run with: the
// given directories on PATH, in order, and nothing the test shell happened to
// export. The suite's result must not depend on who runs it — an inherited
// BASH_ENV=/dev/null alone sends every command to the shell route, and the
// fixture PATH entries mean a program name resolves to the fixture rather than
// to whatever this host has installed.
func fixturePathEnv(dirs ...string) []string {
	return []string{
		envvars.Path.Name + "=" + strings.Join(dirs, string(os.PathListSeparator)),
		"HOME=/nonexistent-test-home",
	}
}

// fixtureBin makes a directory to hold fixture programs and returns it with the
// tail every fixture PATH keeps for the platform's own tools.
func fixtureBin(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "fixtures")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSplitShellWords pins the word splitter's whole contract: the exact words
// it produces for a bare word list, and its refusal for every construct a POSIX
// shell would act on. A wrong guess here changes what a command means — a
// naive strings.Fields split turns `grep "foo bar" x` into four arguments — so
// every ambiguous input must come back ok=false and fall back to the shell.
func TestSplitShellWords(t *testing.T) {
	t.Parallel()

	accepted := []struct {
		name    string
		command string
		want    []string
	}{
		{"bare words", "git status --porcelain", []string{"git", "status", "--porcelain"}},
		{"single word", "ls", []string{"ls"}},
		{"tab separators", "git\tstatus", []string{"git", "status"}},
		{"leading and trailing space", "  git status  ", []string{"git", "status"}},
		{"double-quoted argument with a space", `grep "foo bar" x`, []string{"grep", "foo bar", "x"}},
		{"single-quoted argument with a space", `grep 'foo bar' x`, []string{"grep", "foo bar", "x"}},
		{"single quote inside double quotes", `grep "it's here" x`, []string{"grep", "it's here", "x"}},
		{"double quote inside single quotes", `grep 'say "hi"' x`, []string{"grep", `say "hi"`, "x"}},
		{"escaped space", `grep a\ b x`, []string{"grep", "a b", "x"}},
		{"escaped double quote in double quotes", `grep "a\"b" x`, []string{"grep", `a"b`, "x"}},
		{"escaped backslash in double quotes", `grep "a\\b" x`, []string{"grep", `a\b`, "x"}},
		{"escaped dollar in double quotes stays literal", `grep "\$HOME" x`, []string{"grep", "$HOME", "x"}},
		{"backslash before an ordinary byte in double quotes stays literal", `grep "a\qb" x`, []string{"grep", `a\qb`, "x"}},
		{"adjacent quoted and unquoted parts join one word", `grep pre"mid dle"post x`, []string{"grep", "premid dlepost", "x"}},
		{"single-quote escape sequence", `grep 'it'\''s' x`, []string{"grep", "it's", "x"}},
		{"empty quoted argument", `grep "" x`, []string{"grep", "", "x"}},
		{"quoted word that is only empty", `""`, []string{""}},
		{"empty single-quoted argument", `grep '' x`, []string{"grep", "", "x"}},
		// Quoting is what makes a metacharacter literal, so a quoted operator or
		// glob is an ordinary word: `find . -name '*.go'` and `grep 'a|b' x`
		// pass those bytes to the program, they do not ask the shell for them.
		{"quoted glob stays literal", `find . -name '*.go'`, []string{"find", ".", "-name", "*.go"}},
		{"quoted pipe stays literal", `grep 'a|b' x`, []string{"grep", "a|b", "x"}},
		{"double-quoted glob stays literal", `grep "*.go" x`, []string{"grep", "*.go", "x"}},
		{"escaped metacharacter stays literal", `grep a\|b x`, []string{"grep", "a|b", "x"}},
		{"assignment word splits like any other", "FOO=bar git status", []string{"FOO=bar", "git", "status"}},
		{"path program", "/bin/true", []string{"/bin/true"}},
	}

	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := splitShellWords(tc.command)
			if !ok {
				t.Fatalf("splitShellWords(%q) refused a bare word list", tc.command)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("splitShellWords(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}

	// Every one of these must reach the shell: either the construct changes
	// what the command means, or splitting it is a guess.
	refused := []struct {
		name    string
		command string
	}{
		{"pipe", `git log | head -1`},
		{"pipe without spaces", `git log|head`},
		{"and-and", `make && echo ok`},
		{"or-or", `false || true`},
		{"semicolon", `cd /tmp; ls`},
		{"background ampersand", `sleep 5 &`},
		{"subshell", `(cd /tmp && ls)`},
		{"brace group", `{ echo a; echo b; }`},
		{"input redirection", `wc -l < file`},
		{"output redirection", `echo hi > file`},
		{"append redirection", `echo hi >> file`},
		{"stderr merge", `go build 2>&1`},
		{"here-document", "cat <<EOF\nbody\nEOF"},
		{"here-string", `cat <<< word`},
		{"glob star", `ls *.go`},
		{"glob question mark", `ls file?.txt`},
		{"glob character class", `ls [ab].txt`},
		{"brace expansion", `mkdir {a,b}`},
		{"variable expansion", `echo $HOME`},
		{"braced variable expansion", `echo ${HOME}`},
		{"command substitution", `echo $(pwd)`},
		{"backtick substitution", "echo `pwd`"},
		{"tilde expansion", `ls ~`},
		{"tilde inside a word", `ls a~b`},
		{"history expansion", `!ls`},
		{"comment", `git status # trailing note`},
		{"unmatched double quote", `grep "foo`},
		{"unmatched single quote", `grep 'foo`},
		{"trailing backslash", `grep foo\`},
		{"newline separates two commands", "git status\ngit log"},
		{"line continuation", "git \\\n status"},
		{"double-quoted variable", `grep "$HOME" x`},
		{"double-quoted command substitution", "grep \"`pwd`\" x"},
		{"escaped newline in double quotes", "grep \"a\\\nb\" x"},
	}

	// The splitter itself is happy to find no words; refusing an empty command
	// is the classifier's job (TestArgvFromCommandString's empty cases).
	for _, command := range []string{"", "   "} {
		if got, ok := splitShellWords(command); !ok || len(got) != 0 {
			t.Fatalf("splitShellWords(%q) = %q ok=%t, want no words", command, got, ok)
		}
	}

	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := splitShellWords(tc.command)
			if ok {
				t.Fatalf("splitShellWords(%q) = %q, want a shell fallback", tc.command, got)
			}
		})
	}
}

// TestArgvFromCommandString pins the classifier's extra refusals on top of the
// splitter: a bash builtin, a reserved word, or a leading assignment must keep
// the shell, because the shell does not run a PATH program for those.
func TestArgvFromCommandString(t *testing.T) {
	t.Parallel()

	accepted := []struct {
		name    string
		command string
		want    []string
	}{
		{"git status", "git status --porcelain", []string{"git", "status", "--porcelain"}},
		{"program with no arguments", "ls", []string{"ls"}},
		{"quoted argument", `grep "foo bar" x`, []string{"grep", "foo bar", "x"}},
		{"program path", "/bin/true", []string{"/bin/true"}},
		{"bare-name tool with an equals in an argument", "git log --format=%H", []string{"git", "log", "--format=%H"}},
	}

	for _, tc := range accepted {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name, args, ok := argvFromCommandString(tc.command)
			if !ok {
				t.Fatalf("argvFromCommandString(%q) refused", tc.command)
			}
			got := append([]string{name}, args...)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("argvFromCommandString(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}

	refused := []struct {
		name    string
		command string
	}{
		// bash resolves these itself, ahead of any PATH program of the same
		// name, and several have no binary at all.
		{"builtin echo", "echo hello"},
		{"builtin printf", `printf '%s\n' x`},
		{"builtin pwd", "pwd"},
		{"builtin exit", "exit 0"},
		{"builtin true", "true"},
		{"builtin test", "test -f file"},
		{"reserved word if", "if true; then echo a; fi"},
		{"builtin with no arguments", "export"},
		{"leading assignment", "FOO=bar git status"},
		{"assignment alone", "FOO=bar"},
		{"assignment with a quoted value", `FOO="a b" git status`},
		// The splitter's refusals carry through: one word of shell syntax in
		// any position keeps the whole line on the shell.
		{"pipe", "git log | head -1"},
		{"glob", "ls *.go"},
		{"variable", "echo $HOME"},
		{"redirection", "echo hi > file"},
		{"empty command", ""},
		{"empty program word", `"" x`},
	}

	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if name, args, ok := argvFromCommandString(tc.command); ok {
				t.Fatalf("argvFromCommandString(%q) = %q, want a shell fallback", tc.command, append([]string{name}, args...))
			}
		})
	}
}

// TestArgvFromCommandStringRefusesWindows pins that the classifier is a POSIX
// statement only. cmd.exe splits and expands a command line by its own rules,
// so on Windows every command string keeps going through the shell.
//
// Not parallel: it swaps the package-wide runtimeGOOS seam, as the shellCommand
// tests next to it do.
func TestArgvFromCommandStringRefusesWindows(t *testing.T) {
	original := runtimeGOOS
	t.Cleanup(func() { runtimeGOOS = original })
	runtimeGOOS = "windows"

	if name, args, ok := argvFromCommandString("git status --porcelain"); ok {
		t.Fatalf("argvFromCommandString on windows = %q, want a shell fallback", append([]string{name}, args...))
	}
}

// TestExecCommandChoosesTheRuntimeByCommandString pins which runtime a command
// string reaches, through the environment's existing command-factory seam: a
// bare word list must arrive as argv, and every construct the classifier
// refuses must arrive as the original command string.
func TestExecCommandChoosesTheRuntimeByCommandString(t *testing.T) {
	t.Parallel()

	bin := fixtureBin(t)
	writeExecFixture(t, filepath.Join(bin, "git"), "#!/bin/sh\nexit 0\n")
	writeExecFixture(t, filepath.Join(bin, "grep"), "#!/bin/sh\nexit 0\n")

	cases := []struct {
		name string
		// command is what the shell tool passes to ExecCommand.
		command string
		// wantArgv is the exact program and arguments the fast path must hand
		// the runtime; nil means the command must reach the shell unchanged.
		wantArgv []string
	}{
		{name: "git status", command: "git status --porcelain", wantArgv: []string{"git", "status", "--porcelain"}},
		{name: "quoted argument", command: `grep "foo bar" x`, wantArgv: []string{"grep", "foo bar", "x"}},
		{name: "escaped space", command: `grep a\ b x`, wantArgv: []string{"grep", "a b", "x"}},
		{name: "pipe", command: "git log | head -1"},
		{name: "and-and", command: "make && echo ok"},
		{name: "glob", command: "ls *.go"},
		{name: "variable", command: "go build -ldflags $FLAGS"},
		{name: "redirection", command: "go build 2>&1"},
		{name: "leading assignment", command: "FOO=bar git status"},
		{name: "builtin", command: "echo hello"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env, factory := recordingCommandEnv(t, fixturePathEnv(bin)...)
			if _, err := env.ExecCommand(context.Background(), tc.command, 5000, "", nil); err != nil {
				t.Fatalf("ExecCommand(%q): %v", tc.command, err)
			}
			if tc.wantArgv != nil {
				if len(factory.shell) != 0 {
					t.Fatalf("ExecCommand(%q) ran through the shell: %q", tc.command, factory.shell)
				}
				if len(factory.argv) != 1 || !slices.Equal(factory.argv[0], tc.wantArgv) {
					t.Fatalf("ExecCommand(%q) argv = %q, want %q", tc.command, factory.argv, tc.wantArgv)
				}
				return
			}
			if len(factory.argv) != 0 {
				t.Fatalf("ExecCommand(%q) ran as argv %q, want the shell", tc.command, factory.argv)
			}
			if !slices.Equal(factory.shell, []string{tc.command}) {
				t.Fatalf("ExecCommand(%q) shell = %q, want the command unchanged", tc.command, factory.shell)
			}
		})
	}
}

// TestExecCommandFastPathMatchesTheShell pins the claim the fast path rests on:
// for a command string it accepts, the program sees exactly what `bash -c` would
// have shown it. bash is the oracle here, not the splitter: a fixture prints its
// own arguments one per line, and each case runs the same command string twice
// through execPreparedCommand — once forced through the shell and once through
// ExecCommand's choice — so a mis-split quote, a lost empty argument, a
// mishandled escape, or a program resolved differently shows up as a differing
// result. The unexecutable paths in the table are the cases the classifier must
// refuse (TestExecCommandKeepsTheShellForAPathThatIsNotExecutable pins that);
// here they prove the refusal costs nothing, since both runs are the shell. Which
// runtime ExecCommand picks for each case is pinned separately, by
// TestExecCommandChoosesTheRuntimeByCommandString and the two KeepsTheShell tests.
func TestExecCommandFastPathMatchesTheShell(t *testing.T) {
	t.Parallel()
	if runtimeGOOS == "windows" {
		t.Skip("POSIX quoting contract does not apply on windows")
	}

	root := t.TempDir()
	bin := filepath.Join(root, ".venv", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done\n"
	argPath := filepath.Join(bin, "argvdump")
	nonExecutable := filepath.Join(root, "noexec")
	plainText := filepath.Join(root, "plain")
	strayNUL := filepath.Join(root, "nultext")
	emptyFile := filepath.Join(root, "empty")

	// Executable fixtures go through writeExecFixture, which holds
	// syscall.ForkLock, so a sibling test forking concurrently cannot inherit an
	// open write descriptor to a fixture and make a later launch fail ETXTBSY.
	writeExecFixture(t, argPath, tool)
	writeExecFixture(t, plainText, "exit 0\n")
	writeExecFixture(t, strayNUL, "exit 3\x00junk\n")
	writeExecFixture(t, emptyFile, "")
	if err := os.WriteFile(nonExecutable, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// fastPath marks the commands that must run as argv; the rest are the
	// classifier's refusals and must reach the shell unchanged.
	cases := []struct {
		command  string
		fastPath bool
	}{
		{`argvdump plain words`, true},
		{`argvdump 'a b' "c d"`, true},
		{`argvdump e\ f`, true},
		{`argvdump "it's here"`, true},
		{`argvdump 'say "hi"'`, true},
		{`argvdump '' ""`, true},
		{`argvdump pre"mid dle"post`, true},
		{`argvdump 'it'\''s'`, true},
		{`argvdump "a\"b" "a\\b" "\$HOME" "a\qb"`, true},
		{`argvdump '*.go' a\|b "*.md"`, true},
		{`argvdump --format=%H`, true},
		{"argvdump\ttabbed\twords", true},
		{`  argvdump   spaced  `, true},
		// A program named by path, resolved identically on both sides.
		{argPath + ` 'a b'`, true},
		// bash runs a shebang-less executable text file as a script, and reports
		// 126 or 127 for a path it cannot run. ExecCommand must land where the
		// forced shell run does.
		{plainText, false},
		{strayNUL, false},
		{emptyFile, false},
		{nonExecutable, false},
		{filepath.Join(root, "missing"), false},
	}

	for _, tc := range cases {
		command := tc.command
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			env := NewLocalExecutionEnvironment(root)
			t.Cleanup(env.Cleanup)
			// A controlled inherited environment: the fixture bin (which the
			// venv injection also finds) plus the platform's own tools, and no
			// BASH_ENV or exported function inherited from the test shell.
			env.inheritedEnv = func() []string { return fixturePathEnv(bin, "/usr/local/bin", "/usr/bin", "/bin") }
			recorder := &recordingCommandFactory{delegate: systemCommandRuntimeFactory{}}
			env.commandFactory = recorder

			ctx := context.Background()
			shell, shellErr := env.execPreparedCommand(ctx, env.commands().Shell(command), 5000, "", nil)
			recorder.shell, recorder.argv = nil, nil
			fast, fastErr := env.ExecCommand(ctx, command, 5000, "", nil)
			if tc.fastPath != (len(recorder.argv) == 1 && len(recorder.shell) == 0) {
				t.Fatalf("%q: route argv=%q shell=%q, want fastPath=%t", command, recorder.argv, recorder.shell, tc.fastPath)
			}
			if (shellErr == nil) != (fastErr == nil) {
				t.Fatalf("%q: ExecCommand error %v, the shell error %v", command, fastErr, shellErr)
			}
			if fast.Stdout != shell.Stdout || fast.Stderr != shell.Stderr || fast.ExitCode != shell.ExitCode {
				t.Fatalf("%q: ExecCommand gave %q/%q exit %d, the shell gave %q/%q exit %d",
					command, fast.Stdout, fast.Stderr, fast.ExitCode, shell.Stdout, shell.Stderr, shell.ExitCode)
			}
		})
	}
}

// TestExecCommandKeepsTheShellWhenBashEnvIsSet pins the last shell feature that
// can intervene before the program runs. A non-interactive bash sources
// $BASH_ENV, and a function defined there shadows the PATH program of the same
// name, so an argv runtime would run the binary where the shell runs the
// function. The fast path must keep the shell whenever the child environment
// would give bash that file.
func TestExecCommandKeepsTheShellWhenBashEnvIsSet(t *testing.T) {
	t.Parallel()
	if runtimeGOOS == "windows" {
		t.Skip("POSIX shell contract does not apply on windows")
	}
	gitPath, err := execLookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	// Set explicitly rather than inherited: this test is the one that is about
	// BASH_ENV, so no test shell may supply it.
	childEnv := fixturePathEnv(filepath.Dir(gitPath), "/usr/local/bin", "/usr/bin", "/bin")

	root := t.TempDir()
	bashEnv := filepath.Join(root, "bashenv")
	if err := os.WriteFile(bashEnv, []byte("git() { echo shadowed; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Through the shell, the sourced function wins over the git binary.
	shellEnv := NewLocalExecutionEnvironment(root)
	t.Cleanup(shellEnv.Cleanup)
	shellEnv.inheritedEnv = func() []string { return childEnv }
	res, err := shellEnv.ExecCommand(context.Background(), "git status --porcelain", 5000, "", map[string]string{"BASH_ENV": bashEnv})
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if strings.TrimSpace(res.Stdout) != "shadowed" {
		t.Fatalf("BASH_ENV function did not run: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}

	// The route itself: with BASH_ENV set the command must reach the shell.
	env, factory := recordingCommandEnv(t, childEnv...)
	if _, err := env.ExecCommand(context.Background(), "git status --porcelain", 5000, "", map[string]string{"BASH_ENV": bashEnv}); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if len(factory.argv) != 0 {
		t.Fatalf("BASH_ENV-set command ran as argv %q, want the shell", factory.argv)
	}

	// With it unset, the same command takes the fast path.
	factory.shell, factory.argv = nil, nil
	if _, err := env.ExecCommand(context.Background(), "git status --porcelain", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if len(factory.argv) != 1 {
		t.Fatalf("argv = %q, want the fast path", factory.argv)
	}
}

// recordingCommandEnv returns an environment whose factory records the runtime
// choice, and an inert runtime that neither forks nor writes. inherited is the
// whole inherited environment, so a route assertion cannot depend on the
// environment this test process happens to run in.
func recordingCommandEnv(t *testing.T, inherited ...string) (*LocalExecutionEnvironment, *recordingCommandFactory) {
	t.Helper()
	env := NewLocalExecutionEnvironment(t.TempDir())
	factory := &recordingCommandFactory{}
	env.commandFactory = factory
	env.inheritedEnv = func() []string { return inherited }
	t.Cleanup(env.Cleanup)
	return env, factory
}

// TestExecCommandKeepsTheShellForAProgramTheChildPathCannotResolve pins the one
// divergence the fast path could introduce. bash resolves a bare program name
// against the CHILD environment's PATH; os/exec falls back to this process's own
// PATH when the child's does not name it. An argv runtime could therefore run a
// binary the shell would have refused to find, so a bare name the child PATH
// cannot resolve stays on the shell — while a path-shaped program, which the
// shell also execs as named, may still take the fast path.
func TestExecCommandKeepsTheShellForAProgramTheChildPathCannotResolve(t *testing.T) {
	t.Parallel()
	if _, err := execLookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// The child PATH names nothing, so the shell would not find `git` — even
	// though this process's own PATH does.
	emptyBin := filepath.Join(t.TempDir(), "no-such-bin")
	env, factory := recordingCommandEnv(t, envvars.Path.Name+"="+emptyBin)

	if _, err := env.ExecCommand(context.Background(), "git status --porcelain", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if len(factory.argv) != 0 {
		t.Fatalf("unresolvable program ran as argv %q, want the shell", factory.argv)
	}
	if !slices.Equal(factory.shell, []string{"git status --porcelain"}) {
		t.Fatalf("shell = %q, want the command unchanged", factory.shell)
	}

	// A program named by path needs no lookup on either side.
	program := filepath.Join(t.TempDir(), "binary")
	writeExecFixture(t, program, "#!/bin/sh\nexit 0\n")
	if _, err := env.ExecCommand(context.Background(), program, 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	if len(factory.argv) != 1 || !slices.Equal(factory.argv[0], []string{program}) {
		t.Fatalf("argv = %q, want the path-shaped program on the fast path", factory.argv)
	}
}

// TestExecCommandKeepsTheShellForAPathThatIsNotExecutable pins the other two
// resolution differences between bash and a direct exec. bash reports a
// non-executable file or a directory as 126 where a failed Start reaches the
// caller as 127, and it runs a shebang-less text file as a script where execve
// returns ENOEXEC. Every such path must keep the shell; a `#!` file and a binary
// (its NUL header) may still take the fast path.
func TestExecCommandKeepsTheShellForAPathThatIsNotExecutable(t *testing.T) {
	t.Parallel()
	if runtimeGOOS == "windows" {
		t.Skip("POSIX exec semantics do not apply on windows")
	}

	root := t.TempDir()
	writeExec := func(name, contents string) string {
		t.Helper()
		path := filepath.Join(root, name)
		writeExecFixture(t, path, contents)
		return path
	}
	shebang := writeExec("shebang", "#!/bin/sh\nexit 0\n")
	program := writeExec("program", "#!/bin/sh\nexit 0\n")
	noShebang := writeExec("plain", "exit 0\n")
	// A stray NUL does not make a text file a program: execve still returns
	// ENOEXEC (bash: 126 "cannot execute binary file"), so a NUL is not a
	// usable "this is a binary" test and the format whitelist must refuse it.
	strayNUL := writeExec("nultext", "exit 3\x00junk\n")
	emptyFile := writeExec("empty", "")
	notExecutable := filepath.Join(root, "noexec")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "adir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	env, factory := recordingCommandEnv(t)
	ctx := context.Background()

	for _, path := range []string{shebang, program} {
		factory.shell, factory.argv = nil, nil
		if _, err := env.ExecCommand(ctx, path, 5000, "", nil); err != nil {
			t.Fatalf("ExecCommand(%q): %v", path, err)
		}
		if len(factory.argv) != 1 {
			t.Fatalf("ExecCommand(%q) = shell %q, want the fast path", path, factory.shell)
		}
	}
	for _, path := range []string{noShebang, strayNUL, emptyFile, notExecutable, dir, filepath.Join(root, "missing")} {
		factory.shell, factory.argv = nil, nil
		if _, err := env.ExecCommand(ctx, path, 5000, "", nil); err != nil {
			t.Fatalf("ExecCommand(%q): %v", path, err)
		}
		if len(factory.argv) != 0 {
			t.Fatalf("ExecCommand(%q) = argv %q, want the shell", path, factory.argv)
		}
	}
}

// TestStreamCommandChoosesTheSameRuntimeAsExecCommand pins that both entry points
// choose one runtime: the shell tool does not call ExecCommand, it calls
// StreamCommand (registerShellTools → runShell), so a selection made only in
// ExecCommand left ordinary shell tool calls paying the `bash -o pipefail -c`
// wrapper this change removes. Both entry points must hand the same command to
// the same runtime.
func TestStreamCommandChoosesTheSameRuntimeAsExecCommand(t *testing.T) {
	t.Parallel()

	bin := fixtureBin(t)
	writeExecFixture(t, filepath.Join(bin, "git"), "#!/bin/sh\nexit 0\n")

	cases := []struct {
		name     string
		command  string
		wantArgv []string
	}{
		{name: "git status", command: "git status --porcelain", wantArgv: []string{"git", "status", "--porcelain"}},
		{name: "quoted argument", command: `git log --format="a b"`, wantArgv: []string{"git", "log", "--format=a b"}},
		{name: "pipe", command: "git log | head -1"},
		{name: "redirection", command: "git status 2>&1"},
		{name: "glob", command: "git status *.go"},
		{name: "variable", command: "git log $REV"},
		{name: "builtin", command: "echo hello"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env, factory := recordingCommandEnv(t, fixturePathEnv(bin)...)
			handle, err := env.StreamCommand(context.Background(), tc.command, "", nil, nil)
			if err != nil {
				t.Fatalf("StreamCommand(%q): %v", tc.command, err)
			}
			if _, err := handle.Wait(); err != nil {
				t.Fatalf("StreamCommand(%q) wait: %v", tc.command, err)
			}
			if tc.wantArgv != nil {
				if len(factory.shell) != 0 {
					t.Fatalf("StreamCommand(%q) ran through the shell: %q", tc.command, factory.shell)
				}
				if len(factory.argv) != 1 || !slices.Equal(factory.argv[0], tc.wantArgv) {
					t.Fatalf("StreamCommand(%q) argv = %q, want %q", tc.command, factory.argv, tc.wantArgv)
				}
				return
			}
			if len(factory.argv) != 0 {
				t.Fatalf("StreamCommand(%q) ran as argv %q, want the shell", tc.command, factory.argv)
			}
			if !slices.Equal(factory.shell, []string{tc.command}) {
				t.Fatalf("StreamCommand(%q) shell = %q, want the command unchanged", tc.command, factory.shell)
			}
		})
	}
}

// TestStreamCommandMatchesTheShell pins that the shell tool's route change did
// not change what the caller sees: for each command string, the streamed output
// and exit status are the ones the forced shell run produces. bash is the oracle,
// and the commands exercise the shell constructs StreamCommand must still route
// to the shell as well as the bare word lists it now runs as argv.
func TestStreamCommandMatchesTheShell(t *testing.T) {
	t.Parallel()
	if runtimeGOOS == "windows" {
		t.Skip("POSIX shell contract does not apply on windows")
	}

	root := t.TempDir()
	bin := fixtureBin(t)
	writeExecFixture(t, filepath.Join(bin, "say"),
		"#!/bin/sh\nprintf 'out:%s\\n' \"$1\"\nprintf 'err:%s\\n' \"$2\" >&2\nexit 3\n")

	cases := []struct{ command string }{
		{`say 'a b' c d`},
		{`say "it's here" 'x y'`},
		{`say e\ f g`},
		{"say '' \"\""},
		{`say one two | cat`},
		{`say one two; say three four`},
		{`say a b 2>&1`},
		{`SAY_VAR=x say "$SAY_VAR" b`},
		{`say $(printf sub) b`},
	}

	for _, tc := range cases {
		command := tc.command
		t.Run(command, func(t *testing.T) {
			t.Parallel()
			stream := func(forceShell bool) (string, int, error) {
				env := NewLocalExecutionEnvironment(root)
				t.Cleanup(env.Cleanup)
				env.inheritedEnv = func() []string { return fixturePathEnv(bin, "/usr/local/bin", "/usr/bin", "/bin") }
				dir, childEnv, err := env.prepareStreamCommand(context.Background(), "", nil)
				if err != nil {
					t.Fatalf("%q: prepareStreamCommand: %v", command, err)
				}
				cmd := env.commands().Shell(command)
				if !forceShell {
					cmd, childEnv = env.commandRuntimeForCommand(command, dir, childEnv)
				}
				var buf strings.Builder
				handle, err := env.startStreamCommand(context.Background(), cmd, dir, childEnv, &buf)
				if err != nil {
					return buf.String(), 0, err
				}
				code, err := handle.Wait()
				return buf.String(), code, err
			}
			wantOut, wantCode, wantErr := stream(true)
			gotOut, gotCode, gotErr := stream(false)
			if (gotErr == nil) != (wantErr == nil) {
				t.Fatalf("%q: error %v, the shell error %v", command, gotErr, wantErr)
			}
			if gotOut != wantOut || gotCode != wantCode {
				t.Fatalf("%q: gave %q exit %d, the shell gave %q exit %d", command, gotOut, gotCode, wantOut, wantCode)
			}
		})
	}
}

// recordingCommandFactory records which command-runtime entry point the
// environment chose: shell holds each command string passed to Shell, argv
// holds each program and argument vector passed to Argv. With delegate set it
// returns that factory's real runtime, so one test can pin the route and the
// behaviour the route produces together.
type recordingCommandFactory struct {
	shell    []string
	argv     [][]string
	delegate commandRuntimeFactory
}

func (f *recordingCommandFactory) Shell(command string) commandRuntime {
	f.shell = append(f.shell, command)
	if f.delegate != nil {
		return f.delegate.Shell(command)
	}
	return inertCommandRuntime{}
}

func (f *recordingCommandFactory) Argv(name string, args ...string) commandRuntime {
	f.argv = append(f.argv, append([]string{name}, args...))
	if f.delegate != nil {
		return f.delegate.Argv(name, args...)
	}
	return inertCommandRuntime{}
}

// inertCommandRuntime starts, produces no output, and exits zero, so a test can
// observe the runtime choice without forking.
type inertCommandRuntime struct{}

func (inertCommandRuntime) Args() []string                 { return nil }
func (inertCommandRuntime) Configure(commandRuntimeConfig) {}
func (inertCommandRuntime) Start() error                   { return nil }
func (inertCommandRuntime) Wait() error                    { return nil }
func (inertCommandRuntime) PID() int                       { return 0 }
func (inertCommandRuntime) ExitCode(error) (int, bool)     { return 0, false }
func (inertCommandRuntime) Terminate()                     {}
func (inertCommandRuntime) Kill()                          {}
