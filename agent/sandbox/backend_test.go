package sandbox

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestNewWrapperRejectsNonEnforcingBackend(t *testing.T) {
	if _, err := NewWrapper(ResolvedPolicy{Backend: BackendNone}, "/usr/bin/bwrap", "/tmp/s"); err == nil {
		t.Error("expected NewWrapper to refuse the non-enforcing BackendNone")
	}
}

func TestNewWrapperAcceptsSeatbeltBackend(t *testing.T) {
	if _, err := NewWrapper(ResolvedPolicy{Backend: BackendSeatbelt}, "/usr/bin/sandbox-exec", "/tmp/s"); err != nil {
		t.Errorf("NewWrapper must accept the seatbelt backend: %v", err)
	}
}

func TestNewWrapperRejectsRelativeBinaryPath(t *testing.T) {
	for _, b := range []Backend{BackendBwrap, BackendSeatbelt} {
		if _, err := NewWrapper(ResolvedPolicy{Backend: b}, "bwrap", "/tmp/s"); err == nil {
			t.Fatalf("backend %v: expected NewWrapper to refuse a cwd-relative binary path (PATH-injection defense)", b)
		}
		if _, err := NewWrapper(ResolvedPolicy{Backend: b}, "./bin/sandbox-exec", "/tmp/s"); err == nil {
			t.Fatalf("backend %v: expected NewWrapper to refuse a cwd-relative binary path (PATH-injection defense)", b)
		}
	}
}

func TestWrapPrependsBwrapAndSeparatesCommand(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}

	argv := []string{"/bin/bash", "-c", "echo hi"}
	got := w.Wrap(argv, cwd)

	if got[0] != "/usr/bin/bwrap" {
		t.Errorf("wrapped argv must start with the bwrap binary, got %q", got[0])
	}
	// --argv0 preserves the command's own argv[0].
	if !hasSeq(got, "--argv0", "/bin/bash") {
		t.Errorf("expected --argv0 /bin/bash: %v", got)
	}
	// Everything after the LAST "--" is the original command, unmodified.
	sep := slices.Index(got, "--")
	if sep < 0 {
		t.Fatalf("wrapped argv missing the -- command separator: %v", got)
	}
	if !slices.Equal(got[sep+1:], argv) {
		t.Errorf("command after -- = %v, want %v", got[sep+1:], argv)
	}
	// The bwrap flags carry the confinement.
	if !hasSeq(got, "--unshare-pid") || !hasSeq(got, "--proc", "/proc") {
		t.Errorf("wrapped argv missing confinement flags: %v", got)
	}
}

func TestWrapNilIsIdentity(t *testing.T) {
	var w *Wrapper
	argv := []string{"/bin/echo", "hi"}
	if got := w.Wrap(argv, "/somewhere"); !slices.Equal(got, argv) {
		t.Errorf("nil wrapper must be identity, got %v", got)
	}
}

// TestConfineWrapsBwrapAndLeavesDir pins that Confine rewrites the command's argv
// with the backend invocation but does NOT touch cmd.Dir for bwrap — bwrap carries
// the working directory in its own argv (--chdir), so the caller's cmd.Dir is left
// as-is. (The Seatbelt cmd.Dir path is exercised on darwin by TestConfineSetsSeatbeltDir.)
func TestConfineWrapsBwrapAndLeavesDir(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	cmd := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	orig := slices.Clone(cmd.Args)
	w.Confine(cmd, cwd)

	if cmd.Args[0] != "/usr/bin/bwrap" || cmd.Path != "/usr/bin/bwrap" {
		t.Errorf("Confine must prepend the bwrap binary: args[0]=%q path=%q", cmd.Args[0], cmd.Path)
	}
	sep := slices.Index(cmd.Args, "--")
	if sep < 0 || !slices.Equal(cmd.Args[sep+1:], orig) {
		t.Errorf("original command must survive after --: %v", cmd.Args)
	}
	if cmd.Dir != "" {
		t.Errorf("Confine must not set cmd.Dir for bwrap (uses --chdir), got %q", cmd.Dir)
	}
}

