package requestutil

import "slices"

// GatewayArchitecture is the model-facts object an OpenRouter-shaped /models
// listing publishes per row.
type GatewayArchitecture struct {
	InputModalities []string `json:"input_modalities"`
}

// GatewayCapabilities is the capability object some OpenAI-compatible gateways
// publish per /models row (lunaroute's shape). Only vision affects model
// resolution today.
type GatewayCapabilities struct {
	Vision *bool `json:"vision"`
}

// GatewayInputModalities is the input modality list a /models row advertises:
// architecture.input_modalities when that list carries any entry, else text
// plus image for an advertised vision capability, else nil. The protocol
// readers share it so they cannot disagree about the precedence. The result is
// nil, never an empty slice, when the row advertises nothing: mergeCaps would
// otherwise replay the live layer's empty list over a curated one.
func GatewayInputModalities(arch GatewayArchitecture, caps GatewayCapabilities) []string {
	if mods := advertisedModalities(arch.InputModalities); len(mods) > 0 {
		return mods
	}
	if caps.Vision != nil && *caps.Vision {
		return []string{"text", "image"}
	}
	return nil
}

// advertisedModalities drops empty entries and duplicates from an advertised
// modality list, in first-seen order.
func advertisedModalities(in []string) []string {
	out := make([]string, 0, len(in))
	for _, m := range in {
		if m != "" && !slices.Contains(out, m) {
			out = append(out, m)
		}
	}
	return out
}
