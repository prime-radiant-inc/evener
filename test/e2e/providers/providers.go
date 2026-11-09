// Package providers is the one writer of the providers.toml files the e2e
// harnesses hand to a disposable hub (#4445).
//
// Three harnesses used to spell the registry schema by hand - a shell
// heredoc, a sed-substituted fixture, and a Go fmt.Sprintf block - so a schema
// change could leave one behind with nothing failing (#4411, #4437). Harnesses
// now describe their fake provider and let Render marshal it through the
// registry's own writer; shell harnesses run the command in ./cmd, Go
// harnesses import this package.
package providers

import (
	"fmt"

	"primeradiant.com/evener/llm/registry"
)

// baseProvider is the curated registry row a fake backend inherits: the same
// openai-compatible base the hand-written configs named, so the hub resolves
// the fake the way it resolves a real OpenAI-compatible endpoint.
const baseProvider = "openai-compatible"

// Config describes the single fake provider an e2e harness points a
// disposable hub at. Name is both the default provider and its map key.
type Config struct {
	Name                   string
	BaseURL                string
	APIKey                 string
	SessionAffinityHeaders bool
	ContextWindow          int
}

// Render returns a providers.toml in the registry schema the hub loads
// (docs/superpowers/specs/2026-08-28-provider-registry-design.md §14.1). It
// builds the Layer and lets registry.MarshalConfig write it, so the harnesses
// share the config format's one serializer instead of growing a second TOML
// writer. Optional fields are omitted when unset, so the fake429 config
// carries no credential and the turncpu config can add a context window.
func Render(c Config) ([]byte, error) {
	provider := registry.Provider{
		Base:      baseProvider,
		Transport: registry.Transport{BaseURL: c.BaseURL},
		APIKey:    c.APIKey,
	}
	if c.SessionAffinityHeaders {
		provider.Caps.SessionAffinityHeaders = new(true)
	}
	if c.ContextWindow > 0 {
		provider.Caps.ContextWindow = new(c.ContextWindow)
	}
	layer := &registry.Layer{
		Default:   c.Name,
		Providers: map[string]registry.Provider{c.Name: provider},
	}
	data, err := registry.MarshalConfig(layer)
	if err != nil {
		return nil, fmt.Errorf("render providers.toml: %w", err)
	}
	return data, nil
}