// TestConfineNilIsIdentity pins that a nil wrapper leaves the command untouched,
// so an unsandboxed spawn is byte-identical to before.
func TestConfineNilIsIdentity(t *testing.T) {
	var w *Wrapper
	cmd := exec.Command("/bin/echo", "hi") //nolint:noctx // test-only cmd, never run
	want := slices.Clone(cmd.Args)
	w.Confine(cmd, "/somewhere")
	if !slices.Equal(cmd.Args, want) || cmd.Dir != "" {
		t.Errorf("nil wrapper must be identity, got args=%v dir=%q", cmd.Args, cmd.Dir)
	}
}

// TestConfineTrustedInfraKeepsNetworkUnderNetOff pins kata 83pm: MCP servers are
// trusted infrastructure (launched only from config layers the model cannot
// write), so under a net=off session policy Confine still severs the network
// (--unshare-net) for an ordinary spawned process, but ConfineTrustedInfra does
// NOT — while both retain identical filesystem confinement (the base hardening
// flags and the worktree bind survive either path).
func TestConfineTrustedInfraKeepsNetworkUnderNetOff(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, false)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}

	ordinary := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	w.Confine(ordinary, cwd)
	if !slices.Contains(ordinary.Args, "--unshare-net") {
		t.Errorf("model-authored spawn must be network-severed under net=off: %v", ordinary.Args)
	}

	infra := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	w.ConfineTrustedInfra(infra, cwd)
	if slices.Contains(infra.Args, "--unshare-net") {
		t.Errorf("trusted-infrastructure spawn (MCP) must keep network under net=off: %v", infra.Args)
	}
	// Filesystem confinement is unchanged by the network carve-out.
	if !slices.Contains(infra.Args, "--unshare-pid") || !hasSeq(infra.Args, "--bind", cwd, cwd) {
		t.Errorf("trusted-infrastructure spawn must keep filesystem confinement: %v", infra.Args)
	}
}

// TestConfineTrustedInfraNilIsIdentity mirrors TestConfineNilIsIdentity for the
// trusted-infra entry point.
func TestConfineTrustedInfraNilIsIdentity(t *testing.T) {
	var w *Wrapper
	cmd := exec.Command("/bin/echo", "hi") //nolint:noctx // test-only cmd, never run
	want := slices.Clone(cmd.Args)
	w.ConfineTrustedInfra(cmd, "/somewhere")
	if !slices.Equal(cmd.Args, want) || cmd.Dir != "" {
		t.Errorf("nil wrapper must be identity, got args=%v dir=%q", cmd.Args, cmd.Dir)
	}
}

// childEnvRecords reads the private child-env fd a confined command carries and
// returns its NUL-separated records. It decodes the payload itself rather than
// reusing the production encoder, so a test proves the wire format. The fd is
// closed at test end: a unit test never starts the launcher that would consume
// it.
func childEnvRecords(t *testing.T, cmd *exec.Cmd) []string {
	t.Helper()
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("confined command must carry exactly one private child-env fd, got %d", len(cmd.ExtraFiles))
	}
	return fdRecords(t, cmd.ExtraFiles[0])
}

// fdRecords reads one private child-env fd and returns its NUL-separated
// records. The fd is closed at test end: a unit test never starts the launcher
// that would consume it.
func fdRecords(t *testing.T, f *os.File) []string {
	t.Helper()
	t.Cleanup(func() { _ = f.Close() })
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("seek private child-env fd: %v", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read private child-env fd: %v", err)
	}
	records := strings.Split(string(data), "\x00")
	if n := len(records); n > 0 && records[n-1] == "" {
		records = records[:n-1]
	}
	return records
}

// seatbeltChildEnv decodes the Seatbelt reader's "-NAME"/"+NAME=VALUE" records
// from the private fd at index i of cmd.ExtraFiles.
func seatbeltChildEnv(t *testing.T, cmd *exec.Cmd, i int) (map[string]string, []string) {
	t.Helper()
	if i >= len(cmd.ExtraFiles) {
		t.Fatalf("command carries %d extra files, want the child-env fd at index %d", len(cmd.ExtraFiles), i)
	}
	set := map[string]string{}
	var unset []string
	for _, rec := range fdRecords(t, cmd.ExtraFiles[i]) {
		switch {
		case strings.HasPrefix(rec, "-"):
			unset = append(unset, rec[1:])
		case strings.HasPrefix(rec, "+"):
			name, value, ok := strings.Cut(rec[1:], "=")
			if !ok {
				t.Fatalf("malformed seatbelt child-env record %q", rec)
			}
			set[name] = value
		default:
			t.Fatalf("unexpected seatbelt child-env record %q", rec)
		}
	}
	return set, unset
}

