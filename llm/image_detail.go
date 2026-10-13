package llm

import "strings"

// ChatImageDetail is the detail a Chat Completions image part carries on the
// wire. The part's own value wins when it names one, except that a row whose
// image_detail is "omit" drops the field outright: that value means the row
// rejects or mishandles detail, so even an explicitly named part detail is
// omitted. The chat wire accepts only OpenAI's auto|low|high, so the cap
// vocabulary's "original" (full resolution) maps to "high", and an unset value
// drops the field. The Chat Completions builder and the token estimator both
// call this, so the detail that is billed matches the one that is sent (the
// Responses builder carries its row's value verbatim instead).
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
	case "", "omit":
		return ""
	case "original":
		return "high"
	default:
		return d
	}
}
