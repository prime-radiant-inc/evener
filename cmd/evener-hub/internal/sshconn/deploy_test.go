package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// TestBuildLdflagsStampsControllerBuildinfo proves the deployed binary is
// stamped with this process's own buildinfo values, so its launch-check version
// equals the controller's buildinfo.Version().
func TestBuildLdflagsStampsControllerBuildinfo(t *testing.T) {
	origSHA, origDirty, origTime, origCh := buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.BuildTime, buildinfo.Channel
	t.Cleanup(func() {
		buildinfo.GitSHA, buildinfo.GitDirty, buildinfo.BuildTime, buildinfo.Channel = origSHA, origDirty, origTime, origCh
	})
	buildinfo.GitSHA = "abc1234"
	buildinfo.GitDirty = "true"
	buildinfo.BuildTime = "2026-09-14T00:00:00Z"
	buildinfo.Channel = "snapshot"

	got := buildLdflags()
	for _, want := range []string{
		"-X primeradiant.com/evener/buildinfo.GitSHA=abc1234",
		"-X primeradiant.com/evener/buildinfo.GitDirty=true",
		"-X primeradiant.com/evener/buildinfo.BuildTime=2026-09-14T00:00:00Z",
		"-X primeradiant.com/evener/buildinfo.Channel=snapshot",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildLdflags() = %q, missing %q", got, want)
		}
	}
}

// TestDeployBuildsPushesAtomicallyAndCleansStaging covers the primary deploy
// path: cross-compile via the seam, an ssh `cat > tmp && chmod +x && mv` push
// fed on stdin, and local staging cleanup.
func TestDeployBuildsPushesAtomicallyAndCleansStaging(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	var builtOS, builtArch, builtOut string
	var pushedBytes []byte
	var pushArgv []string

	fr := &fakeRunner{
		runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			joined := strings.Join(argv, " ")
			switch {
			case strings.Contains(joined, "test -d /opt/evener/bin"):
				return nil, nil
			case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
				return []byte("/opt/evener/bin/evener\n"), nil
			case strings.Contains(joined, "cat >"):
				pushArgv = append([]string(nil), argv...)
				if stdin != nil {
					pushedBytes, _ = io.ReadAll(stdin)
				}
				return nil, nil
			default:
				return nil, fmt.Errorf("unexpected remote command: %v", argv)
			}
		},
	}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(_ context.Context, goos, goarch, out string) error {
			builtOS, builtArch, builtOut = goos, goarch, out
			return os.WriteFile(out, []byte("binary-bytes"), 0o755)
		},
	})

	if err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if builtOS != "linux" || builtArch != "amd64" {
		t.Fatalf("build target = %s/%s, want linux/amd64", builtOS, builtArch)
	}
	if string(pushedBytes) != "binary-bytes" {
		t.Fatalf("pushed bytes = %q", pushedBytes)
	}

	wantRemote := "cat > /opt/evener/bin/evener.tmp && chmod +x /opt/evener/bin/evener.tmp && mv /opt/evener/bin/evener.tmp /opt/evener/bin/evener"
	want := rawCommandArgv(m.opts, host, wantRemote)
	if !equalArgv(pushArgv, want) {
		t.Fatalf("push argv:\n got %v\nwant %v", pushArgv, want)
	}
	if len(pushArgv) == 0 || pushArgv[0] != "ssh" {
		t.Fatalf("push argv[0] = %v, want ssh", pushArgv)
	}
	if _, err := os.Stat(builtOut); !os.IsNotExist(err) {
		t.Fatalf("staging file %q still present after deploy (err=%v)", builtOut, err)
	}
}