// bwrapChildEnv decodes the bwrap --args records the private fd carries.
func bwrapChildEnv(t *testing.T, cmd *exec.Cmd) (map[string]string, []string) {
	t.Helper()
	records := childEnvRecords(t, cmd)
	set := map[string]string{}
	var unset []string
	for i := 0; i < len(records); {
		switch records[i] {
		case "--setenv":
			if i+2 >= len(records) {
				t.Fatalf("truncated --setenv record: %q", records)
			}
			set[records[i+1]] = records[i+2]
			i += 3
		case "--unsetenv":
			if i+1 >= len(records) {
				t.Fatalf("truncated --unsetenv record: %q", records)
			}
			unset = append(unset, records[i+1])
			i += 2
		default:
			t.Fatalf("unexpected child-env record %q", records[i])
		}
	}
	return set, unset
}

// assertNoArgvValue fails when any element of args contains want: the launcher
// command line must never carry a configured secret value.
func assertNoArgvValue(t *testing.T, args []string, want string) {
	t.Helper()
	if joined := strings.Join(args, "\x00"); strings.Contains(joined, want) {
		t.Errorf("value %q is visible in the launcher argv: %v", want, args)
	}
}

// TestConfineWithChildEnvAppliesConfigBehindBwrap is the backend half of the
// rule that a server's configured environment must never reach the sandbox
// LAUNCHER: bwrap is dynamically linked, so LD_PRELOAD et al. in its own
// environment run before any namespace or mount exists. The assignments ride a
// private fd (bwrap --args) that bwrap applies to the child after confinement is
// in place, so neither the launcher's environment nor its argv carries them, and
// the argv after "--" stays the original command.
func TestConfineWithChildEnvAppliesConfigBehindBwrap(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	const hidden = "/tmp/evil.so"
	cmd := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	orig := slices.Clone(cmd.Args)
	if err := w.ConfineWithChildEnv(cmd, cwd, ChildEnvDelta{Set: map[string]string{"LD_PRELOAD": hidden, "MY_SERVER_FLAG": "1"}}); err != nil {
		t.Fatalf("ConfineWithChildEnv: %v", err)
	}

	if cmd.Path != "/usr/bin/bwrap" {
		t.Errorf("ConfineWithChildEnv must still prepend the backend: path=%q", cmd.Path)
	}
	// The command itself is untouched and still last.
	sep := slices.Index(cmd.Args, "--")
	if sep < 0 || !slices.Equal(cmd.Args[sep+1:], orig) {
		t.Fatalf("original command must survive after --: %v", cmd.Args)
	}
	// issue #7009: the value must not be on the launcher command line, where
	// host process-list diagnostics sample it.
	assertNoArgvValue(t, cmd.Args, hidden)
	// The launcher is told where the private channel is.
	if !hasSeq(cmd.Args[:sep], "--args", "3") {
		t.Errorf("bwrap must be pointed at the private child-env fd with --args 3: %v", cmd.Args)
	}
	got, _ := bwrapChildEnv(t, cmd)
	for name, want := range map[string]string{"LD_PRELOAD": "/tmp/evil.so", "MY_SERVER_FLAG": "1"} {
		if got[name] != want {
			t.Errorf("private child-env fd set %s = %q, want %q", name, got[name], want)
		}
	}
	// The trusted-infra entry point carries the same child env and the network
	// carve-out, so a net=off policy still delivers config to the server.
	rpOff, cwdOff, _ := resolveFixture(t, ModeWorkspaceWrite, false)
	wOff, err := NewWrapper(rpOff, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	infra := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	if err := wOff.ConfineTrustedInfraWithChildEnv(infra, cwdOff, ChildEnvDelta{Set: map[string]string{"MY_SERVER_FLAG": "1"}}); err != nil {
		t.Fatalf("ConfineTrustedInfraWithChildEnv: %v", err)
	}
	if slices.Contains(infra.Args, "--unshare-net") {
		t.Errorf("trusted-infrastructure child env must keep the network carve-out: %v", infra.Args)
	}
	if set, _ := bwrapChildEnv(t, infra); set["MY_SERVER_FLAG"] != "1" {
		t.Errorf("trusted-infrastructure child env must still be delivered: %v", infra.Args)
	}
}

// TestConfineWithChildEnvUnsetsFloorRemovedNames is the bwrap half of Medium
// #2's proof at the backend.
//
// bwrap starts the confined child from the LAUNCHER's environment, so a name the
// floor removed from the child's environment would reach the child anyway
// unless the child-env channel says otherwise. --unsetenv is that statement,
// delivered over the private fd beside the assignments. The reductions arrive
// sorted so the payload stays deterministic.
func TestConfineWithChildEnvUnsetsFloorRemovedNames(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	cmd := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	orig := slices.Clone(cmd.Args)
	if err := w.ConfineWithChildEnv(cmd, cwd, ChildEnvDelta{
		Set:   map[string]string{"npm_config_cache": "/tmp/scratch/npm"},
		Unset: []string{"SSH_AUTH_SOCK", "AWS_SECRET_ACCESS_KEY"},
	}); err != nil {
		t.Fatalf("ConfineWithChildEnv: %v", err)
	}

	sep := slices.Index(cmd.Args, "--")
	if sep < 0 || !slices.Equal(cmd.Args[sep+1:], orig) {
		t.Fatalf("original command must survive after --: %v", cmd.Args)
	}
	set, unset := bwrapChildEnv(t, cmd)
	if !slices.Equal(unset, []string{"AWS_SECRET_ACCESS_KEY", "SSH_AUTH_SOCK"}) {
		t.Errorf("child-env unsets = %v, want both floor-removed names in sorted order", unset)
	}
	if got := set["npm_config_cache"]; got != "/tmp/scratch/npm" {
		t.Errorf("child-env set npm_config_cache = %q, want the derived redirect", got)
	}
	// Both halves ride the private fd, never the launcher's command line.
	for _, name := range unset {
		assertNoArgvValue(t, cmd.Args, name)
	}

	// An unset-only delta still gets a channel; a delta with neither half leaves
	// the argv and fds untouched, so no backend pays for an unused file.
	unsetOnly := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	if err := w.ConfineWithChildEnv(unsetOnly, cwd, ChildEnvDelta{Unset: []string{"DOCKER_HOST"}}); err != nil {
		t.Fatalf("ConfineWithChildEnv: %v", err)
	}
	if _, unset := bwrapChildEnv(t, unsetOnly); !slices.Equal(unset, []string{"DOCKER_HOST"}) {
		t.Errorf("an unset-only delta delivered %v, want [DOCKER_HOST]", unset)
	}
	empty := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
	want := slices.Clone(empty.Args)
	if err := w.ConfineWithChildEnv(empty, cwd, ChildEnvDelta{}); err != nil {
		t.Fatalf("ConfineWithChildEnv: %v", err)
	}
	if sep := slices.Index(empty.Args, "--"); sep < 0 || !slices.Equal(empty.Args[sep+1:], want) || slices.Contains(empty.Args, "--args") || empty.ExtraFiles != nil {
		t.Errorf("an empty delta must leave the child argv unchanged: %v", empty.Args)
	}
}

// TestChildEnvArgvSeatbeltUsesReaderAfterSeparator pins the Seatbelt half: that
// backend has no args-from-fd flag, so a fixed reader on the confined side of
// the "--" applies the assignments from the private fd. sandbox-exec itself
// keeps the launcher env, and no value reaches the argv.
func TestChildEnvArgvSeatbeltUsesReaderAfterSeparator(t *testing.T) {
	argv := []string{"/usr/bin/sandbox-exec", "-p", "(version 1)", "--", "/bin/bash", "-c", "echo hi"}
	got := childEnvArgv(BackendSeatbelt, argv, 3, 3)
	want := []string{
		"/usr/bin/sandbox-exec", "-p", "(version 1)", "--",
		childEnvReader, "-e", childEnvScript(3),
		"/bin/bash", "-c", "echo hi",
	}
	if !slices.Equal(got, want) {
		t.Errorf("childEnvArgv =\n%v\nwant\n%v", got, want)
	}
	if script := childEnvScript(3); !strings.Contains(script, "my $fd = 3;") || !strings.Contains(script, "close $fh;") || !strings.Contains(script, "exec { $ARGV[0] } @ARGV;") {
		t.Errorf("seatbelt reader must read the private fd, close it, and exec the original command: %q", script)
	}
	if script := childEnvScript(7); !strings.Contains(script, "my $fd = 7;") {
		t.Errorf("seatbelt reader must use the fd it is given: %q", script)
	}
	// Nothing from the delta can reach this argv, because it carries none.
	if joined := strings.Join(got, "\x00"); strings.Contains(joined, "LD_PRELOAD") {
		t.Errorf("seatbelt wrapper argv must carry no assignments: %v", got)
	}
}

// TestChildEnvPayloadRecords pins both wire formats: bwrap's --args consumes
// bwrap flags, while the Seatbelt helper reads "-NAME" and "+NAME=VALUE".
// Unsets come first and names are sorted, so the payload is deterministic.
func TestChildEnvPayloadRecords(t *testing.T) {
	delta := ChildEnvDelta{
		Set:   map[string]string{"MULTI": "line1\nline2", "LD_PRELOAD": "/tmp/evil.so"},
		Unset: []string{"SSH_AUTH_SOCK"},
	}
	bwrap := strings.TrimSuffix(string(childEnvPayload(BackendBwrap, delta)), "\x00")
	wantBwrap := strings.Join([]string{
		"--unsetenv", "SSH_AUTH_SOCK",
		"--setenv", "LD_PRELOAD", "/tmp/evil.so",
		"--setenv", "MULTI", "line1\nline2",
	}, "\x00")
	if bwrap != wantBwrap {
		t.Errorf("bwrap payload = %q, want %q", bwrap, wantBwrap)
	}
	seatbelt := strings.TrimSuffix(string(childEnvPayload(BackendSeatbelt, delta)), "\x00")
	wantSeatbelt := strings.Join([]string{
		"-SSH_AUTH_SOCK",
		"+LD_PRELOAD=/tmp/evil.so",
		"+MULTI=line1\nline2",
	}, "\x00")
	if seatbelt != wantSeatbelt {
		t.Errorf("seatbelt payload = %q, want %q", seatbelt, wantSeatbelt)
	}
}

// TestChildEnvScriptAppliesTheFdWithoutEvaluatingValues proves the Seatbelt
// reader applies assignments from the private fd and treats a value as data: a
// value carrying shell syntax must arrive unchanged, a dotted name (legal for a
// process environment but not for a shell builtin) must be set, an empty value
// must stay empty, and an unset name must be gone. It runs the same script
// macOS's /usr/bin/perl executes, using the host perl.
func TestChildEnvScriptAppliesTheFdWithoutEvaluatingValues(t *testing.T) {
	reader, err := exec.LookPath(childEnvReader)
	if err != nil {
		t.Skipf("child-env reader %s not installed: %v", childEnvReader, err)
	}
	const value = "line1\nline2 $HOME $(id) `id`"
	f, err := childEnvFile(BackendSeatbelt, ChildEnvDelta{
		Set: map[string]string{
			"SECRET_VALUE":       value,
			"logging.level.root": "debug",
			"EMPTY_VALUE":        "",
		},
		Unset: []string{"AMBIENT_SECRET"},
	})
	if err != nil {
		t.Fatalf("childEnvFile: %v", err)
	}
	t.Cleanup(func() { _ = f.Close() })
	cmd := exec.Command(reader, "-e", childEnvScript(3), "/usr/bin/env")
	cmd.Env = []string{"AMBIENT_SECRET=ambient", "PATH=/usr/bin:/bin"}
	cmd.ExtraFiles = []*os.File{f}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child-env script: %v: %s", err, out)
	}
	env := string(out)
	for _, want := range []string{
		"SECRET_VALUE=" + value + "\n",
		"logging.level.root=debug\n",
		"EMPTY_VALUE=\n",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("child environment is missing %q:\n%s", want, env)
		}
	}
	if strings.Contains(env, "AMBIENT_SECRET=") {
		t.Errorf("unset name must not reach the child:\n%s", env)
	}
	// The reader closes the private fd before exec, so the server inherits no
	// extra descriptor.
	if _, err := os.Stat("/dev/fd"); err == nil {
		probe, err := childEnvFile(BackendSeatbelt, ChildEnvDelta{Set: map[string]string{"PROBE": "x"}})
		if err != nil {
			t.Fatalf("childEnvFile: %v", err)
		}
		t.Cleanup(func() { _ = probe.Close() })
		fdCheck := exec.Command(reader, "-e", childEnvScript(3), "/bin/sh", "-c", `if [ -e /dev/fd/3 ]; then echo open; else echo closed; fi`)
		fdCheck.ExtraFiles = []*os.File{probe}
		fdOut, err := fdCheck.CombinedOutput()
		if err != nil {
			t.Fatalf("fd check: %v: %s", err, fdOut)
		}
		if strings.TrimSpace(string(fdOut)) != "closed" {
			t.Errorf("child inherited the private fd: %s", fdOut)
		}
	}
}

