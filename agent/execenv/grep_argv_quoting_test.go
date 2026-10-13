package execenv

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/execsupport/shellquote"
)

// TestGrepPassesRipgrepArgsAsArgv pins the fix for the Windows cmd.exe quoting
// finding: Grep must hand ripgrep a real argument vector, never a shell command
// string. ExecCommand on Windows runs its line through cmd.exe, where POSIX
// single quotes are ordinary text and '&' stays a command separator and '%VAR%'
// still expands. With ExecArgv there is no shell on any platform, so no byte of
// the pattern, directory, or glob filter can ever be active.
//
// The same table also pins that this portability fix did not disturb the POSIX
// rendering callers still rely on (round two's disposition: shellquote.Literal
// leaves non-ASCII bytes bare).
func TestGrepPassesRipgrepArgsAsArgv(t *testing.T) {
	const rgPath = "/fixture/rg"
	cases := []struct {
		name    string
		pattern string
		path    string
		glob    string
		// posix is shellquote.Literal's current rendering of pattern; it must
		// not move. "%VAR%" is deliberately left bare by the allow-list (which
		// is exactly why cmd.exe used to expand it).
		posix string
	}{
		{
			name:    "cmd command separator",
			pattern: "foo&whoami",
			posix:   `'foo&whoami'`,
		},
		{
			name:    "cmd variable expansion",
			pattern: "%VAR%",
			posix:   "%VAR%",
		},
		{
			name:    "cmd pipe and redirects with glob",
			pattern: "a|b>c<d",
			glob:    "*.go",
			posix:   `'a|b>c<d'`,
		},
		{
			name:    "ordinary accented path",
			pattern: "needle",
			path:    "café",
			posix:   "needle",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			rec := &grepArgvRecorder{}
			env := NewLocalExecutionEnvironment(root)
			env.EnvPolicy = EnvPolicyNone
			env.commandFactory = rec
			env.lookPath = func(name string) (string, error) {
				if name != "rg" {
					t.Fatalf("unexpected lookup %q", name)
				}
				return rgPath, nil
			}

			if _, err := env.Grep(context.Background(), tc.pattern, tc.path, tc.glob, false, 0, ""); err != nil {
				t.Fatalf("Grep: %v", err)
			}

			// The Windows-relevant property: Grep never builds a shell command
			// string, so cmd.exe has nothing to parse on any platform.
			if len(rec.shellCommands) != 0 {
				t.Fatalf("Grep ran through a shell: %q", rec.shellCommands)
			}
			// The rg arm also runs rg --files listings to count the over-cap
			// files its note discloses (#4574 round 5), so the pin is the one
			// call that carries the search pattern.
			var search []string
			for _, call := range rec.argvAll {
				if call[0] != rgPath {
					t.Fatalf("Grep runtime invoked %q, want %q", call[0], rgPath)
				}
				if slices.Contains(call[1:], tc.pattern) {
					if search != nil {
						t.Fatalf("Grep handed ripgrep the pattern twice: %q", rec.argvAll)
					}
					search = call[1:]
				}
			}
			if search == nil {
				t.Fatalf("Grep runtime = argv calls %d, want a search call carrying %q for %q", len(rec.argvAll), tc.pattern, rgPath)
			}

			// The raw bytes reach ripgrep as discrete, byte-identical argv
			// elements; nothing is quoted, split, or expanded.
			if !slices.Contains(search, tc.pattern) {
				t.Fatalf("pattern not delivered as one argv element: %q in %q", tc.pattern, search)
			}
			if tc.glob != "" && !slices.Contains(search, tc.glob) {
				t.Fatalf("glob filter not delivered as one argv element: %q in %q", tc.glob, search)
			}
			if tc.path != "" {
				dir := filepath.Join(root, tc.path)
				if !slices.Contains(search, dir) {
					t.Fatalf("directory not delivered as one argv element: %q in %q", dir, search)
				}
			}
			// No POSIX-quoted rendering of the metacharacter-bearing tokens can
			// appear anywhere in the vector.
			if quoted := shellquote.Literal(tc.pattern); quoted != tc.pattern && slices.Contains(search, quoted) {
				t.Fatalf("POSIX-quoted pattern reached the argument vector: %q", quoted)
			}

			// The POSIX rendering itself is unchanged.
			if got := shellquote.Literal(tc.pattern); got != tc.posix {
				t.Fatalf("shellquote.Literal(%q) = %q, want %q", tc.pattern, got, tc.posix)
			}
			if got := shellquote.Literal("café"); got != "café" {
				t.Fatalf("shellquote.Literal(accented) = %q, want it left bare", got)
			}
		})
	}
}

// grepArgvRecorder records which command-runtime entry point Grep chose. Shell
// records the rendered command line (a failure for Grep); Argv records the
// program name and argument vector.
type grepArgvRecorder struct {
	shellCommands []string
	argvCalls     int
	argvName      string
	argvArgs      []string
	// argvAll holds every argv invocation's name and arguments, in order:
	// the rg arm legitimately makes more than one call — its oversize
	// disclosure listings — and the quoting pins need the search call's
	// vector specifically.
	argvAll [][]string
}

func (r *grepArgvRecorder) Shell(command string) commandRuntime {
	r.shellCommands = append(r.shellCommands, command)
	return &grepArgvRuntime{}
}

func (r *grepArgvRecorder) Argv(name string, args ...string) commandRuntime {
	r.argvCalls++
	r.argvName = name
	r.argvArgs = append([]string(nil), args...)
	r.argvAll = append(r.argvAll, append([]string{name}, args...))
	return &grepArgvRuntime{}
}

// grepArgvRuntime is an inert commandRuntime: it starts, produces no output, and
// exits zero, so Grep's success path is exercised without forking.
type grepArgvRuntime struct{}

func (c *grepArgvRuntime) Args() []string                 { return nil }
func (c *grepArgvRuntime) Configure(commandRuntimeConfig) {}
func (c *grepArgvRuntime) Start() error                   { return nil }
func (c *grepArgvRuntime) Wait() error                    { return nil }
func (c *grepArgvRuntime) PID() int                       { return 0 }
func (c *grepArgvRuntime) ExitCode(error) (int, bool)     { return 0, false }
func (c *grepArgvRuntime) Terminate()                     {}
func (c *grepArgvRuntime) Kill()                          {}
