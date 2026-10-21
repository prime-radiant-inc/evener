package launchcheck

import (
	"context"
	"errors"
	"io"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// codexLiveProtocol stands in for the responses protocol on a Codex instance:
// its live listing is the roster the test controls, with no HTTP.
type codexLiveProtocol struct {
	id   string
	rows []registry.Model
}

func (p codexLiveProtocol) ID() string { return p.id }
func (codexLiveProtocol) PrunablePaths() []string {
	return nil
}
func (codexLiveProtocol) BuildBody(llm.Request, registry.Resolved) (map[string]any, error) {
	return map[string]any{}, nil
}
func (codexLiveProtocol) Complete(context.Context, llm.Request, registry.Resolved) (llm.Response, error) {
	return llm.Response{}, nil
}
func (codexLiveProtocol) Stream(context.Context, llm.Request, registry.Resolved) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (p codexLiveProtocol) ListModels(context.Context, registry.Resolved) ([]registry.Model, error) {
	return p.rows, nil
}
func (codexLiveProtocol) CountTokens(context.Context, llm.Request, registry.Resolved) (int, error) {
	return 0, llm.ErrInputTokenCountUnsupported
}

// registerCodexLiveProtocol is idempotent for repeated test runs.
func registerCodexLiveProtocol(p codexLiveProtocol) {
	if _, ok := llm.ProtocolFor(p.ID()); ok {
		return
	}
	llm.RegisterProtocol(p)
}

func codexLiveCheckClient(t *testing.T, protocolID string) *llm.Client {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"codex-plan": {Base: "openai-codex", Protocol: protocolID},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return llm.NewClient(llm.WithRegistry(r))
}

// TestLaunchCheckAcceptsLiveListedCodexModel pins the hub's fresh-launch path:
// the preflight's network-free facts read defers a Codex id the bundled roster
// rejects, and the model check judges membership in the instance's live
// listing — the same roster the child applies before it launches.
func TestLaunchCheckAcceptsLiveListedCodexModel(t *testing.T) {
	const protocolID = "test-proto-launchcheck-codex-live"
	registerCodexLiveProtocol(codexLiveProtocol{id: protocolID, rows: []registry.Model{{ID: "gpt-reserve"}}})
	client := codexLiveCheckClient(t, protocolID)
	withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))

	if err := RunLaunchCheck([]string{"--model", "codex-plan/gpt-reserve"}, io.Discard, io.Discard); err != nil {
		t.Fatalf("launch-check rejected a Codex model its instance lists live: %v", err)
	}
}

// TestLaunchCheckStillRejectsAbsentCodexModel pins the other side: when the
// live listing answers and does not carry the model, the launch is refused
// with the registry's Codex sentinel.
func TestLaunchCheckStillRejectsAbsentCodexModel(t *testing.T) {
	const protocolID = "test-proto-launchcheck-codex-absent"
	registerCodexLiveProtocol(codexLiveProtocol{id: protocolID, rows: []registry.Model{{ID: "gpt-5.6"}}})
	client := codexLiveCheckClient(t, protocolID)
	withLaunchCheckLoadClient(t, fixedLaunchCheckClient(client))

	err := RunLaunchCheck([]string{"--model", "codex-plan/gpt-reserve"}, io.Discard, io.Discard)
	if !errors.Is(err, registry.ErrCodexModelNotListed) {
		t.Fatalf("error = %v, want the Codex roster sentinel", err)
	}
}
