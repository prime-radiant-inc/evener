package requestutil

import (
	"bytes"
	"encoding/json"
	"strings"

	"primeradiant.com/evener/llm/registry"
)

// ListedReasoning is a /models row's reasoning fact. OpenRouter publishes
// an object (mandatory, default_enabled, supported_efforts); other
// OpenAI-compatible servers publish a plain boolean verdict. Both spell
// one fact — whether the model reasons — and neither may fail the listing,
// so any shape evener cannot read states nothing.
type ListedReasoning struct {
	Mandatory        *bool
	DefaultEnabled   *bool
	SupportedEfforts []string
	Verdict          *bool
}

// UnmarshalJSON reads the verdict and the object as one fact: a decode of
// either shape that fails leaves the field at its zero value rather than
// failing the row.
func (f *ListedReasoning) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		return nil
	}
	var verdict bool
	if err := json.Unmarshal(b, &verdict); err == nil {
		f.Verdict = &verdict
		return nil
	}
	var obj struct {
		Mandatory        *bool    `json:"mandatory"`
		DefaultEnabled   *bool    `json:"default_enabled"`
		SupportedEfforts []string `json:"supported_efforts"`
	}
	if err := json.Unmarshal(b, &obj); err == nil {
		f.Mandatory, f.DefaultEnabled, f.SupportedEfforts = obj.Mandatory, obj.DefaultEnabled, obj.SupportedEfforts
	}
	return nil
}

// ListedReasoningLevels is a /models row's supported_reasoning_levels
// ladder: the Codex backend publishes effort objects, other
// OpenAI-compatible servers plain strings. An entry of any other shape
// states nothing, and neither spelling may fail the listing.
type ListedReasoningLevels []string

func (l *ListedReasoningLevels) UnmarshalJSON(b []byte) error {
	var entries []json.RawMessage
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		var name string
		if err := json.Unmarshal(entry, &name); err == nil {
			out = append(out, name)
			continue
		}
		var obj struct {
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(entry, &obj); err == nil {
			out = append(out, obj.Effort)
		}
	}
	*l = out
	return nil
}

// ListedLimits is a /models row's own output-cap object (a local server's
// shape).
type ListedLimits struct {
	MaxOutputTokens int `json:"max_output_tokens"`
}

// NormalizeReasoningEfforts trims a listing's advertised reasoning-effort
// names and drops the empty ones, the duplicates and `ultra` — a Codex
// client delegation preset, not an effort the wire accepts. Served order
// survives. Both /models readers share it, so a level list keeps one
// meaning across listings.
func NormalizeReasoningEfforts(levels []string) []string {
	if len(levels) == 0 {
		return nil
	}
	out := make([]string, 0, len(levels))
	seen := make(map[string]bool, len(levels))
	for _, level := range levels {
		effort := strings.TrimSpace(level)
		if effort == "" || strings.EqualFold(effort, "ultra") || seen[effort] {
			continue
		}
		seen[effort] = true
		out = append(out, effort)
	}
	return out
}

// NormalizeDefaultReasoningEffort returns the effort name a listing states
// a model runs at when the request omits one, or "" when it states nothing
// usable. `ultra` is the Codex client's delegation preset, not an effort
// the wire accepts.
func NormalizeDefaultReasoningEffort(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "ultra") {
		return ""
	}
	return s
}

// ApplyListedReasoningFacts folds a row's reasoning facts into caps, in the
// order the listings spell them: the effort ladder and its default in the
// Codex backend's supported_reasoning_levels / default_reasoning_level,
// then OpenRouter's reasoning object, then a plain boolean verdict. A
// usable ladder is itself evidence the model tunes effort; the explicit
// verdict is the row's own statement about the model, so it wins in both
// directions and a false one carries no ladder, whatever levels the row
// also lists. Both /models readers share this so a row's facts cannot
// resolve differently by protocol.
func ApplyListedReasoningFacts(caps *registry.Caps, r ListedReasoning, levels []string, defaultEffort string) {
	if efforts := NormalizeReasoningEfforts(levels); len(efforts) > 0 {
		caps.EffortValues = efforts
		caps.Reasoning = new(true)
	}
	if d := NormalizeDefaultReasoningEffort(defaultEffort); d != "" {
		caps.DefaultEffort = new(d)
	}
	if r.Mandatory != nil || r.DefaultEnabled != nil || len(r.SupportedEfforts) > 0 {
		caps.Reasoning = new(true)
		if r.Mandatory != nil && *r.Mandatory {
			caps.ThinkingAlwaysOn = new(true)
		}
		caps.EffortValues = DedupStrings(r.SupportedEfforts)
	}
	if v := r.Verdict; v != nil {
		caps.Reasoning = v
		if !*v {
			caps.EffortValues = nil
		}
	}
}

// DecodeListingRows decodes a listing's rows independently and returns the
// decodable ones. A row whose shape the reader cannot read is dropped, so
// one alien row cannot fail the healthy ones; firstErr names the first
// failure so the caller can still fail a listing nothing survived.
func DecodeListingRows[T any](raw []json.RawMessage) (rows []T, firstErr error) {
	for _, r := range raw {
		var e T
		if err := json.Unmarshal(r, &e); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		rows = append(rows, e)
	}
	return rows, firstErr
}
