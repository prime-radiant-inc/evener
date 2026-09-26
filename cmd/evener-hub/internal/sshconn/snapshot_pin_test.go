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

// TestWaitHealthySnapshotPin proves the build-identity half of the probe: with a
// snapshot pin, a response that reports the expected version but an empty or
// different backend_git_sha is not yet healthy, so polling continues and the
// bounded wait fails with ErrRestart rather than attaching to a build the
// deploy did not produce. A matching SHA is healthy, and no pin at all leaves
// the version-only rule exactly as it was.
func TestWaitHealthySnapshotPin(t *testing.T) {
	host := hostreg.Host{Name: "alpha", SSH: "alpha.example"}
	const expectedSHA = "expectedsha"

	cases := []struct {
		name    string
		pin     string
		body    string
		wantErr bool
	}{
		{
			name:    "an empty backend_git_sha is not healthy for a snapshot pin",
			pin:     expectedSHA,
			body:    `{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
			wantErr: true,
		},
		{
			name:    "a different backend_git_sha is not healthy for a snapshot pin",
			pin:     expectedSHA,
			body:    `{"version":"newsha","backend_git_sha":"othersha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
			wantErr: true,
		},
		{
			name:    "the expected backend_git_sha is healthy for a snapshot pin",
			pin:     expectedSHA,
			body:    `{"version":"newsha","backend_git_sha":"expectedsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
			wantErr: false,
		},
		{
			// No pin: a release or dev controller, whose version alone is the
			// identity the probe has always used. A body with no backend_git_sha
			// still verifies.
			name:    "no pin keeps the version-only rule",
			pin:     "",
			body:    `{"version":"newsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`,
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probes := 0
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				if !strings.Contains(strings.Join(argv, " "), "api/health") {
					return nil, fmt.Errorf("unexpected remote command: %v", argv)
				}
				probes++
				return []byte(tc.body), nil
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{
				sleep: func(context.Context, time.Duration) error { return nil },
			})

			err := m.waitHealthy(context.Background(), host, "newsha", tc.pin, hubIdentity{})
			if (err != nil) != tc.wantErr {
				t.Fatalf("waitHealthy = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrRestart) {
				t.Fatalf("waitHealthy error = %v, want ErrRestart so the restart is retried", err)
			}
			if tc.wantErr {
				if probes < 2 {
					t.Fatalf("health probes = %d, want >= 2 (a rejected answer must keep polling)", probes)
				}
				if !strings.Contains(err.Error(), "backend_git_sha") {
					t.Fatalf("error does not name the build identity it rejected: %v", err)
				}
			}
		})
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
		return newTestManager(t, testRegistry(t, host), fr, Options{
			controllerVersionOverride: "newsha",
			sleep:                     func(context.Context, time.Duration) error { return nil },
		})
	}

	err := newManager(`{"version":"newsha","backend_git_sha":"othersha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`).
		restartHub(context.Background(), host, Preflight{OS: "linux"}, hubIdentity{})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart (the version matched but the commit did not)", err)
	}
	if !strings.Contains(err.Error(), "backend_git_sha") || !strings.Contains(err.Error(), "othersha") {
		t.Fatalf("error does not name the mismatched build identity: %v", err)
	}

	if err := newManager(`{"version":"newsha","backend_git_sha":"expectedsha","mobile_api_version":1,"hub_addr":"127.0.0.1:9180"}`).
		restartHub(context.Background(), host, Preflight{OS: "linux"}, hubIdentity{}); err != nil {
		t.Fatalf("restartHub: %v (a matching snapshot commit must verify)", err)
	}
}

// TestFirstAttachBootstrapPinsSnapshotBuild proves the START path carries the
// snapshot pin too. A cold bootstrap (a stopped host, or a deploy that found no
// hub present) launches the on-disk binary and, before this, verified it by
// version alone; for a snapshot channel a version cannot tell two builds apart,
// so a start could attach to a commit this controller never installed. The start
// now takes the same pin as the restart: a mismatched or absent backend_git_sha
// is not yet healthy (ErrRestart), and the controller's commit verifies. The
// start path's own facts make it reachable — the binary it launches is the one
// on disk, but "on disk" was accepted by the version-equality rule, which is
// exactly what a snapshot version cannot prove.
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
			launched := false
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				joined := strings.Join(argv, " ")
				switch {
				case strings.HasSuffix(joined, "uname -s"):
					return []byte("Linux\n"), nil
				case strings.HasSuffix(joined, "uname -m"):
					return []byte("x86_64\n"), nil
				case strings.HasSuffix(joined, "id -u"):
					return []byte("1000\n"), nil
				case strings.Contains(joined, "XDG_STATE_HOME"):
					return []byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=\n"), nil
				case strings.Contains(joined, "launch-check"):
					return []byte(`{"protocol":"evener-appwire-v6","version":"newsha","launch_flags":["api-log"]}`), nil
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
			m := newTestManager(t, testRegistry(t, host), fr, Options{
				controllerVersionOverride: "newsha",
				sleep:                     func(context.Context, time.Duration) error { return nil },
			})

			ch, err := m.Ensure(context.Background(), "alpha")
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
