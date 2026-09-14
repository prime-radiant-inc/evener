package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