// TestChildEnvRejectsEmbeddedNUL is the review fix for the private channel's
// framing: the payload is NUL-delimited, so a configured name or value carrying
// a NUL byte would split one record into several and let the trailing bytes be
// read as launcher flags by bwrap's --args (or as another assignment by the
// Seatbelt reader). The spawn is refused before anything is encoded, the
// command is left untouched, and the error names the variable without echoing
// its value.
func TestChildEnvRejectsEmbeddedNUL(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeWorkspaceWrite, true)
	w, err := NewWrapper(rp, "/usr/bin/bwrap", "/tmp/evener-session")
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	const injected = "fixture-value-before-the-nul"
	cases := []struct {
		name     string
		delta    ChildEnvDelta
		wantName string
	}{
		{"set value", ChildEnvDelta{Set: map[string]string{"MCP_TOKEN": injected + "\x00--args"}}, "MCP_TOKEN"},
		{"set name", ChildEnvDelta{Set: map[string]string{"MCP\x00TOKEN": "x"}}, "MCP"},
		{"unset name", ChildEnvDelta{Unset: []string{"MCP\x00TOKEN"}}, "MCP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("/bin/bash", "-c", "echo hi") //nolint:noctx // test-only cmd, never run
			orig := slices.Clone(cmd.Args)
			err := w.ConfineWithChildEnv(cmd, cwd, tc.delta)
			if err == nil {
				t.Fatal("a child env carrying a NUL byte must be refused")
			}
			if !strings.Contains(err.Error(), tc.wantName) {
				t.Errorf("error must name the offending variable: %v", err)
			}
			if strings.Contains(err.Error(), injected) {
				t.Errorf("error must not echo the value: %v", err)
			}
			if !slices.Equal(cmd.Args, orig) || cmd.ExtraFiles != nil {
				t.Errorf("a refused spawn must leave the command untouched: args=%v extraFiles=%v", cmd.Args, cmd.ExtraFiles)
			}
		})
	}
	// The unconfined path frames no records, but a NUL still cannot survive an
	// exec, so it refuses there too rather than half-applying the delta.
	var nilWrapper *Wrapper
	cmd := exec.Command("/bin/echo", "hi") //nolint:noctx // test-only cmd, never run
	if err := nilWrapper.ConfineWithChildEnv(cmd, "", ChildEnvDelta{Set: map[string]string{"MCP_TOKEN": injected + "\x00x"}}); err == nil {
		t.Error("a NUL byte must be refused on the unconfined path too")
	}
}

