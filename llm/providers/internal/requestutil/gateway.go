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
// architecture.input_modalities when that list is non-empty, else text plus
// image for an advertised vision capability, else nil. The protocol readers
// share it so they cannot disagree about the precedence, and the estimate of
// what a row accepts follows one rule.
func GatewayInputModalities(arch GatewayArchitecture, caps GatewayCapabilities) []string {
	if len(arch.InputModalities) > 0 {
		out := make([]string, 0, len(arch.InputModalities))
		for _, m := range arch.InputModalities {
			if m != "" && !slices.Contains(out, m) {
				out = append(out, m)
			}
		}
		return out
	}
	if caps.Vision != nil && *caps.Vision {
		return []string{"text", "image"}
	}
	return nil
}
