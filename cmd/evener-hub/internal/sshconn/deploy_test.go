package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
			case strings.Contains(joined, "readlink -f /opt/evener/bin/evener"):
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
		case strings.Contains(joined, "readlink -f"):
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
		case strings.Contains(joined, "readlink -f"):
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
		case strings.Contains(joined, "readlink -f "+link):
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

// TestModuleRootFromRequiresEvenerModule proves the source-tree lookup only
// accepts this module: an unrelated go.mod (an installed hub launched inside
// another workspace module) must not be mistaken for the evener checkout.
func TestModuleRootFromRequiresEvenerModule(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "sub", "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := moduleRootFrom(deep); got != "" {
		t.Fatalf("moduleRootFrom found %q with no go.mod", got)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/other\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := moduleRootFrom(deep); got != "" {
		t.Fatalf("moduleRootFrom matched a different module at %q", got)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module primeradiant.com/evener\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := moduleRootFrom(deep); got != root {
		t.Fatalf("moduleRootFrom = %q, want %q", got, root)
	}
}

// TestLocalBuildWithoutModuleRootFailsClearly proves an installed hub launched
// outside the checkout reports a clear unavailable-source error instead of
// running `go build ./cmd/evener/` against the wrong directory.
func TestLocalBuildWithoutModuleRootFailsClearly(t *testing.T) {
	t.Chdir(t.TempDir())
	err := localBuild(context.Background(), "linux", "amd64", filepath.Join(t.TempDir(), "evener"))
	if err == nil {
		t.Fatal("localBuild succeeded with no evener module root")
	}
	if !strings.Contains(err.Error(), "evener module root") {
		t.Fatalf("error does not explain the missing source tree: %v", err)
	}
}