// TestConfineWithChildEnvWiresSeatbeltReader proves the production Seatbelt
// wiring on a host that cannot run sandbox-exec: it drives the real
// ConfineWithChildEnv, so the test fails if the wrapper stops attaching the
// payload fd or stops splicing the reader at the fd the payload actually landed
// on. Only the platform-specific policy rendering is stubbed (wrapSeatbelt is
// the package's existing seam); the same argv builder the darwin path uses still
// produces the sandbox-exec command. The live darwin suite proves the whole
// thing against the real kernel.
func TestConfineWithChildEnvWiresSeatbeltReader(t *testing.T) {
	rp, cwd, _ := resolveFixture(t, ModeRestricted, true)
	rp.Backend = BackendSeatbelt
	sessionTmp := t.TempDir()
	realWrap := wrapSeatbelt
	wrapSeatbelt = func(argv []string, stubPolicy ResolvedPolicy, stubTmp, _ string) ([]string, error) {
		text, params := SeatbeltPolicy(stubPolicy, stubTmp, nil)
		return seatbeltArgs("/usr/bin/sandbox-exec", text, params, argv), nil
	}
	t.Cleanup(func() { wrapSeatbelt = realWrap })
	w, err := NewWrapper(rp, "/usr/bin/sandbox-exec", sessionTmp)
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}

	const secret = "seatbelt-fixture-secret-value-7009"
	cmd := exec.Command("/usr/bin/env") //nolint:noctx // test-only cmd, never run
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	orig := slices.Clone(cmd.Args)
	// A pre-existing extra file makes the payload land on child fd 4, so the
	// reader script must name the fd ConfineWithChildEnv computed, not a
	// hardcoded 3.
	prior, err := os.CreateTemp(t.TempDir(), "prior-fd-*")
	if err != nil {
		t.Fatalf("create prior extra fd: %v", err)
	}
	t.Cleanup(func() { _ = prior.Close() })
	cmd.ExtraFiles = []*os.File{prior}

	if err := w.ConfineWithChildEnv(cmd, cwd, ChildEnvDelta{
		Set:   map[string]string{"logging.level.root": "debug", "MCP_SERVER_TOKEN": secret},
		Unset: []string{"SSH_AUTH_SOCK"},
	}); err != nil {
		t.Fatalf("ConfineWithChildEnv: %v", err)
	}

	if len(cmd.ExtraFiles) != 2 {
		t.Fatalf("confined command must gain exactly one private env fd, got %d extra files", len(cmd.ExtraFiles))
	}
	set, unset := seatbeltChildEnv(t, cmd, 1)
	if set["MCP_SERVER_TOKEN"] != secret || set["logging.level.root"] != "debug" {
		t.Errorf("private child-env fd did not carry the assignments: set=%v unset=%v", set, unset)
	}
	if !slices.Equal(unset, []string{"SSH_AUTH_SOCK"}) {
		t.Errorf("private child-env fd unsets = %v, want [SSH_AUTH_SOCK]", unset)
	}

	reader := slices.Index(cmd.Args, childEnvReader)
	if reader < 0 || reader+2 >= len(cmd.Args) || cmd.Args[reader+1] != "-e" {
		t.Fatalf("seatbelt wrapper must invoke the fd reader with a script: %v", cmd.Args)
	}
	if want := "my $fd = 4;"; !strings.Contains(cmd.Args[reader+2], want) {
		t.Errorf("reader script must name the fd the payload landed on (%q): %q", want, cmd.Args[reader+2])
	}
	if !slices.Equal(cmd.Args[reader+3:], orig) {
		t.Errorf("original command must follow the reader untouched: %v", cmd.Args)
	}
	// The assignments are on the far side of the launcher: neither its argv nor
	// its environment may carry the secret.
	assertNoArgvValue(t, cmd.Args, secret)
	for _, kv := range cmd.Env {
		if strings.Contains(kv, secret) {
			t.Errorf("configured secret is in the launcher environment: %q", kv)
		}
	}
}

