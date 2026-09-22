package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunInstallBuildsSafeSSHCommand(t *testing.T) {
	old := runRemoteInstallCommand
	t.Cleanup(func() { runRemoteInstallCommand = old })

	var gotArgs []string
	var gotScript []byte
	runRemoteInstallCommand = func(_ context.Context, args []string, stdin io.Reader, stdout, _ io.Writer) error {
		gotArgs = append([]string(nil), args...)
		gotScript, _ = io.ReadAll(stdin)
		_, _ = io.WriteString(stdout, "remote output\n")
		return nil
	}

	var stdout, stderr bytes.Buffer
	err := runInstall([]string{
		"--version", "snapshot",
		"--prefix", "/tmp/evener;do-not-run",
		"user@example.com",
	}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("runInstall() error = %v", err)
	}
	if len(gotArgs) != 7 || gotArgs[0] != "-T" || gotArgs[1] != "-o" || gotArgs[2] != "BatchMode=yes" || gotArgs[3] != "-o" || gotArgs[4] != "ConnectTimeout=10" || gotArgs[5] != "user@example.com" {
		t.Fatalf("SSH args = %#v", gotArgs)
	}
	if !strings.Contains(gotArgs[6], "EVENER_INSTALL_VERSION=snapshot") {
		t.Fatalf("remote command omitted version: %q", gotArgs[6])
	}
	if !strings.Contains(gotArgs[6], "PREFIX='/tmp/evener;do-not-run'") {
		t.Fatalf("remote command did not quote prefix: %q", gotArgs[6])
	}
	if strings.Contains(gotArgs[6], "PREFIX=/tmp/evener;do-not-run") {
		t.Fatalf("remote command leaves prefix injectable: %q", gotArgs[6])
	}
	if len(gotScript) == 0 || !bytes.Contains(gotScript, []byte("checksums.txt")) {
		t.Fatalf("remote installer payload was not streamed: %q", gotScript)
	}
	if !strings.Contains(stdout.String(), "Installing Evener on user@example.com") || !strings.Contains(stdout.String(), "remote output") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestDispatchInstallCommand(t *testing.T) {
	called := false
	runners := cliCommandRunners{
		install: func(args []string, _ io.Reader, _, _ io.Writer) error {
			called = len(args) == 1 && args[0] == "host"
			return nil
		},
	}
	handled, label, err := dispatchCLICommandWith([]string{"install", "host"}, strings.NewReader(""), io.Discard, io.Discard, runners)
	if err != nil || !handled || label != "evener install" || !called {
		t.Fatalf("dispatch install: handled=%v label=%q err=%v called=%v", handled, label, err, called)
	}
}

func TestRunInstallRejectsUnsafeTarget(t *testing.T) {
	old := runRemoteInstallCommand
	t.Cleanup(func() { runRemoteInstallCommand = old })
	called := false
	runRemoteInstallCommand = func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
		called = true
		return nil
	}

	for _, target := range []string{"", "-oProxyCommand=bad", "user@host name", "user@host\nname"} {
		err := runInstall([]string{target}, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil {
			t.Errorf("runInstall(%q) error = nil, want rejection", target)
		}
	}
	if called {
		t.Fatal("SSH runner called for an unsafe target")
	}
}

// TestRunInstallRejectsTildePrefixedPaths pins why the path flags refuse a
// leading '~': the value travels as an argument to `env` in the remote command,
// where POSIX shells perform no tilde expansion, so '~/.local' would install
// under a directory literally named '~' on the remote host. Leaving the flag
// unset already installs into the remote user's $HOME/.local.
func TestRunInstallRejectsTildePrefixedPaths(t *testing.T) {
	old := runRemoteInstallCommand
	t.Cleanup(func() { runRemoteInstallCommand = old })
	called := false
	runRemoteInstallCommand = func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
		called = true
		return nil
	}

	for _, args := range [][]string{
		{"--prefix", "~/.local", "user@example.com"},
		{"--bin-dir", "~/bin", "user@example.com"},
		{"--share-bin-dir", "~/.local/share/evener/bin", "user@example.com"},
	} {
		err := runInstall(args, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "~") {
			t.Errorf("runInstall(%v) err = %v, want a tilde rejection", args, err)
		}
	}
	if called {
		t.Fatal("the SSH runner was called despite a tilde-prefixed path")
	}
}

// TestRunInstallRejectsRelativePathFlags pins the CLI half of the same
// contract: a relative --prefix/--bin-dir/--share-bin-dir resolves against the
// remote shell's working directory and produces a broken install, so the flags
// require absolute remote paths — which also rejects option-like values,
// because those do not begin with '/' either.
func TestRunInstallRejectsRelativePathFlags(t *testing.T) {
	old := runRemoteInstallCommand
	t.Cleanup(func() { runRemoteInstallCommand = old })
	called := false
	runRemoteInstallCommand = func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
		called = true
		return nil
	}

	for _, args := range [][]string{
		{"--prefix", "rel", "user@example.com"},
		{"--bin-dir", "rel/bin", "user@example.com"},
		{"--share-bin-dir", "rel/share", "user@example.com"},
		{"--prefix", "-not-a-path", "user@example.com"},
	} {
		err := runInstall(args, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "absolute") {
			t.Errorf("runInstall(%v) err = %v, want an absolute-path rejection", args, err)
		}
	}
	if called {
		t.Fatal("the SSH runner was called despite a relative path flag")
	}
}

// TestRunInstallTrimsTheVersionFlag pins that a version with surrounding
// whitespace is normalized before it reaches the remote command: the empty
// check trims, so passing the untrimmed value on would quote the spaces into
// EVENER_INSTALL_VERSION and fail later as an opaque download error instead of
// a clear argument error.
func TestRunInstallTrimsTheVersionFlag(t *testing.T) {
	old := runRemoteInstallCommand
	t.Cleanup(func() { runRemoteInstallCommand = old })
	var gotArgs []string
	runRemoteInstallCommand = func(_ context.Context, args []string, _ io.Reader, _, _ io.Writer) error {
		gotArgs = append([]string(nil), args...)
		return nil
	}

	err := runInstall([]string{"--version", " v1.2.3 ", "user@example.com"}, strings.NewReader(""), io.Discard, io.Discard)
	if err != nil {
		t.Fatalf("runInstall() error = %v", err)
	}
	if !strings.Contains(gotArgs[6], "EVENER_INSTALL_VERSION=v1.2.3") {
		t.Fatalf("remote command forwards the untrimmed version: %q", gotArgs[6])
	}
	if strings.Contains(gotArgs[6], "' v1.2.3") {
		t.Fatalf("remote command still quotes the version's surrounding whitespace: %q", gotArgs[6])
	}
}

func TestRunInstallPropagatesSSHFailure(t *testing.T) {
	old := runRemoteInstallCommand
	t.Cleanup(func() { runRemoteInstallCommand = old })
	runRemoteInstallCommand = func(context.Context, []string, io.Reader, io.Writer, io.Writer) error {
		return errors.New("exit status 255")
	}

	err := runInstall([]string{"user@example.com"}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "remote install on user@example.com") {
		t.Fatalf("runInstall() error = %v, want contextual SSH error", err)
	}
}
