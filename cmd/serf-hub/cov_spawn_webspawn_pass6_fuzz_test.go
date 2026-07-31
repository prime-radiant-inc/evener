package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/launchconfig"
	"primeradiant.com/serf/rendezvous"
)

func pass6Executable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "serf")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func FuzzSpawnWebSpawnPass6(f *testing.F) {
	f.Add(byte(0))
	f.Fuzz(func(t *testing.T, _ byte) {
		ctx := context.Background()
		root := t.TempDir()

		// Exercise the rendezvous loops' ignored entries, freshness filter,
		// cancellation, clean exit, and failed exit paths.
		now := time.Now()
		_, _ = rendezvous.Write(root, rendezvous.Entry{PID: 10, StartedAt: now.Add(-time.Hour)})
		_, _ = rendezvous.Write(root, rendezvous.Entry{PID: 11, StartedAt: now.Add(time.Hour)})
		cancelCtx, cancel := context.WithCancel(ctx)
		cancel()
		_, _ = waitForRendezvous(cancelCtx, root, 10, WithStartedAfter(now))
		_, _ = waitForRendezvous(ctx, root, 11, WithStartedAfter(now))
		for _, exitErr := range []error{nil, errors.New("status 9")} {
			exited := make(chan error, 1)
			exited <- exitErr
			_, _ = waitForRendezvousOrExit(ctx, root, 99, exited)
		}

		valid := pass6Executable(t, `
if [ "$1" = "launch-check" ]; then
  case " $* " in
    *" --models "*) printf '{"protocol":"`+appwire.ProtocolVersion+`","models":[{"provider":" p ","model":" m "},{"provider":"","model":"bad"}],"diagnostics":[{"provider":" p ","source":" hub ","title":" title ","message":" message ","hint":" hint "},{"message":" "}]}' ;;
    *) printf '{"protocol":"`+appwire.ProtocolVersion+`"}' ;;
  esac
  exit 0
fi
rd=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--run-dir" ]; then shift; rd="$1"; fi
  shift
done
mkdir -p "$rd"
printf '{"pid":%s,"address":"127.0.0.1:1","started_at":"2999-01-01T00:00:00Z"}' "$$" > "$rd/$$.json"
sleep 1`)

		h := HubSpawner{SerfBinary: valid, RunDir: root, Cfg: Config{SpawnTimeout: time.Second}}
		_, _ = h.ListLaunchModels(ctx)
		_, _ = h.ListLaunchModelContract(ctx)
		_, _ = h.ListLaunchModelContractForWorkingDir(ctx, root)

		replay := 4
		resolved := launchconfig.Resolved{}
		resolved.Effective.AppReplaySize = &replay
		_, _ = h.Spawn(ctx, hubcore.SpawnRequest{WorkingDir: root, Resolved: resolved})
		_, _ = h.Resume(ctx, hubcore.ResumeRequest{SessionID: "old", WorkingDir: root, Resolved: resolved})

		// Preparation failures return before launch-check. A provider on resume
		// also covers its optional credential-validation branch with a nil store.
		blocked := filepath.Join(root, "not-a-directory")
		if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		inline := launchconfig.Resolved{}
		inline.Effective.SystemPromptMode = "inline"
		inline.Effective.SystemPromptText = "system"
		_, _ = h.Spawn(ctx, hubcore.SpawnRequest{StateDir: blocked, Resolved: inline})
		_, _ = h.Resume(ctx, hubcore.ResumeRequest{SessionID: "old", StateDir: blocked, Provider: "unknown", Resolved: inline})

		// Cover start failure, timeout, and both process-exit error forms.
		missing := filepath.Join(root, "missing-serf")
		_, _ = SpawnDaemon(ctx, missing, root, hubcore.SpawnRequest{}, time.Millisecond)
		_, _ = ResumeDaemon(ctx, missing, root, hubcore.ResumeRequest{}, time.Millisecond)
		for _, body := range []string{"exit 0", "echo detail >&2; exit 7", "sleep 2"} {
			bin := pass6Executable(t, body)
			_, _ = SpawnDaemon(ctx, bin, root, hubcore.SpawnRequest{}, time.Millisecond)
			_, _ = ResumeDaemon(ctx, bin, root, hubcore.ResumeRequest{}, time.Millisecond)
		}

		// Complete the launch-check and model-list response/error matrices.
		for _, body := range []string{
			`printf '{"protocol":"wrong"}'`,
			`printf 'not-json'`,
			`echo supersecret; exit 4`,
			`exit 4`,
		} {
			bin := pass6Executable(t, body)
			_ = validateSerfLaunchContract(ctx, bin, "model", []string{"API_KEY=supersecret"})
			_, _ = listSerfLaunchModelContract(ctx, bin, []string{"API_KEY=supersecret"})
		}
	})
}