// TestChildEnvFdRoundTripsThroughRealBwrap proves the Linux path end to end:
// a configured value reaches the confined child's environment while staying off
// the launcher's command line.
func TestChildEnvFdRoundTripsThroughRealBwrap(t *testing.T) {
	facts := requireRealBwrap(t)
	facts.Home = t.TempDir()
	cwd := materializeWorkspace(t, MainCheckout)
	net := true
	rp, err := Resolve(SandboxPolicy{Mode: ModeWorkspaceWrite, Network: &net}, facts, cwd)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	w, err := NewWrapper(rp, facts.BwrapPath, t.TempDir())
	if err != nil {
		t.Fatalf("NewWrapper: %v", err)
	}
	const secret = "child-env-fixture-value-4e11"
	cmd := exec.Command("/bin/sh", "-c", `printf '%s' "$MCP_SERVER_TOKEN"`)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	if err := w.ConfineWithChildEnv(cmd, cwd, ChildEnvDelta{Set: map[string]string{"MCP_SERVER_TOKEN": secret}}); err != nil {
		t.Fatalf("ConfineWithChildEnv: %v", err)
	}
	t.Cleanup(func() {
		for _, f := range cmd.ExtraFiles {
			_ = f.Close()
		}
	})
	assertNoArgvValue(t, cmd.Args, secret)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("confined command: %v: %s", err, out)
	}
	if string(out) != secret {
		t.Errorf("confined child saw MCP_SERVER_TOKEN=%q, want %q", out, secret)
	}
}

