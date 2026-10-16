package sshconn

import (
	"context"
	"errors"
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

	host := hostreg.Host{Name: "alpha", SSH: "alpha.example", EvenerPath: "/opt/evener/bin/evener"}
	newManager := func(sha string) *Manager {
		fr, _ := supervisorRestartRunner(nil, func(int) ([]byte, error) { return nil, nil })
		modelSelectedProcess(t, fr, "/opt/evener/bin/evener", "oldsha", "newsha").sha = sha
		return newTestManager(t, testRegistry(t, host), fr, Options{
			controllerVersionOverride: "newsha",
			sleep:                     func(context.Context, time.Duration) error { return nil },
		})
	}

	err := newManager("othersha").
		restartHub(context.Background(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1, Version: "newsha"})
	if !errors.Is(err, ErrRestart) {
		t.Fatalf("err = %v, want ErrRestart (the version matched but the commit did not)", err)
	}
	if !strings.Contains(err.Error(), "backend_git_sha") || !strings.Contains(err.Error(), "othersha") {
		t.Fatalf("error does not name the mismatched build identity: %v", err)
	}

	if err := newManager("expectedsha").
		restartHub(context.Background(), host, Preflight{OS: "linux", LaunchCheckKnown: true, HubEndpointSchema: 1, Version: "newsha"}); err != nil {
		t.Fatalf("restartHub: %v (a matching snapshot commit must verify)", err)
	}
}
