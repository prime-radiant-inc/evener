package agent

// Regression tests for #4217: switching a session to a model that cannot see
// tool-result images must not silently orphan the images read earlier. Request
// assembly drops the bytes the target cannot accept (the text of the result
// stays), and the switch marker tells the user the transcript's images are now
// out of the model's view.

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// imageToolResultMessage is one tool result carrying an image the tool layer
// read earlier in the session, with no describing text beside it.
func imageToolResultMessage() llm.Message {
	return llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: "call_img", Name: "read_file", Content: "[image: chart.png]",
			ImageData: []byte("png-bytes"), ImageMediaType: "image/png",
		},
	}}}
}

// runOneTurn drives a single turn whose only model reply is a communicate call,
// so the round completes without a tool loop, and returns the request the
// session dispatched.
func runOneTurn(t *testing.T, sess *Session, adapter *fakeAdapter) llm.Request {
	t.Helper()
	adapter.steps = []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(communicateCall("c1", "ok")) },
	}
	// TRIPWIRE: scripted in-process adapters; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("no provider request recorded")
	}
	return reqs[len(reqs)-1]
}

// toolResultImages returns every image payload still riding in the request's
// tool-result messages.
func toolResultImages(req llm.Request) [][]byte {
	var out [][]byte
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil && len(p.ToolResult.ImageData) > 0 {
				out = append(out, p.ToolResult.ImageData)
			}
		}
	}
	return out
}

// TestChatTargetRequestDropsUnseenToolResultImages is the reproduced defect: a
// chat-protocol target cannot see tool-result images, so the bytes must not be
// sent to it. The result's text still reaches the model.
func TestChatTargetRequestDropsUnseenToolResultImages(t *testing.T) {
	t.Parallel()
	profile := newOpenAICompatProfile("kimi", "kimi-k3", 0)
	if profile.SeesToolResultMedia("image/png") {
		t.Fatal("test premise: the chat fixture row must not see tool-result images")
	}
	adapter := &fakeAdapter{name: "kimi"}
	sess := newSession(t, withProfile(profile), withAdapter(adapter))
	sess.appendTurn(schema.TurnToolResults, imageToolResultMessage())

	req := runOneTurn(t, sess, adapter)
	if got := toolResultImages(req); len(got) != 0 {
		t.Fatalf("chat request carried %d tool-result image(s); an image-blind target must not receive them", len(got))
	}
	if !strings.Contains(toolResultText(req.Messages), "[image: chart.png]") {
		t.Fatalf("request lost the tool result's text: %+v", req.Messages)
	}
}

// TestAnthropicTargetRequestKeepsSeenToolResultImages keeps the other side:
// an image-capable target still receives the bytes.
func TestAnthropicTargetRequestKeepsSeenToolResultImages(t *testing.T) {
	t.Parallel()
	profile := newAnthropicProfile("claude-opus-4-6")
	if !profile.SeesToolResultMedia("image/png") {
		t.Fatal("test premise: the Anthropic fixture row must see tool-result images")
	}
	adapter := &fakeAdapter{name: "anthropic"}
	sess := newSession(t, withProfile(profile), withAdapter(adapter))
	sess.appendTurn(schema.TurnToolResults, imageToolResultMessage())

	req := runOneTurn(t, sess, adapter)
	if got := toolResultImages(req); len(got) != 1 {
		t.Fatalf("Anthropic request carried %d tool-result image(s), want 1", len(got))
	}
}

// TestExpandHistory_LegacyTurnToolDropsUnseenImage covers the old TOOL turn
// kind older transcripts use: that branch goes through projectTurnMessage, and
// the same target-media filter must apply there or the drop and the marker
// disagree.
func TestExpandHistory_LegacyTurnToolDropsUnseenImage(t *testing.T) {
	t.Parallel()
	turns := []schema.Turn{{Kind: schema.TurnTool, Message: imageToolResultMessage()}}
	out := expandHistory(turns, replayScope{dropsToolResultMedia: func(string) bool { return true }})
	for _, m := range out {
		for _, p := range m.Content {
			if p.Kind == llm.ContentToolResult && p.ToolResult != nil && len(p.ToolResult.ImageData) > 0 {
				t.Fatal("legacy TOOL turn sent an image the target cannot see")
			}
		}
	}
	if !strings.Contains(toolResultText(out), "[image: chart.png]") {
		t.Fatalf("legacy TOOL turn lost the result text: %+v", out)
	}
}