// TestConfineWithChildEnvNilWrapperJoinsCmdEnv pins the unconfined case: with no
// backend there is no launcher, so the child's own environment is cmd.Env and
// the assignments belong there. A nil wrapper must not panic on the missing
// separator.
func TestConfineWithChildEnvNilWrapperJoinsCmdEnv(t *testing.T) {
	var w *Wrapper
	cmd := exec.Command("/bin/echo", "hi") //nolint:noctx // test-only cmd, never run
	cmd.Env = []string{"KEEP=1", "OVERRIDE=old"}
	wantArgs := slices.Clone(cmd.Args)
	if err := w.ConfineWithChildEnv(cmd, "/somewhere", ChildEnvDelta{Set: map[string]string{"OVERRIDE": "new", "ADDED": "2"}, Unset: []string{"KEEP"}}); err != nil {
		t.Fatalf("ConfineWithChildEnv: %v", err)
	}

	if !slices.Equal(cmd.Args, wantArgs) {
		t.Errorf("an unsandboxed spawn must not gain argv: %v", cmd.Args)
	}
	joined := strings.Join(cmd.Env, "\n")
	if strings.Contains(joined, "KEEP=1") || !strings.Contains(joined, "ADDED=2") || strings.Contains(joined, "OVERRIDE=old") || !strings.Contains(joined, "OVERRIDE=new") {
		t.Errorf("nil-wrapper child env = %v", cmd.Env)
	}
}

