package hub

import (
	"context"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// recordingStartSource records the wire params it was asked to start.
type recordingStartSource struct {
	*scriptedAppSource
	gotParams appwire.ThreadStartParams
}

func (s *recordingStartSource) StartThread(_ context.Context, params appwire.ThreadStartParams) (appwire.ThreadStartResponse, error) {
	s.gotParams = params
	return appwire.ThreadStartResponse{Thread: appwire.Thread{ID: "spawned-" + s.ID(), Source: s.ID()}}, nil
}

// TestHubThreadStartRoutesExplicitSource covers the explicit Source precedence:
// a named source receives thread/start, even when Harness would route elsewhere,
// and receives the normalized Source the hub resolved rather than the verbatim
// padded field.
func TestHubThreadStartRoutesExplicitSource(t *testing.T) {
	sources := appsource.NewRegistry()
	source := &recordingStartSource{scriptedAppSource: &scriptedAppSource{id: "remote-a"}}
	sources.Add(source)

	resp, err := hubThreadStart(t.Context(), hubcore.WebConfig{}, sources, appwire.ThreadStartParams{
		Source:  "  remote-a  ",
		Harness: "evener",
		CWD:     "/tmp",
	})
	if err != nil {
		t.Fatalf("hubThreadStart: %v", err)
	}
	if resp.Thread.ID != "spawned-remote-a" {
		t.Fatalf("thread = %+v, want the remote source's thread", resp.Thread)
	}
	if got := source.gotParams.Source; got != "remote-a" {
		t.Fatalf("forwarded params.Source = %q, want the trimmed routing value", got)
	}
}

// TestHubThreadStartUnknownSourceUnavailable covers the existing error shape for
// an unavailable non-local source.
func TestHubThreadStartUnknownSourceUnavailable(t *testing.T) {
	_, err := hubThreadStart(t.Context(), hubcore.WebConfig{}, appsource.NewRegistry(), appwire.ThreadStartParams{Source: "missing"})
	if err == nil {
		t.Fatal("hubThreadStart accepted an unknown source")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("err = %v, want CodeUnavailable", err)
	}
	if wire.Message != "spawn source is not available: missing" {
		t.Fatalf("message = %q, want the existing unavailable shape", wire.Message)
	}
}

// TestHubThreadStartEmptySourceDefaultsLocal covers the empty case: with no
// Source (and no harness route) thread/start takes the local spawn path instead
// of a source lookup. A nil Spawner is the observable local-path sentinel.
func TestHubThreadStartEmptySourceDefaultsLocal(t *testing.T) {
	for name, params := range map[string]appwire.ThreadStartParams{
		"empty":        {},
		"whitespace":   {Source: "   "},
		"explicit-loc": {Source: "local"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := hubThreadStart(t.Context(), hubcore.WebConfig{}, appsource.NewRegistry(), params)
			if err == nil || !strings.Contains(err.Error(), "spawner not configured") {
				t.Fatalf("err = %v, want local spawner path (spawner not configured)", err)
			}
		})
	}
}