// TestOrphanedToolResultMediaCount pins the transition arithmetic behind the
// switch notice: only media the departing profile saw and the new one cannot is
// a new orphan.
func TestOrphanedToolResultMediaCount(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{{Kind: schema.TurnToolResults, Message: imageToolResultMessage()}}
	seeing := newAnthropicProfile("claude-opus-4-6")
	blind := newOpenAICompatProfile("kimi", "kimi-k3", 0)
	if !seeing.SeesToolResultMedia("image/png") || blind.SeesToolResultMedia("image/png") {
		t.Fatal("test premise: one fixture must see tool-result images and the other must not")
	}
	cases := []struct {
		name     string
		from, to *provider.Profile
		want     int
	}{
		{"seeing to blind orphans one", seeing, blind, 1},
		{"blind to blind orphans none", blind, blind, 0},
		{"blind to seeing orphans none", blind, seeing, 0},
	}
	for _, tc := range cases {
		if got := orphanedToolResultMediaCount(history, tc.from, tc.to); got != tc.want {
			t.Errorf("%s: count = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestModelSwitchMarkerCarriesTheOrphanCount pins the marker plumbing without
// pinning prose: a nonzero count changes the marker and the count appears in
// it; a zero count leaves the bare switch line.
func TestModelSwitchMarkerCarriesTheOrphanCount(t *testing.T) {
	t.Parallel()
	from, to := newAnthropicProfile("claude-opus-4-6"), newOpenAICompatProfile("kimi", "kimi-k3", 0)
	bare := buildModelSwitchMarkerText(modelSwitchMarkerParams{oldProfile: from, nextProfile: to})
	one := buildModelSwitchMarkerText(modelSwitchMarkerParams{oldProfile: from, nextProfile: to, orphanedToolResultMedia: 1})
	if one == bare {
		t.Fatal("a nonzero orphan count did not change the marker")
	}
	if !strings.Contains(one, "1") {
		t.Fatalf("marker = %q, want the orphan count", one)
	}
	if zero := buildModelSwitchMarkerText(modelSwitchMarkerParams{oldProfile: from, nextProfile: to, orphanedToolResultMedia: 0}); zero != bare {
		t.Fatalf("a zero count changed the marker: %q", zero)
	}
}

// TestSetModel_ImageBlindSwitchCountsOrphanedMedia drives the switch and
// asserts the count the marker is built from, not the warning's wording.
func TestSetModel_ImageBlindSwitchCountsOrphanedMedia(t *testing.T) {
	t.Parallel()
	profile := newAnthropicProfile("claude-opus-4-6")
	if !profile.SeesToolResultMedia("image/png") {
		t.Fatal("test premise: the Anthropic fixture row must see tool-result images")
	}
	sess := newSession(t,
		withProfile(profile),
		withAdapter(&fakeAdapter{name: "anthropic"}),
		withAdapter(&fakeAdapter{name: "kimi"}),
		withConfig(SessionConfig{ResolveProfile: testResolver, testOnly: testConfig{skipGitSnapshot: true}}),
	)
	sess.appendTurn(schema.TurnToolResults, imageToolResultMessage())

	before := sess.currentProfile()
	if err := sess.SetModel("kimi/kimi-k3"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	after := sess.currentProfile()
	if after.SeesToolResultMedia("image/png") {
		t.Fatal("test premise: the target must be image-blind")
	}
	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if got := orphanedToolResultMediaCount(history, before, after); got != 1 {
		t.Fatalf("switch orphan count = %d, want 1", got)
	}
}

// TestSetModel_BlindToBlindSwitchReportsNoNewOrphans pins the transition
// arithmetic: an image neither model could see was already out of view, so the
// second switch must not claim it orphaned anything.
func TestSetModel_BlindToBlindSwitchReportsNoNewOrphans(t *testing.T) {
	t.Parallel()
	profile := newOpenAICompatProfile("kimi", "kimi-k3", 0)
	if profile.SeesToolResultMedia("image/png") {
		t.Fatal("test premise: the chat fixture row must be image-blind")
	}
	sess := newSession(t,
		withProfile(profile),
		withAdapter(&fakeAdapter{name: "kimi"}),
		withAdapter(&fakeAdapter{name: "glm"}),
		withConfig(SessionConfig{ResolveProfile: testResolver, testOnly: testConfig{skipGitSnapshot: true}}),
	)
	sess.appendTurn(schema.TurnToolResults, imageToolResultMessage())

	before := sess.currentProfile()
	if err := sess.SetModel("glm/glm-4.6"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if got := orphanedToolResultMediaCount(history, before, sess.currentProfile()); got != 0 {
		t.Fatalf("blind-to-blind orphan count = %d, want 0", got)
	}
}

// TestFallbackRequestDropsMediaTheFallbackCannotSee covers the shared-request
// gap a review found: the fallback reuses the primary's built messages, so
// without this projection an image-capable primary would hand tool-result
// images to an image-blind fallback while the fallback's own budget counted
// none.
func TestFallbackRequestDropsMediaTheFallbackCannotSee(t *testing.T) {
	t.Parallel()
	primary := provider.FromResolved(registry.Resolved{
		Instance: "gw", ModelID: "vis", WireID: "vis", Protocol: registry.ProtocolOpenAIResponses, Surface: registry.SurfaceOpenAI,
		Caps: registry.Caps{InputModalities: []string{"text", "image"}},
	}, nil)
	blind := provider.FromResolved(registry.Resolved{
		Instance: "gwb", ModelID: "blind", WireID: "blind", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceOpenAI,
		Caps: registry.Caps{InputModalities: []string{"text"}},
	}, nil)
	primaryAdapter := &agenttest.ScriptedAdapter{
		Provider: "gw",
		FaultResponder: func(llm.Request) error {
			return llm.ErrorFromHTTPStatus("gw", 404, "model not found", nil, nil)
		},
		Responder: func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c0", "unused"))
		},
	}
	blindAdapter := &agenttest.ScriptedAdapter{
		Provider: "gwb",
		Responder: func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("c1", "ok"))
		},
	}
	sess := newSession(t,
		withProfile(primary),
		withAdapter(primaryAdapter),
		withAdapter(blindAdapter),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"gwb/blind"},
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				if ref == "gwb/blind" {
					return blind, nil
				}
				return nil, nil
			},
			testOnly: testConfig{skipGitSnapshot: true},
		}),
	)
	sess.appendTurn(schema.TurnToolResults, imageToolResultMessage())

	// TRIPWIRE: scripted in-process adapters; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "continue", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	primaryReqs := primaryAdapter.Requests()
	if len(primaryReqs) == 0 || len(toolResultImages(primaryReqs[0])) == 0 {
		t.Fatal("test premise: the image-capable primary must carry the image")
	}
	blindReqs := blindAdapter.Requests()
	if len(blindReqs) == 0 {
		t.Fatal("the fallback never ran")
	}
	if got := toolResultImages(blindReqs[0]); len(got) != 0 {
		t.Fatalf("image-blind fallback received %d tool-result image(s)", len(got))
	}
	if !strings.Contains(toolResultText(blindReqs[0].Messages), "[image: chart.png]") {
		t.Fatal("the fallback lost the tool result's text")
	}
}

// toolResultText concatenates every tool result's text content.
func toolResultText(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
		for _, p := range m.Content {
			if p.Kind != llm.ContentToolResult || p.ToolResult == nil {
				continue
			}
			if s, ok := p.ToolResult.Content.(string); ok {
				b.WriteString(s)
				b.WriteByte('\n')
			}
		}
	}
	return b.String()
}
