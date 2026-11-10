package cmdutil

import (
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// ErrFastCheapProviderUnavailable identifies a --fast-cheap-model provider the
// client cannot route a side call to.
var ErrFastCheapProviderUnavailable = errors.New("is not configured or has no credential")

// ApplyFastCheapModel sets the auxiliary cheap model for side calls. The ref is
// "provider/model" to route side calls to a different provider instance than the
// active model, or a bare "model" to keep the active provider. A cross-provider
// cheap provider must be registered in the client (i.e. configured AND
// credentialed) — that is what actually lets the side call route, unlike a
// config-only check which is credential-blind.
func ApplyFastCheapModel(profile *provider.Profile, raw string, client *llm.Client) (*provider.Profile, error) {
	if profile == nil || strings.TrimSpace(raw) == "" {
		return profile, nil
	}
	raw = strings.TrimSpace(raw)
	if cheapProvider, model, ok := strings.Cut(raw, "/"); ok && cheapProvider != "" && model != "" && cheapProvider != profile.ID() {
		if !client.HasProvider(cheapProvider) {
			return nil, fmt.Errorf("--fast-cheap-model provider %q %w (active provider %q); available providers: %s",
				cheapProvider, ErrFastCheapProviderUnavailable, profile.ID(), strings.Join(client.ProviderNames(), ", "))
		}
	}
	return provider.WithCheapModel(profile, raw), nil
}
