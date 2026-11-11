package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// windowListingProtocol serves a per-instance live listing: the rows an
// instance lists, or the error its listing fails with.
type windowListingProtocol struct {
	id   string
	rows map[string][]registry.Model
	errs map[string]error
}

func (p windowListingProtocol) ID() string            { return p.id }
func (windowListingProtocol) PrunablePaths() []string { return nil }
func (windowListingProtocol) BuildBody(llm.Request, registry.Resolved) (map[string]any, error) {
	return map[string]any{}, nil
}
func (windowListingProtocol) Complete(context.Context, llm.Request, registry.Resolved) (llm.Response, error) {
	return llm.Response{}, nil
}
func (windowListingProtocol) Stream(context.Context, llm.Request, registry.Resolved) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (windowListingProtocol) CountTokens(context.Context, llm.Request, registry.Resolved) (int, error) {
	return 0, llm.ErrInputTokenCountUnsupported
}
func (p windowListingProtocol) ListModels(_ context.Context, res registry.Resolved) ([]registry.Model, error) {
	if err := p.errs[res.Instance]; err != nil {
		return nil, err
	}
	return p.rows[res.Instance], nil
}

var registerWindowListingProtocol sync.Once

// windowRecordClient is a client whose registry holds a gateway listing a 500K
// window for "wide" and none for "narrow", and a gateway whose listing fails.
// No listing has been applied yet, so a profile resolved from it knows no live
// window.
func windowRecordClient(t *testing.T) (*llm.Client, *registry.Registry) {
	t.Helper()
	const protocolID = "test-proto-window-record"
	registerWindowListingProtocol.Do(func() {
		llm.RegisterProtocol(windowListingProtocol{
			id: protocolID,
			rows: map[string][]registry.Model{
				"gw-listed": {
					{ID: "wide", Caps: registry.Caps{ContextWindow: new(500_000)}},
					{ID: "narrow"},
				},
			},
			errs: map[string]error{"gw-down": errors.New("listing unavailable")},
		})
	})
	gateway := func() registry.Provider {
		return registry.Provider{Base: "openai-compatible", Protocol: protocolID, Transport: registry.Transport{BaseURL: "http://gw.invalid/v1"}}
	}
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{"gw-listed": gateway(), "gw-down": gateway()}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	for _, inst := range r.Instances() {
		if inst.Name != "gw-listed" && inst.Name != "gw-down" {
			client.Register(&fakeAdapter{name: inst.Name})
		}
	}
	return client, r
}

func windowRecordSession(t *testing.T) *Session {
	t.Helper()
	client, r := windowRecordClient(t)
	home, err := provider.Resolve(r, "gw-listed/wide")
	if err != nil {
		t.Fatalf("home profile: %v", err)
	}
	return newSession(t,
		withClient(client),
		withProfile(home),
		withConfig(SessionConfig{
			StateDir:       t.TempDir(),
			ResolveProfile: func(ref string) (*provider.Profile, error) { return provider.Resolve(r, ref) },
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)
}

func lastModelSwitchRecord(t *testing.T, sess *Session) schema.ModelSwitchInfo {
	t.Helper()
	data, err := readTranscriptFull(context.Background(), sess.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	var last *schema.ModelSwitchInfo
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnModelSwitch {
			last = entry.Turn.ModelSwitch
		}
	}
	if last == nil {
		t.Fatal("no MODEL_SWITCH entry in the transcript")
	}
	return *last
}

// The window a switch budgets against, and where it came from, is part of the
// durable switch record, the session metadata, and the context metrics.
func TestSetModel_RecordsBudgetingWindowAndSource(t *testing.T) {
	sess := windowRecordSession(t)
	for _, tc := range []struct {
		model      string
		wantWindow int
		wantSource string
	}{
		{"gw-listed/wide", 500_000, registry.LayerLive},
		{"gw-listed/narrow", contextmgr.AssumedContextWindow, provider.ContextWindowSourceAssumed},
		{"gw-down/unlisted", contextmgr.AssumedContextWindow, provider.ContextWindowSourceAssumed},
	} {
		if err := sess.SetModel(tc.model); err != nil {
			t.Fatalf("SetModel(%s): %v", tc.model, err)
		}
		rec := lastModelSwitchRecord(t, sess)
		if rec.ContextWindow != tc.wantWindow || rec.ContextWindowSource != tc.wantSource {
			t.Errorf("%s: MODEL_SWITCH window = %d from %q, want %d from %q", tc.model, rec.ContextWindow, rec.ContextWindowSource, tc.wantWindow, tc.wantSource)
		}
		meta := sess.Meta()
		if meta.ContextWindow != tc.wantWindow || meta.ContextWindowSource != tc.wantSource {
			t.Errorf("%s: meta window = %d from %q, want %d from %q", tc.model, meta.ContextWindow, meta.ContextWindowSource, tc.wantWindow, tc.wantSource)
		}
		metrics, _ := sess.ContextStatus()
		if metrics.WindowSource != tc.wantSource {
			t.Errorf("%s: metrics source = %q, want %q", tc.model, metrics.WindowSource, tc.wantSource)
		}
	}
}

// A restored session re-resolves its window from the instance's live listing
// and records it in the metadata it saves next.
func TestRestore_RecordsBudgetingWindowAndSource(t *testing.T) {
	sess := windowRecordSession(t)
	meta := sess.Meta()
	sess.Close()

	client, r := windowRecordClient(t)
	profile, err := provider.Resolve(r, "gw-listed/wide")
	if err != nil {
		t.Fatal(err)
	}
	if profile.ContextWindowSource() != provider.ContextWindowSourceAssumed {
		t.Fatalf("fixture must start without a live window, got %q", profile.ContextWindowSource())
	}
	dir := t.TempDir()
	resumed, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	defer resumed.Close()
	got := resumed.Meta()
	if got.ContextWindow != 500_000 || got.ContextWindowSource != registry.LayerLive {
		t.Fatalf("restored meta window = %d from %q, want 500000 from live", got.ContextWindow, got.ContextWindowSource)
	}
}
