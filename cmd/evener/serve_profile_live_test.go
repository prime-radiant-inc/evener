package main

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// codexLiveListProtocol stands in for the responses protocol on a Codex
// instance: its live listing is the roster the test controls, with no HTTP.
type codexLiveListProtocol struct {
	id   string
	rows []registry.Model
}

func (p codexLiveListProtocol) ID() string { return p.id }
func (codexLiveListProtocol) PrunablePaths() []string {
	return nil
}
func (codexLiveListProtocol) BuildBody(llm.Request, registry.Resolved) (map[string]any, error) {
	return map[string]any{}, nil
}
func (codexLiveListProtocol) Complete(context.Context, llm.Request, registry.Resolved) (llm.Response, error) {
	return llm.Response{}, nil
}
func (codexLiveListProtocol) Stream(context.Context, llm.Request, registry.Resolved) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (p codexLiveListProtocol) ListModels(context.Context, registry.Resolved) ([]registry.Model, error) {
	return p.rows, nil
}
func (codexLiveListProtocol) CountTokens(context.Context, llm.Request, registry.Resolved) (int, error) {
	return 0, llm.ErrInputTokenCountUnsupported
}

// registerCodexLiveProtocol is idempotent: the protocol registry is
// process-global, so a repeated test run (-count>1) must not panic on an id
// it already holds.
func registerCodexLiveProtocol(p codexLiveListProtocol) {
	if _, ok := llm.ProtocolFor(p.ID()); ok {
		return
	}
	llm.RegisterProtocol(p)
}

func codexLiveClient(t *testing.T, protocolID string) *llm.Client {
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

// TestBuildInitialProfileUsesLiveCodexRoster pins the restart path for a model
// the Codex backend serves but the bundled roster does not carry yet. A running
// session can select such a model because its client has the live listing; a
// restarted process resolves the persisted model before it ever lists, so the
// catalog-only allowlist rejected it. The build must consult the instance's
// live roster before rejecting the model the session was already using.
func TestBuildInitialProfileUsesLiveCodexRoster(t *testing.T) {
	const protocolID = "test-proto-codex-live-roster"
	registerCodexLiveProtocol(codexLiveListProtocol{id: protocolID, rows: []registry.Model{{ID: "gpt-reserve"}}})
	client := codexLiveClient(t, protocolID)

	profile, startupListing, err := buildInitialProfile(context.Background(), client, cmdutil.ModelRef{Provider: "codex-plan", Model: "gpt-reserve"}, "")
	if err != nil {
		t.Fatalf("buildInitialProfile rejected a Codex model its instance lists live: %v", err)
	}
	if profile.ID() != "codex-plan" || profile.Model() != "gpt-reserve" {
		t.Fatalf("profile = %s/%s, want codex-plan/gpt-reserve", profile.ID(), profile.Model())
	}
	if startupListing == nil || startupListing.Instance != "codex-plan" || !startupListing.Listing.Usable {
		t.Fatalf("startup listing = %+v, want the usable codex-plan roster for reuse", startupListing)
	}
}

// TestBuildInitialProfileKeepsCodexRosterRejection pins the other side: a model
// the instance's live roster does not carry still fails with the registry's
// Codex sentinel.
func TestBuildInitialProfileKeepsCodexRosterRejection(t *testing.T) {
	const protocolID = "test-proto-codex-live-reject"
	registerCodexLiveProtocol(codexLiveListProtocol{id: protocolID, rows: []registry.Model{{ID: "gpt-5.6"}}})
	client := codexLiveClient(t, protocolID)

	_, _, err := buildInitialProfile(context.Background(), client, cmdutil.ModelRef{Provider: "codex-plan", Model: "gpt-reserve"}, "")
	if !errors.Is(err, registry.ErrCodexModelNotListed) {
		t.Fatalf("error = %v, want the Codex roster sentinel", err)
	}
}