// TestDeployTargetDirectoryMissingFailsClearly covers a non-empty evener_path
// whose directory does not exist: a clear ErrDeploy, and no build.
func TestDeployTargetDirectoryMissingFailsClearly(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/nope/bin/evener"}
	buildCalled := false
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		if strings.Contains(strings.Join(argv, " "), "test -d /nope/bin") {
			return []byte("test: /nope/bin: No such file or directory\n"), errors.New("exit status 1")
		}
		return nil, fmt.Errorf("unexpected remote command: %v", argv)
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(context.Context, string, string, string) error { buildCalled = true; return nil },
	})

	err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"})
	if !errors.Is(err, ErrDeploy) {
		t.Fatalf("err = %v, want ErrDeploy", err)
	}
	if !strings.Contains(err.Error(), "/nope/bin") {
		t.Fatalf("error does not name the missing directory: %v", err)
	}
	if buildCalled {
		t.Fatal("build ran despite missing target directory")
	}
}

// TestDeployTargetEmptyResolvesRemotePATH covers the empty evener_path case:
// the install path is whatever `evener` resolves to on the remote PATH.
func TestDeployTargetEmptyResolvesRemotePATH(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	var pushJoined string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "command -v evener"):
			return []byte("/home/dev/.local/bin/evener\n"), nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte("/home/dev/.local/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			pushJoined = joined
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(_ context.Context, _, _, out string) error {
			return os.WriteFile(out, []byte("x"), 0o755)
		},
	})

	if err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	want := "cat > /home/dev/.local/bin/evener.tmp && chmod +x /home/dev/.local/bin/evener.tmp && mv /home/dev/.local/bin/evener.tmp /home/dev/.local/bin/evener"
	if !strings.Contains(pushJoined, want) {
		t.Fatalf("push command = %q, want it to contain %q", pushJoined, want)
	}
}

// TestDeployQuotesRemotePaths proves a registry-configured evener_path with a
// space or shell metacharacter is quoted in every deploy command, so it neither
// breaks the command nor injects additional remote commands.
func TestDeployQuotesRemotePaths(t *testing.T) {
	const target = "/opt/my evener/bin/evener;rm"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: target}
	var testDirRemote, pushRemote string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d"):
			testDirRemote = joined
			return nil, nil
		case strings.Contains(joined, "evener_resolve"):
			return []byte(target + "\n"), nil
		case strings.Contains(joined, "cat >"):
			pushRemote = joined
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(_ context.Context, _, _, out string) error {
			return os.WriteFile(out, []byte("x"), 0o755)
		},
	})

	if err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if dir := path.Dir(target); !strings.Contains(testDirRemote, "test -d "+shellQuote(dir)) {
		t.Fatalf("test -d does not quote the path with a space: %q", testDirRemote)
	}
	tmp := shellQuote(target + deployTempSuffix)
	wantPush := fmt.Sprintf("cat > %s && chmod +x %s && mv %s %s", tmp, tmp, tmp, shellQuote(target))
	if !strings.Contains(pushRemote, wantPush) {
		t.Fatalf("push does not quote the paths:\n got %q\nwant it to contain %q", pushRemote, wantPush)
	}
}

// TestDeployResolvesSymlinkTarget proves the push replaces the executable a
// symlink points at, not the symlink itself: `make install` installs evener as
// a symlink and `command -v evener` returns that link, so mv-ing onto the link
// would clobber the installed layout.
func TestDeployResolvesSymlinkTarget(t *testing.T) {
	const link = "/usr/local/bin/evener"
	const realPath = "/opt/evener/1.2.3/evener"
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: link}
	var pushRemote string
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /usr/local/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve "+link):
			return []byte(realPath + "\n"), nil
		case strings.Contains(joined, "cat >"):
			pushRemote = joined
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{
		BuildBinary: func(_ context.Context, _, _, out string) error {
			return os.WriteFile(out, []byte("x"), 0o755)
		},
	})

	if err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	if !strings.Contains(pushRemote, "cat > "+realPath+".tmp") || !strings.Contains(pushRemote, "mv "+realPath+".tmp "+realPath) {
		t.Fatalf("push does not install onto the symlink's real file %q: %q", realPath, pushRemote)
	}
	if strings.Contains(pushRemote, "mv "+realPath+".tmp "+link) {
		t.Fatalf("push replaces the symlink %q instead of its target: %q", link, pushRemote)
	}
}

