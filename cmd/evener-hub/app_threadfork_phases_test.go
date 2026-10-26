package hub

import (
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestForkStateDirPrefersTheDaemonsOverTheHubs(t *testing.T) {
	cfg := hubcore.WebConfig{StateDir: "/hub/state"}
	got, err := forkStateDir(cfg, hubcore.PastEntry{StateDir: "/daemon/state"})
	if err != nil || got != "/daemon/state" {
		t.Fatalf("daemon state dir = %q, %v", got, err)
	}
	got, err = forkStateDir(cfg, hubcore.PastEntry{})
	if err != nil || got != "/hub/state" {
		t.Fatalf("fallback state dir = %q, %v", got, err)
	}
}

func TestForkWithoutAStateDirIsUnavailable(t *testing.T) {
	fork := forkRequest{cfg: hubcore.WebConfig{}}
	for name, run := range map[string]func() error{
		"aside": func() error { _, err := fork.forkAside(hubcore.PastEntry{}, "01ABCDEFGHJKMNPQRSTVWXYZ01"); return err },
		"branch": func() error {
			_, err := fork.forkBranch(hubcore.PastEntry{}, "01ABCDEFGHJKMNPQRSTVWXYZ01", 0)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			var wire appwire.WireError
			if err := run(); !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
				t.Fatalf("error = %v, want an unavailable wire error", err)
			}
		})
	}
}

func TestForkThreadResponseNamesTheChildByItsLocalRef(t *testing.T) {
	got := forkThreadResponse("child-1", "original")
	if got.Thread.ID != "child-1" || got.Thread.SessionID != "child-1" || got.Thread.Source != "local" {
		t.Fatalf("thread = %+v", got.Thread)
	}
	if want := (appwire.Ref{SourceID: "local", ThreadID: "child-1"}).String(); got.Thread.Evener.Ref != want {
		t.Fatalf("ref = %q, want %q", got.Thread.Evener.Ref, want)
	}
	if got.OriginalInput != "original" {
		t.Fatalf("original input = %q", got.OriginalInput)
	}
}

func TestForkRefForNamesOnlyTheRequestedThread(t *testing.T) {
	fork := forkRequest{params: appwire.ThreadForkParams{Ref: "local:alias"}, ref: appwire.Ref{SourceID: "local", ThreadID: "alias"}}
	if got := fork.refFor("alias"); got != "local:alias" {
		t.Fatalf("requested thread ref = %q", got)
	}
	if got := fork.refFor("resolved"); got != "" {
		t.Fatalf("resolved session ref = %q, want none", got)
	}
}
