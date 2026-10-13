package llm

import "strings"

// ChatImageDetail is the detail a Chat Completions image part carries on the
// wire. The wire accepts only OpenAI's auto|low|high, so every other value is
// dropped rather than sent: a strict OpenAI-compatible endpoint rejects an
// unknown value with HTTP 400, which is the failure #4489 reported. The part's
// own value is normalized first, then the row's image_detail cap when the part
// names none; the cap vocabulary's "original" (full resolution) maps to "high".
// A row set to "omit" drops the field outright, even when the part names a
// detail, matching the Responses builder's reading of that value. The Responses
// builder otherwise carries the part's own detail or the row's value; only Chat
// Completions needs this wire-value mapping.
//
// The Chat Completions builder and the token estimator both call this, so the
// detail that is billed matches the one that is sent.
func ChatImageDetail(partDetail, capDetail string) string {
	cap := strings.ToLower(strings.TrimSpace(capDetail))
	if cap == "omit" {
		return ""
	}
	d := strings.ToLower(strings.TrimSpace(partDetail))
	if d == "" {
		d = cap
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
