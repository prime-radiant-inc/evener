package llm

import "strings"

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
