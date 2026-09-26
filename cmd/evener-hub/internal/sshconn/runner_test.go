package sshconn

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/internal/shellquote"
)

func TestComposeDest(t *testing.T) {
	cases := []struct {
		name string
		host hostreg.Host
		want string
	}{
		{"no user", hostreg.Host{SSH: "alpha.example"}, "alpha.example"},
		{"user composes", hostreg.Host{SSH: "alpha.example", User: "bob"}, "bob@alpha.example"},
		{"ssh already has user", hostreg.Host{SSH: "bob@alpha.example"}, "bob@alpha.example"},
		{"no double at", hostreg.Host{SSH: "bob@alpha.example", User: "alice"}, "bob@alpha.example"},
		{"trimmed", hostreg.Host{SSH: "  alpha.example  ", User: "bob"}, "bob@alpha.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeDest(tc.host); got != tc.want {
				t.Fatalf("composeDest = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEvenerCommand(t *testing.T) {
	cases := map[string]string{
		"":                       "evener",
		"   ":                    "evener",
		"evener":                 "evener",
		" /usr/local/bin/evener": "/usr/local/bin/evener",
		"/opt/evener/bin/evener": "/opt/evener/bin/evener",
	}
	for in, want := range cases {
		if got := evenerCommand(in); got != want {
			t.Fatalf("evenerCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChannelArgv(t *testing.T) {
	opts := Options{
		ConnectTimeout:      10 * time.Second,
		ServerAliveInterval: 15 * time.Second,
		ServerAliveCountMax: 3,
	}
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", User: "bob", EvenerPath: "/opt/evener/bin/evener"}
	got := channelArgv(opts, host)
	want := []string{
		"ssh",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"--",
		"bob@alpha.example",
		"/opt/evener/bin/evener",
		"hub", "attach", "--stdio",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channelArgv:\n got %v\nwant %v", got, want)
	}
}

func TestChannelArgvDefaultsAndEmptyPath(t *testing.T) {
	got := channelArgv(Options{}, hostreg.Host{Name: "alpha", SSH: "alpha.example"})
	want := []string{
		"ssh",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"--",
		"alpha.example",
		"evener",
		"hub", "attach", "--stdio",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channelArgv defaults:\n got %v\nwant %v", got, want)
	}
}

func TestRawAndEvenerCommandArgv(t *testing.T) {
	opts := Options{ConnectTimeout: 7 * time.Second, ServerAliveInterval: 9 * time.Second, ServerAliveCountMax: 2}
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	raw := rawCommandArgv(opts, host, "uname -s")
	if raw[0] != "ssh" || raw[len(raw)-2] != "alpha.example" || raw[len(raw)-1] != "uname -s" {
		t.Fatalf("rawCommandArgv = %v", raw)
	}
	if raw[len(raw)-3] != "--" {
		t.Fatalf("rawCommandArgv missing option terminator before the dest: %v", raw)
	}
	lc := evenerCommandArgv(opts, host, "launch-check", "--protocol", appwire.ProtocolVersion, "--json")
	if lc[len(lc)-5] != "evener" || lc[len(lc)-4] != "launch-check" {
		t.Fatalf("evenerCommandArgv = %v", lc)
	}
	if lc[len(lc)-3] != "--protocol" || lc[len(lc)-2] != appwire.ProtocolVersion {
		t.Fatalf("protocol argv tail = %v", lc)
	}
	if lc[len(lc)-7] != "--" {
		t.Fatalf("evenerCommandArgv missing option terminator before the dest: %v", lc)
	}
}

// A host with its own hub.toml or listen address must attach with them: without
// the flags the bridge resolves defaults and addresses the wrong process.
func TestChannelArgvCarriesConfigAndAddr(t *testing.T) {
	host := hostreg.Host{
		Name:       "alpha",
		SSH:        "alpha.example",
		EvenerPath: "/opt/evener/bin/evener",
		ConfigPath: "/etc/evener/hub.toml",
		Addr:       "127.0.0.1:9180",
	}
	got := channelArgv(Options{}, host)
	want := []string{
		"ssh",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"--",
		"alpha.example",
		"/opt/evener/bin/evener",
		"hub", "attach", "--stdio",
		"--config", "/etc/evener/hub.toml",
		"--addr", "127.0.0.1:9180",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channelArgv:\n got %v\nwant %v", got, want)
	}

	for _, tc := range []struct {
		name string
		host hostreg.Host
		want []string
	}{
		{"addr only", hostreg.Host{Name: "a", SSH: "a.example", Addr: "10.0.0.1:1"}, []string{"--addr", "10.0.0.1:1"}},
		{"config only", hostreg.Host{Name: "a", SSH: "a.example", ConfigPath: "/x/hub.toml"}, []string{"--config", "/x/hub.toml"}},
		{"blank omitted", hostreg.Host{Name: "a", SSH: "a.example", ConfigPath: "  ", Addr: "\t"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := channelArgv(Options{}, tc.host)
			idx := slices.Index(argv, "--stdio")
			if idx < 0 {
				t.Fatalf("no --stdio in %v", argv)
			}
			tailArgs := argv[idx+1:]
			if !slices.Equal(tailArgs, tc.want) {
				t.Fatalf("flags after --stdio = %v, want %v", tailArgs, tc.want)
			}
		})
	}
}

// A host entry carrying an SSH key path must dial with it: -i <key> sits with
// the other options, before the "--" destination terminator, so every ssh
// invocation — the bridge channel, the raw probes, and the evener commands —
// resolves the identity the entry configured instead of the operator's
// ssh_config default.
func TestChannelArgvCarriesKeyPath(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", KeyPath: "/keys/alpha"}
	got := channelArgv(Options{}, host)
	want := []string{
		"ssh",
		"-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-i", "/keys/alpha",
		"--",
		"alpha.example",
		"evener",
		"hub", "attach", "--stdio",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channelArgv with key:\n got %v\nwant %v", got, want)
	}
	for _, tc := range []struct {
		name string
		argv []string
	}{
		{"raw command", rawCommandArgv(Options{}, host, "uname -s")},
		{"evener command", evenerCommandArgv(Options{}, host, "launch-check")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			argv := tc.argv
			term := slices.Index(argv, "--")
			if term < 0 {
				t.Fatalf("no option terminator in %v", argv)
			}
			if !slices.Contains(argv[:term], "-i") || !slices.Contains(argv[:term], "/keys/alpha") {
				t.Fatalf("key option missing before the terminator: %v", argv[:term])
			}
		})
	}
	// A key path that begins with "-" is still an argument to -i, never an
	// ssh option: it sits before the terminator that ends option parsing.
	dash := hostreg.Host{Name: "alpha", SSH: "alpha.example", KeyPath: "-weird-key"}
	argv := channelArgv(Options{}, dash)
	term := slices.Index(argv, "--")
	if term < 2 || argv[term-2] != "-i" || argv[term-1] != "-weird-key" {
		t.Fatalf("dash-leading key path not passed as -i's argument before --: %v", argv)
	}
}

// A caller that already gave up must not leave us a child to reap.
func TestExecRunnerStartRejectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stdio, err := (execRunner{}).Start(ctx, []string{"sh", "-c", "true"}, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start with a canceled ctx = %v, want context.Canceled", err)
	}
	if stdio != nil {
		t.Fatal("Start returned a Stdio for a canceled ctx")
	}
}

// ssh joins the remote argv and hands the result to the remote login shell, so a
// value with a space would split into two arguments and a metacharacter would be
// executed there. Ordinary words keep their documented, unquoted form.
func TestRemoteWord(t *testing.T) {
	cases := map[string]string{
		"":                       "''",
		"evener":                 "evener",
		"/opt/evener/bin/evener": "/opt/evener/bin/evener",
		"--stdio":                "--stdio",
		"evener-appwire-v5":      "evener-appwire-v5",
		"127.0.0.1:9180":         "127.0.0.1:9180",
		"~/bin/evener":           "~/bin/evener",
		"/home/dev/My Evener":    "'/home/dev/My Evener'",
		"a;b":                    "'a;b'",
		"$(id)":                  "'$(id)'",
		"`id`":                   "'`id`'",
		"it's":                   `'it'\''s'`,
		"a\tb":                   "'a\tb'",
		// A non-ASCII word keeps its bare form: high bytes are not remote-shell
		// metacharacters, so the pre-consolidation deny-list left them bare and
		// the shared helper must too.
		"café":                "café",
		"/opt/Ünïcode/evener": "/opt/Ünïcode/evener",
	}
	for in, want := range cases {
		if got := shellquote.RemoteWord(in); got != want {
			t.Errorf("RemoteWord(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChannelArgvQuotesHostValues(t *testing.T) {
	host := hostreg.Host{
		Name:       "alpha",
		SSH:        "alpha.example",
		EvenerPath: "/home/dev/My Evener/evener",
		ConfigPath: "/etc/My Config/hub.toml",
		Addr:       "127.0.0.1:9180",
	}
	argv := channelArgv(Options{}, host)
	for _, want := range []string{"'/home/dev/My Evener/evener'", "'/etc/My Config/hub.toml'"} {
		if !slices.Contains(argv, want) {
			t.Fatalf("argv %v missing the quoted word %q", argv, want)
		}
	}
	for _, word := range argv {
		if strings.ContainsRune(word, ' ') && (!strings.HasPrefix(word, "'") || !strings.HasSuffix(word, "'")) {
			t.Fatalf("word %q would split in the remote shell: %v", word, argv)
		}
	}
}

// A dropped link reaps the ssh child, and Manager.Close then kills an already
// finished process: that is a normal teardown, not an error to report.
func TestExecStdioKillAfterExitIsNotAnError(t *testing.T) {
	stdio, err := (execRunner{}).Start(context.Background(), []string{"sh", "-c", "exit 0"}, io.Discard)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := stdio.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := stdio.Kill(); err != nil {
		t.Fatalf("Kill after the child exited = %v, want nil", err)
	}
}

// A preflight answer is a few hundred bytes. A command that floods the controller
// must be refused rather than buffered without bound.
func TestExecRunnerRunRejectsOversizedOutput(t *testing.T) {
	r := execRunner{}
	want := 3 * runOutputLimit
	out, err := r.Run(context.Background(), []string{"sh", "-c", "yes a | head -c " + strconv.Itoa(want)}, nil)
	if err == nil {
		t.Fatalf("Run accepted %d bytes of output", len(out))
	}
	var rf *RunError
	if !errors.As(err, &rf) {
		t.Fatalf("err = %v, want *RunError", err)
	}
	if !strings.Contains(rf.Err.Error(), "limit") {
		t.Fatalf("the error does not name the limit: %v", rf.Err)
	}
	if len(rf.Stdout) != runOutputLimit {
		t.Fatalf("captured stdout = %d bytes, want the %d byte cap", len(rf.Stdout), runOutputLimit)
	}
	if len(out) > 2*runOutputLimit {
		t.Fatalf("captured %d bytes across both streams, want at most the two caps", len(out))
	}
}

// A registry entry is host-owned config, but it must never be able to smuggle an
// ssh option: "-oProxyCommand=..." would run a command on the controller.
func TestSSHDestGuardsOptionInjection(t *testing.T) {
	const hostile = "-oProxyCommand=touch /tmp/pwned"
	host := hostreg.Host{Name: "evil", SSH: hostile}
	argvs := map[string][]string{
		"raw":     rawCommandArgv(Options{}, host, "uname -s"),
		"evener":  evenerCommandArgv(Options{}, host, "launch-check", "--json"),
		"channel": channelArgv(Options{}, host),
	}
	for name, argv := range argvs {
		dash := slices.Index(argv, "--")
		dest := slices.Index(argv, hostile)
		if dash < 0 {
			t.Fatalf("%s: argv has no option terminator: %v", name, argv)
		}
		if dest != dash+1 {
			t.Fatalf("%s: dest index %d is not the argument after -- at %d: %v", name, dest, dash, argv)
		}
	}
}

// execRunner.Run must keep the streams apart: ssh writes benign notices to
// stderr, and merging them into a preflight response corrupts every parse
// downstream.
func TestExecRunnerRunSeparatesStreams(t *testing.T) {
	r := execRunner{}
	out, err := r.Run(context.Background(), []string{"sh", "-c", "printf stdout-only; printf 'ssh: warning' >&2"}, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out) != "stdout-only" {
		t.Fatalf("success output = %q, want %q", out, "stdout-only")
	}

	out, err = r.Run(context.Background(), []string{"sh", "-c", "printf partial; printf boom >&2; exit 3"}, nil)
	if err == nil {
		t.Fatal("Run on a failing command reported no error")
	}
	if !strings.Contains(string(out), "partial") || !strings.Contains(string(out), "boom") {
		t.Fatalf("failure output = %q, want both streams for the diagnostic", out)
	}
}

func TestSSHSecondsClampsToMinimumOne(t *testing.T) {
	if got := sshSeconds(0); got != "1" {
		t.Fatalf("sshSeconds(0) = %q, want 1", got)
	}
	if got := sshSeconds(1500 * time.Millisecond); got != "1" {
		t.Fatalf("sshSeconds(1.5s) = %q, want 1", got)
	}
	if got := sshSeconds(10 * time.Second); got != "10" {
		t.Fatalf("sshSeconds(10s) = %q, want 10", got)
	}
}

// ServerAliveCountMax is a unitless count, not a duration. Rendering it through
// seconds arithmetic overflowed for a large configured value, because
// count*time.Second exceeds the Duration range; render it directly.
func TestServerAliveCountMaxRendersAsUnitlessCount(t *testing.T) {
	const huge = 1 << 40 // huge*time.Second overflows int64
	got := sshBaseArgv(Options{ServerAliveCountMax: huge})
	want := "ServerAliveCountMax=" + strconv.Itoa(huge)
	if !slices.Contains(got, want) {
		t.Fatalf("sshBaseArgv = %v, want it to contain %q", got, want)
	}
}

func TestAppendTailKeepsTrailingBytes(t *testing.T) {
	buf := appendTail(nil, []byte("abcdef"), 4)
	if string(buf) != "cdef" {
		t.Fatalf("appendTail = %q, want cdef", buf)
	}
	buf = appendTail(buf, []byte("ghijkl"), 4)
	if string(buf) != "ijkl" {
		t.Fatalf("appendTail second = %q, want ijkl", buf)
	}
}

func TestDiagSinkForwardsAndTails(t *testing.T) {
	var sb strings.Builder
	sink := newDiagSink(&sb)
	if _, err := sink.Write([]byte("ssh: connect failed\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.Contains(sb.String(), "connect failed") {
		t.Fatalf("sink did not forward to writer: %q", sb.String())
	}
	if !strings.Contains(sink.tail(), "connect failed") {
		t.Fatalf("tail = %q", sink.tail())
	}
}
