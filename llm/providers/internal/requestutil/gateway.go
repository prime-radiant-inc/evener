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
// readers share it so they cannot disagree about the precedence.
func GatewayInputModalities(arch GatewayArchitecture, caps GatewayCapabilities) []string {
	if mods := DedupStrings(arch.InputModalities); len(mods) > 0 {
		return mods
	}
	if caps.Vision != nil && *caps.Vision {
		return []string{"text", "image"}
	}
	return nil
}

// DedupStrings drops empty entries and duplicates from an advertised list, in
// first-seen order, and returns nil when nothing survives. Every live-listing
// modality or effort list goes through it: a raw list a gateway publishes would
// otherwise merge wholesale over a curated one, and an empty one would erase it
// (llm/registry/merge.go).
func DedupStrings(in []string) []string {
	var out []string
	for _, s := range in {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}
