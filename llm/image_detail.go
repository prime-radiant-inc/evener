package llm

import (
	"strings"

	"primeradiant.com/evener/llm/registry"
)

// ChatImageDetail is the detail a Chat Completions image part carries on the
// wire, from the part's own value or, when it names none, the row's
// image_detail cap. The wire accepts only auto|low|high: "original" (the cap
// vocabulary's full-resolution value) maps to "high" and anything else is
// dropped, so a strict endpoint cannot reject the request (#4489). A row set to
// "omit" drops the field even when the part names a detail, matching the
// Responses builder. The Chat Completions builder and the token estimator share
// this, so the billed detail is the sent one.
func ChatImageDetail(partDetail, capDetail string) string {
	rowDetail := strings.ToLower(strings.TrimSpace(capDetail))
	if rowDetail == "omit" {
		return ""
	}
	d := strings.ToLower(strings.TrimSpace(partDetail))
	if d == "" {
		d = rowDetail
	}
	switch d {
	case "original":
		return "high"
	case "auto", "low", "high":
		return d
	default:
		return ""
	}
}

// ResponsesImageDetail is the detail a Responses image part carries on the wire:
// none when the row's image_detail is "omit", else the part's own value, else
// the row's value, else the wire's "high" default. The part's and the row's
// values ride verbatim; this builder does not map them to a vocabulary.
func ResponsesImageDetail(partDetail, capDetail string) string {
	if capDetail == "omit" {
		return ""
	}
	if partDetail != "" {
		return partDetail
	}
	if capDetail != "" {
		return capDetail
	}
	return "high"
}

// imageDetail is the detail the token estimator charges for an image under a
// protocol: the protocol's own wire rule for openai-chat and openai-responses,
// and the part's own value alone for every other protocol, whose builders emit
// no detail field. An empty result means the field is omitted. It is unexported
// because each builder calls its own protocol's rule directly
// (ChatImageDetail, ResponsesImageDetail); only the estimator needs to dispatch
// on the protocol.
func imageDetail(protocol, partDetail, capDetail string) string {
	switch protocol {
	case registry.ProtocolOpenAIChat:
		return ChatImageDetail(partDetail, capDetail)
	case registry.ProtocolOpenAIResponses:
		return ResponsesImageDetail(partDetail, capDetail)
	default:
		return partDetail
	}
}
