package agent

import (
	"context"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// stagedCodexLiveProtocol is a Codex instance whose live roster gains a model
// after the first listing: the session's startup capture sees only the bundled
// id, and the switch's own listing sees the live-only id.
type stagedCodexLiveProtocol struct {
	id    string
	mu    sync.Mutex
	calls map[string]int
}

func (p *stagedCodexLiveProtocol) ID() string { return p.id }
func (*stagedCodexLiveProtocol) PrunablePaths() []string {
	return nil
}
func (*stagedCodexLiveProtocol) BuildBody(llm.Request, registry.Resolved) (map[string]any, error) {
	return map[string]any{}, nil
}
func (*stagedCodexLiveProtocol) Complete(context.Context, llm.Request, registry.Resolved) (llm.Response, error) {
	return llm.Response{}, nil
}
func (*stagedCodexLiveProtocol) Stream(context.Context, llm.Request, registry.Resolved) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (p *stagedCodexLiveProtocol) ListModels(_ context.Context, res registry.Resolved) ([]registry.Model, error) {
	p.mu.Lock()
	p.calls[res.Instance]++
	call := p.calls[res.Instance]
	p.mu.Unlock()
	if res.Instance == "codex-target" && call >= 2 {
		return []registry.Model{{ID: "gpt-reserve"}}, nil
	}
	return []registry.Model{{ID: "gpt-5.6"}}, nil
}
func (*stagedCodexLiveProtocol) CountTokens(context.Context, llm.Request, registry.Resolved) (int, error) {
	return 0, llm.ErrInputTokenCountUnsupported
}

func registerStagedCodexProtocol(p *stagedCodexLiveProtocol) {
	if _, ok := llm.ProtocolFor(p.ID()); ok {
		return
	}
	llm.RegisterProtocol(p)
}

// TestSetModel_SwitchesToLiveListedCodexModel pins the in-session door: a
// running session's registry may hold only the startup capture's roster (or
// none of the target's rows), while the model the picker offered is in the
// instance's live listing. The switch must list before it takes the session
// lock so the strict cross-provider resolve accepts the model.
func TestSetModel_SwitchesToLiveListedCodexModel(t *testing.T) {
	const protocolID = "test-proto-agent-codex-live"
	proto := &stagedCodexLiveProtocol{id: protocolID, calls: map[string]int{}}
	registerStagedCodexProtocol(proto)

	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"codex-home":   {Base: "openai-codex", Protocol: protocolID},
			"codex-target": {Base: "openai-codex", Protocol: protocolID},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	profile, err := provider.Resolve(r, "codex-home/gpt-5.6")
	if err != nil {
		t.Fatalf("home profile: %v", err)
	}

	sess := newSession(t,
		withClient(client),
		withProfile(profile),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(r, ref)
			},
			testOnly: testConfig{skipGitSnapshot: true},
		}),
	)
	if err := sess.SetModel("codex-target/gpt-reserve"); err != nil {
		t.Fatalf("SetModel to a model the target lists live: %v", err)
	}
	got := sess.currentProfile()
	if got.ID() != "codex-target" || got.Model() != "gpt-reserve" {
		t.Fatalf("profile = %s/%s, want codex-target/gpt-reserve", got.ID(), got.Model())
	}
}
