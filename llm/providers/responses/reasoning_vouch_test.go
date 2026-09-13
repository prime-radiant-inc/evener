package responses

import (
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// An empty ladder vouches for no level, so the Responses reasoning object must
// not spell one. This is the same bug class as the lunaroute 400 on the
// chat-completions side. ReasoningControls must list "effort" so the row is
// effort-capable and the vouch gate actually runs.
func TestReasoningObject_EmptyLadderWritesNoEffort(t *testing.T) {
	high := "high"
	caps := registry.Caps{Reasoning: new(true), ReasoningControls: []string{"effort"}}
	if got := reasoningObject(llm.Request{ReasoningEffort: &high}, caps); got != nil {
		t.Fatalf("empty-ladder row got reasoning = %v, want nil", got)
	}
}

// A vouched level is clamped into the row's ladder and written by name.
func TestReasoningObject_VouchedEffortClamps(t *testing.T) {
	xhigh := "xhigh"
	got := reasoningObject(llm.Request{ReasoningEffort: &xhigh}, registry.Caps{
		Reasoning: new(true), ReasoningControls: []string{"effort"}, EffortValues: []string{"low", "high"},
	})
	if got == nil || got["effort"] != "high" {
		t.Fatalf("reasoning = %v, want effort high", got)
	}
}

// An effort-capable row whose ladder vouches for nothing still reasons: the
// requested level sends no effort name, but the encrypted-reasoning include
// must survive so a gateway that returns it can replay on later turns.
func TestBuildBody_UnvouchedEffortKeepsEncryptedReasoningInclude(t *testing.T) {
	high := "high"
	req := userReq("hi")
	req.ReasoningEffort = &high
	res := resolved(func(c *registry.Caps) {
		c.Reasoning = new(true)
		c.ReasoningControls = []string{"effort"}
	})
	body := build(t, req, res)
	if _, has := body["reasoning"]; has {
		t.Fatalf("an unvouched effort must not send a reasoning object: %v", body)
	}
	inc, _ := body["include"].([]string)
	found := false
	for _, v := range inc {
		if v == "reasoning.encrypted_content" {
			found = true
		}
	}
	if !found {
		t.Fatalf("include = %v, want reasoning.encrypted_content so replay keeps working", inc)
	}
}

// A configured summary is a request for a summary, not an effort name, so it
// still rides when the ladder cannot vouch for the requested level.
func TestBuildBody_UnvouchedEffortKeepsConfiguredSummary(t *testing.T) {
	high := "high"
	req := userReq("hi")
	req.ReasoningEffort = &high
	res := resolved(func(c *registry.Caps) {
		c.Reasoning = new(true)
		c.ReasoningControls = []string{"effort"}
		c.ReasoningSummary = new("auto")
	})
	body := build(t, req, res)
	r, _ := body["reasoning"].(map[string]any)
	if r == nil || r["summary"] != "auto" {
		t.Fatalf("reasoning = %v, want the configured summary", body["reasoning"])
	}
	if _, has := r["effort"]; has {
		t.Fatalf("summary-only object must not carry an unvouched effort: %v", r)
	}
}