// TestResolveDeployCommandIsPortableAcrossReadlinkVariants proves the deploy path
// resolver does not depend on `readlink -f`, a GNU/coreutils extension that BSD
// `readlink` (macOS) rejects with `readlink: illegal option -- f` — the failure
// that made every darwin/arm64 deploy fail at path resolution. It runs the real
// resolver under a `readlink` shim emulating the BSD behavior and confirms the
// legacy form fails under that same shim, so the test embodies the pre-fix
// failure it guards against.
func TestResolveDeployCommandIsPortableAcrossReadlinkVariants(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not available: %v", err)
	}
	realReadlink, err := exec.LookPath("readlink")
	if err != nil {
		t.Skipf("readlink not available: %v", err)
	}

	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved := filepath.Join(realDir, "evener")
	if err := os.WriteFile(resolved, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link1 := filepath.Join(root, "link1")
	link2 := filepath.Join(root, "link2")
	if err := os.Symlink(resolved, link1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link1, link2); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	shim := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  -f) echo 'readlink: illegal option -- f' >&2; echo 'usage: readlink [-n] [file ...]' >&2; exit 1;;\n" +
		"  -n) shift;;\n" +
		"esac\n" +
		"exec " + realReadlink + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "readlink"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"))

	run := func(script string) (string, string, error) {
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = env
		var out, errBuf bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errBuf
		err := cmd.Run()
		return out.String(), errBuf.String(), err
	}

	// The resolver the deploy uses must succeed under the BSD-style readlink.
	out, stderr, err := run(resolveDeployCommand(link2))
	if err != nil {
		t.Fatalf("resolver failed under a BSD-style readlink: %v: %s", err, stderr)
	}
	if got := strings.TrimSpace(out); got != resolved {
		t.Fatalf("resolver output = %q, want %q", got, resolved)
	}

	// The pre-fix command must fail under the same shim: this is the darwin
	// failure the resolver replaces.
	if _, stderr, err := run("readlink -f " + shellQuote(link2)); err == nil {
		t.Fatal("legacy `readlink -f` unexpectedly succeeded under the BSD-style readlink shim")
	} else if !strings.Contains(stderr, "illegal option") {
		t.Fatalf("legacy command failed for an unexpected reason: %s", stderr)
	}

	// A path that does not resolve to an existing file prints nothing and fails,
	// so the caller reports a clear error instead of falling back to the symlink.
	out, _, err = run(resolveDeployCommand(filepath.Join(root, "missing")))
	if err == nil {
		t.Fatalf("resolver succeeded for a missing path (stdout %q)", out)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("resolver printed %q for a missing path, want nothing", out)
	}
}