// A cache root that only becomes a directory after session start (an unmounted
// drive) is judged by the overlay probe when it appears, so a root on a
// filesystem that refuses the overlay is still left out rather than aborting the
// first spawn that makes it servable (#4220).
func TestServableCacheRootProbesARootThatAppearsLate(t *testing.T) {
	dir := overlayFixtureDir(t)
	drive := filepath.Join(dir, "unmounted", "drive")
	link := filepath.Join(dir, "modcache")
	if err := os.Symlink(drive, link); err != nil {
		t.Fatal(err)
	}
	rp := ResolvedPolicy{
		CacheStrategy: CacheOverlay, CacheRoots: []string{link},
		resolveHost: HostFacts{OS: "linux", Home: dir, OverlayLowerOK: func(string) bool { return false }},
	}
	if servableCacheRoot(rp, link) {
		t.Fatal("a dangling cache root must not be served")
	}
	if err := os.MkdirAll(drive, 0o755); err != nil {
		t.Fatal(err)
	}
	if servableCacheRoot(rp, link) {
		t.Errorf("a root the overlay cannot mount must stay unserved once it appears")
	}
	// Control: the same root on a host that can overlay it is served once it appears.
	rp.resolveHost.OverlayLowerOK = func(string) bool { return true }
	if !servableCacheRoot(rp, link) {
		t.Errorf("an overlayable cache root that appears must be served")
	}
}

// The session scratch is allocated after Resolve, so the wrapper drops any
// cache root sharing storage with it (scratch kept under ~/.cache): an overlay
// there would discard the scratch files between commands.
func TestNewWrapperDropsACacheRootHoldingTheSessionScratch(t *testing.T) {
	cache := t.TempDir()
	scratch := filepath.Join(cache, "evener", "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	rp := ResolvedPolicy{Backend: BackendBwrap, CacheStrategy: CacheOverlay, CacheRoots: []string{cache}}
	w, err := NewWrapper(rp, "/usr/bin/bwrap", scratch)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(w.Policy().CacheRoots, cache) {
		t.Errorf("a cache root holding the session scratch must not be overlaid: %v", w.Policy().CacheRoots)
	}
}

// Each spawn applies the sharing rule again: a write root whose link has been
// retargeted into a cache since resolution keeps that cache from being
// overlaid at the next spawn, and the cache is served again once it points
// elsewhere.
func TestServableCacheRootJudgesSharingAtEachSpawn(t *testing.T) {
	cache, elsewhere := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "library")
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	rp := ResolvedPolicy{CacheStrategy: CacheOverlay, CacheRoots: []string{cache}}
	rp.Spawned.WriteRoots = []string{link}
	if !servableCacheRoot(rp, cache) {
		t.Fatalf("fixture: the cache must be served while the write root points elsewhere")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cache, link); err != nil {
		t.Fatal(err)
	}
	if servableCacheRoot(rp, cache) {
		t.Errorf("a cache a write root now lands in must not be overlaid")
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	if !servableCacheRoot(rp, cache) {
		t.Errorf("the cache must be served again once the write root points elsewhere")
	}
}
