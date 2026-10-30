package sshconn

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// TestSnapshotPinGitSHASourcesFromBuildChannel pins where the snapshot pin comes
// from: the build channel, not a second flag. A snapshot controller must carry
// the expected commit; release and dev controllers carry no pin, which is what
// keeps them on the version-equality rule.
func TestSnapshotPinGitSHASourcesFromBuildChannel(t *testing.T) {
	savedChannel, savedSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = savedChannel, savedSHA })

	buildinfo.GitSHA = "expectedsha"
	for _, tc := range []struct {
		channel string
		want    string
	}{
		{"snapshot", "expectedsha"},
		{"release", ""},
		{"", ""}, // an empty channel is buildinfo's dev
	} {
		buildinfo.Channel = tc.channel
		if got := snapshotPinGitSHA(); got != tc.want {
			t.Fatalf("snapshotPinGitSHA() with channel %q = %q, want %q", tc.channel, got, tc.want)
		}
	}
}

// TestRestartHubPinsSnapshotBuildFromBuildChannel proves the caller wires the
// pin in: on the snapshot channel, a restart whose health response reports the
// expected version but a different commit must fail with ErrRestart instead of
// attaching, and the matching commit verifies. It exercises restartHub, so the
// channel read and the wait are proven together rather than in isolation.
func TestRestartHubPinsSnapshotBuildFromBuildChannel(t *testing.T) {
	savedChannel, savedSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = savedChannel, savedSHA })
	buildinfo.Channel, buildinfo.GitSHA = "snapshot", "expectedsha"

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	newManager := func(body string) *Manager {
		fr, _ := supervisorRestartRunner(nil, func(int) ([]byte, error) { return []byte(body), nil })
		run := fr.runFn
		fr.runFn = func(ctx context.Context, argv []string, stdin io.Reader) ([]byte, error) {
			if strings.Contains(strings.Join(argv, " "), "launch-check") {
				return launchContract("newsha"), nil
			}
			return run(ctx, argv, stdin)
		}
		modelLegacySelectedProcess(t, fr, host)
		return newTestManager(t, testRegistry(t, host), fr, Options{
			controllerVersionOverride: "newsha",
			sleep:                     func(context.Context, time.Duration) error { return nil },
		})
	}

	_, err := newManager(`{"version":"newsha","backend_git_sha":"othersha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`).
		RestartForOperation(context.Background(), host, Preflight{OS: "linux"}, nil)
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart (the version matched but the commit did not)", err)
	}
	if !strings.Contains(err.Error(), "backend_git_sha") || !strings.Contains(err.Error(), "othersha") {
		t.Fatalf("error does not name the mismatched build identity: %v", err)
	}

	if _, err := newManager(`{"version":"newsha","backend_git_sha":"expectedsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`).
		RestartForOperation(context.Background(), host, Preflight{OS: "linux"}, nil); err != nil {
		t.Fatalf("restartHub: %v (a matching snapshot commit must verify)", err)
	}
}

// A deployment onto a stopped host must verify its requested snapshot commit
// during bootstrap, even though an ordinary start accepts the installed build.
func TestFirstAttachBootstrapPinsSnapshotBuild(t *testing.T) {
	savedChannel, savedSHA := buildinfo.Channel, buildinfo.GitSHA
	t.Cleanup(func() { buildinfo.Channel, buildinfo.GitSHA = savedChannel, savedSHA })
	buildinfo.Channel, buildinfo.GitSHA = "snapshot", "expectedsha"

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name:    "a different backend_git_sha is not a healthy start",
			body:    `{"version":"newsha","backend_git_sha":"othersha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
			wantErr: true,
		},
		{
			name:    "an empty backend_git_sha is not a healthy start",
			body:    `{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
			wantErr: true,
		},
		{
			name:    "the controller's commit verifies the start",
			body:    `{"version":"newsha","backend_git_sha":"expectedsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
			wantErr: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launched, deployed := false, false
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, stdin io.Reader) ([]byte, error) {
				joined := strings.Join(argv, " ")
				switch {
				case strings.HasSuffix(joined, "uname -s"):
					return []byte("Linux\n"), nil
				case strings.HasSuffix(joined, "uname -m"):
					return []byte("x86_64\n"), nil
				case strings.HasSuffix(joined, "id -u"):
					return []byte("1000\n"), nil
				case argv[len(argv)-1] == envProbeScript:
					return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
				case strings.Contains(joined, "launch-check"):
					if !deployed {
						return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":[]}`), nil
					}
					return []byte(`{"hub_endpoint_schema":1,"protocol":"evener-appwire-v7","version":"newsha","launch_flags":["api-log"]}`), nil
				case strings.Contains(joined, "cat >"):
					_, _ = io.Copy(io.Discard, stdin)
					deployed = true
					return nil, nil
				case strings.Contains(joined, "test -d /opt/evener/bin"):
					return nil, nil
				case strings.Contains(joined, "api/health"):
					if !launched {
						return nil, errors.New("curl: (7) Failed to connect")
					}
					return []byte(tc.body), nil
				case strings.Contains(joined, "list-units"):
					return nil, nil // no supervisor: the ad hoc start path
				case strings.Contains(joined, "lsof -ti :9180"):
					return []byte(noListenerMarker + "\n"), nil
				case strings.Contains(joined, "evener_resolve /opt/evener/bin/evener"):
					return []byte("/opt/evener/bin/evener\n"), nil
				case strings.Contains(joined, "nohup"):
					launched = true
					return nil, nil
				default:
					return nil, fmt.Errorf("unexpected remote command: %v", argv)
				}
			}, startFn: goodStartFn(t)}
			fixture := modelLegacySelectedProcess(t, fr, host)
			fixture.adhoc = true
			m := newTestManager(t, testRegistry(t, host), fr, Options{
				controllerVersionOverride: "newsha",
				BuildBinary:               writeStageBinary,
				sleep:                     func(context.Context, time.Duration) error { return nil },
			})
			testDeployRecorder(t, m)

			ch, err := m.Ensure(context.Background(), "alpha")
			if !deployed || !launched {
				t.Fatalf("deploy=%v launch=%v", deployed, launched)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("Ensure = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if !errors.Is(err, ErrRestart) {
					t.Fatalf("Ensure err = %v, want ErrRestart so the start is retried", err)
				}
				if !strings.Contains(err.Error(), "backend_git_sha") {
					t.Fatalf("error does not name the build identity it rejected: %v", err)
				}
				return
			}
			if !launched {
				t.Fatal("the ad hoc start was never issued")
			}
			if err := ch.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
	}
}