// TestVerifyBuildSourceRequiresAnEvenerCheckout proves the explicit build source
// is verified and never guessed: an unset source, a tree that is not this
// module, and a tree without a ./cmd/evener package are each refused rather than
// built. A different module (an installed hub pointed at another workspace) must
// not be mistaken for the evener checkout.
func TestVerifyBuildSourceRequiresAnEvenerCheckout(t *testing.T) {
	if _, err := verifyBuildSource(""); err == nil || !strings.Contains(err.Error(), "no build source") {
		t.Fatalf("verifyBuildSource(\"\") err = %v, want a missing-source error", err)
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "go.mod"), []byte("module example.com/other\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBuildSource(other); err == nil || !strings.Contains(err.Error(), "not the evener checkout") {
		t.Fatalf("verifyBuildSource(other module) err = %v, want a wrong-module error", err)
	}

	evener := t.TempDir()
	if err := os.WriteFile(filepath.Join(evener, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyBuildSource(evener); err == nil || !strings.Contains(err.Error(), "cmd/evener") {
		t.Fatalf("verifyBuildSource(no cmd/evener) err = %v, want a missing-package error", err)
	}
	if err := os.MkdirAll(filepath.Join(evener, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := verifyBuildSource(evener)
	if err != nil {
		t.Fatalf("verifyBuildSource(valid checkout): %v", err)
	}
	if want, _ := filepath.EvalSymlinks(evener); got != want {
		t.Fatalf("verifyBuildSource = %q, want %q", got, want)
	}
}

// TestLocalBuildWithoutBuildSourceFailsClearly proves the production builder
// fails closed with a clear unavailable-source error instead of running
// `go build ./cmd/evener/` against whatever directory is nearby.
func TestLocalBuildWithoutBuildSourceFailsClearly(t *testing.T) {
	err := localBuild(context.Background(), "", "linux", "amd64", filepath.Join(t.TempDir(), "evener"))
	if err == nil {
		t.Fatal("localBuild succeeded with no build source")
	}
	if !strings.Contains(err.Error(), "build source") {
		t.Fatalf("error does not explain the missing source tree: %v", err)
	}
}

// TestDeployBuildsFromTheConfiguredSource proves an explicit BuildSource is
// honored: the production builder runs `go build` with the configured checkout
// as its working directory, never the process working directory.
func TestDeployBuildsFromTheConfiguredSource(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}

	shimDir := t.TempDir()
	marker := filepath.Join(shimDir, "go-wd")
	shim := "#!/bin/sh\npwd > " + marker + "\n" +
		"out=\"\"\nwhile [ $# -gt 0 ]; do if [ \"$1\" = \"-o\" ]; then out=$2; fi; shift; done\n" +
		"printf fake > \"$out\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "go"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+":"+os.Getenv("PATH"))
	t.Chdir(t.TempDir()) // run from an unrelated directory

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			if stdin != nil {
				_, _ = io.ReadAll(stdin)
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{BuildSource: source})

	if err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"}); err != nil {
		t.Fatalf("deploy: %v", err)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("go build did not run: %v", err)
	}
	want, _ := filepath.EvalSymlinks(source)
	if strings.TrimSpace(string(got)) != want {
		t.Fatalf("go build ran in %q, want the configured source %q", strings.TrimSpace(string(got)), want)
	}
}

// TestDeployDoesNotBuildFromAnUnconfiguredWorkingDirectory proves the production
// builder never discovers its source by looking around the working directory.
// A directory that merely looks like an evener checkout (same module path) must
// not be built: an installed hub launched inside an unrelated or ancestor
// checkout would silently deploy that tree's code. Before the fix localBuild
// walked up from the working directory, found this decoy checkout, and shelled
// out to `go build` (the shim on PATH recorded it), so this test failed with
// "deploy built from the working-directory checkout"; after the fix it fails
// closed with a clear missing-source error before any build runs.
func TestDeployDoesNotBuildFromAnUnconfiguredWorkingDirectory(t *testing.T) {
	decoy := t.TempDir()
	if err := os.WriteFile(filepath.Join(decoy, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(decoy, "cmd", "evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(decoy)

	shimDir := t.TempDir()
	marker := filepath.Join(shimDir, "go-ran")
	shim := "#!/bin/sh\nprintf '%s' \"$PWD\" > " + marker + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(shimDir, "go"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+":"+os.Getenv("PATH"))

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
		joined := strings.Join(argv, " ")
		switch {
		case strings.Contains(joined, "test -d /opt/evener/bin"):
			return nil, nil
		case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
			return []byte("/opt/evener/bin/evener\n"), nil
		case strings.Contains(joined, "cat >"):
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected remote command: %v", argv)
		}
	}}
	// No BuildBinary seam and no explicit source: the production builder runs.
	m := newTestManager(t, testRegistry(t, host), fr, Options{})

	err := m.deploy(context.Background(), host, Preflight{OS: "linux", Arch: "amd64"})
	if err == nil {
		t.Fatal("deploy succeeded with no configured build source")
	}
	if got, statErr := os.ReadFile(marker); statErr == nil {
		t.Fatalf("deploy built from the working-directory checkout (%q): %v", got, err)
	}
	if !strings.Contains(err.Error(), "build source") {
		t.Fatalf("error does not explain the missing build source: %v", err)
	}
}
