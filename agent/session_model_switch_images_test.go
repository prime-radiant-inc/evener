package agent

// Regression tests for #4217: switching a session to a model that cannot see
// tool-result images must not silently orphan the images read earlier. Request
// assembly drops the bytes the target cannot accept (the text of the result
// stays), and the switch marker tells the user the transcript's images are now
// out of the model's view.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/skill"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/internal/testwait"
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
	adapter.setSteps([]func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(communicateCall("c1", "ok")) },
	})
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

// fallbackMediaProfiles builds the production one-registry shape the fallback
// media tests need: the client and the profiles resolve from the same rows, so
// shared client shaping and request assembly make the same media decision.
func fallbackMediaProfiles(t *testing.T, primaryProtocol string, primaryCaps registry.Caps, fallbackProtocol string, fallbackCaps registry.Caps) (*registry.Registry, *llm.Client, *provider.Profile, *provider.Profile) {
	t.Helper()
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"gw": {
				Surface: registry.SurfaceOpenAI, Protocol: primaryProtocol, APIKey: "k",
				DefaultModel: "primary", InheritModels: new(false),
				Transport: registry.Transport{BaseURL: "http://gw.test.invalid/v1"},
				Models:    map[string]registry.Model{"primary": {Caps: primaryCaps}},
			},
			"gwb": {
				Surface: registry.SurfaceOpenAI, Protocol: fallbackProtocol, APIKey: "k",
				DefaultModel: "fallback", InheritModels: new(false),
				Transport: registry.Transport{BaseURL: "http://gwb.test.invalid/v1"},
				Models:    map[string]registry.Model{"fallback": {Caps: fallbackCaps}},
			},
		}),
	)
	if err != nil {
		t.Fatalf("fixture registry: %v", err)
	}
	primary, err := provider.Resolve(r, "gw/primary")
	if err != nil {
		t.Fatalf("resolve primary: %v", err)
	}
	fallback, err := provider.Resolve(r, "gwb/fallback")
	if err != nil {
		t.Fatalf("resolve fallback: %v", err)
	}
	return r, llm.NewClient(llm.WithRegistry(r)), primary, fallback
}

// TestFallbackRequestDropsMediaTheFallbackCannotSee covers one direction: an
// image-capable primary must not hand tool-result images to an image-blind
// fallback, and the fallback's own budget then prices what it receives.
func TestFallbackRequestDropsMediaTheFallbackCannotSee(t *testing.T) {
	t.Parallel()
	r, client, primary, _ := fallbackMediaProfiles(t,
		registry.ProtocolOpenAIResponses, registry.Caps{InputModalities: []string{"text", "image"}},
		registry.ProtocolOpenAIChat, registry.Caps{InputModalities: []string{"text"}})
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
	client.Register(primaryAdapter)
	client.Register(blindAdapter)
	sess := newSession(t,
		withClient(client),
		withProfile(primary),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"gwb/fallback"},
			ResolveProfile: func(ref string) (*provider.Profile, error) { return provider.Resolve(r, ref) },
			testOnly:       testConfig{skipGitSnapshot: true},
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

// TestFallbackRetainsImagesThePrimaryCannotSee is the other direction: the
// blind primary's request omits the image, but an image-capable fallback must
// still receive it. Primary assembly alone cannot answer this, so the round
// keeps its unfiltered messages and each dispatch target projects its own.
func TestFallbackRetainsImagesThePrimaryCannotSee(t *testing.T) {
	t.Parallel()
	r, client, primary, _ := fallbackMediaProfiles(t,
		registry.ProtocolOpenAIChat, registry.Caps{InputModalities: []string{"text"}},
		registry.ProtocolAnthropic, registry.Caps{InputModalities: []string{"text", "image"}})
	primaryAdapter := &agenttest.ScriptedAdapter{
		Provider: "gw",
		FaultResponder: func(llm.Request) error {
			return llm.ErrorFromHTTPStatus("gw", 404, "model not found", nil, nil)
		},
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("unused") },
	}
	fallbackAdapter := &agenttest.ScriptedAdapter{
		Provider:  "gwb",
		Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("ok") },
	}
	client.Register(primaryAdapter)
	client.Register(fallbackAdapter)
	sess := newSession(t,
		withClient(client),
		withProfile(primary),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"gwb/fallback"},
			ResolveProfile: func(ref string) (*provider.Profile, error) { return provider.Resolve(r, ref) },
			testOnly:       testConfig{skipGitSnapshot: true},
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
	if len(primaryReqs) == 0 || len(toolResultImages(primaryReqs[0])) != 0 {
		t.Fatal("the image-blind primary request carried the image")
	}
	fallbackReqs := fallbackAdapter.Requests()
	if len(fallbackReqs) == 0 {
		t.Fatal("the fallback never ran")
	}
	if got := toolResultImages(fallbackReqs[0]); len(got) != 1 {
		t.Fatalf("image-capable fallback received %d image(s), want 1", len(got))
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

// userImageMessage is one user input carrying an image the session accepted
// while a vision-capable model was selected.
func userImageMessage(text string) llm.Message {
	return buildUserInputMessage(text, []ImageAttachment{{
		MediaType: "image/png",
		Data:      []byte("png-bytes"),
	}})
}

// userMessageImages returns every image payload riding in user messages.
func userMessageImages(req llm.Request) [][]byte {
	var out [][]byte
	for _, m := range req.Messages {
		if m.Role != llm.RoleUser {
			continue
		}
		for _, p := range m.Content {
			if p.Kind == llm.ContentImage && p.Image != nil {
				out = append(out, p.Image.Data)
			}
		}
	}
	return out
}

// userImagesInHistory returns every image payload retained in the live
// history, so a request-side projection can be checked against the transcript
// copy it must not touch.
func userImagesInHistory(history []schema.Turn) [][]byte {
	var out [][]byte
	for _, t := range history {
		if t.Message.Role != llm.RoleUser {
			continue
		}
		for _, p := range t.Message.Content {
			if p.Kind == llm.ContentImage && p.Image != nil {
				out = append(out, p.Image.Data)
			}
		}
	}
	return out
}

// TestImageBlindTargetDropsHistoricalUserImages is #7629: an attachment the
// session accepted earlier must not reach a text-only target at all, even
// though the transcript keeps it for the user and for a later capable model.
func TestImageBlindTargetDropsHistoricalUserImages(t *testing.T) {
	t.Parallel()
	profile := newMiniMaxProfile("MiniMax-M2.7")
	if profile.AcceptsImageInput() {
		t.Fatal("test premise: the MiniMax fixture row must not accept image input")
	}
	adapter := &fakeAdapter{name: "minimax"}
	sess := newSession(t, withProfile(profile), withAdapter(adapter))
	sess.appendTurn(schema.TurnUserInput, userImageMessage("what is in this image?"))

	req := runOneTurn(t, sess, adapter)
	if got := userMessageImages(req); len(got) != 0 {
		t.Fatalf("text-only target received %d historical user image(s); the bytes must stay in the transcript", len(got))
	}
	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if got := userImagesInHistory(history); len(got) != 1 {
		t.Fatalf("live history carries %d user image(s), want the original 1: the request projection must not mutate stored turns", len(got))
	}
}

// TestModelSwitchDropsHistoricalUserImagesForImageBlindTarget reproduces the
// reported sequence: a vision model accepted an image, the session switched to
// a text-only model, and the next image-free follow-up must dispatch without
// replaying the old attachment.
func TestModelSwitchDropsHistoricalUserImagesForImageBlindTarget(t *testing.T) {
	t.Parallel()
	vision := newAnthropicProfile("claude-opus-4-6")
	if !vision.AcceptsImageInput() {
		t.Fatal("test premise: the Anthropic fixture row must accept image input")
	}
	blind := newMiniMaxProfile("MiniMax-M2.7")
	if blind.AcceptsImageInput() {
		t.Fatal("test premise: the MiniMax fixture row must not accept image input")
	}
	adapter := &fakeAdapter{name: "minimax"}
	sess := newSession(t,
		withProfile(vision),
		withAdapter(&fakeAdapter{name: "anthropic"}),
		withAdapter(adapter),
		withConfig(SessionConfig{
			ResolveProfile: testResolverFull,
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)
	sess.appendTurn(schema.TurnUserInput, userImageMessage("what is in this image?"))
	if err := sess.SetModel("minimax/MiniMax-M2.7"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}

	req := runOneTurn(t, sess, adapter)
	if got := userMessageImages(req); len(got) != 0 {
		t.Fatalf("after switching to a text-only model, the follow-up replayed %d user image(s)", len(got))
	}
	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if got := userImagesInHistory(history); len(got) != 1 {
		t.Fatalf("live history carries %d user image(s) after the switch, want the original 1", len(got))
	}
}

// TestImageBlindTargetKeepsNewUserImage keeps the other half: an image added
// with the current input still reaches the provider, so a provider that
// refuses it answers with its actionable error instead of the attachment
// vanishing silently.
func TestImageBlindTargetKeepsNewUserImage(t *testing.T) {
	t.Parallel()
	profile := newMiniMaxProfile("MiniMax-M2.7")
	if profile.AcceptsImageInput() {
		t.Fatal("test premise: the MiniMax fixture row must not accept image input")
	}
	adapter := &fakeAdapter{name: "minimax"}
	sess := newSession(t, withProfile(profile), withAdapter(adapter))
	adapter.setSteps([]func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return agenttest.ToolCallResponse(communicateCall("c1", "ok")) },
	})

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "look at this", []ImageAttachment{{MediaType: "image/png", Data: []byte("new-png")}}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reqs := adapter.Requests()
	if len(reqs) == 0 {
		t.Fatal("no provider request recorded")
	}
	req := reqs[len(reqs)-1]
	if got := userMessageImages(req); len(got) != 1 {
		t.Fatalf("the newly added user image reached the request %d time(s), want 1 so the provider can refuse it", len(got))
	}
}

// TestImageCapableTargetKeepsHistoricalUserImages keeps supported routes
// unchanged: a row that accepts image input still sees the transcript's image.
func TestImageCapableTargetKeepsHistoricalUserImages(t *testing.T) {
	t.Parallel()
	profile := newAnthropicProfile("claude-opus-4-6")
	if !profile.AcceptsImageInput() {
		t.Fatal("test premise: the Anthropic fixture row must accept image input")
	}
	adapter := &fakeAdapter{name: "anthropic"}
	sess := newSession(t, withProfile(profile), withAdapter(adapter))
	sess.appendTurn(schema.TurnUserInput, userImageMessage("what is in this image?"))

	req := runOneTurn(t, sess, adapter)
	if got := userMessageImages(req); len(got) != 1 {
		t.Fatalf("image-capable target received %d user image(s), want the retained 1", len(got))
	}
}

// TestImageBlindTargetKeepsSteeringImageAdmittedAfterTurnStart reproduces the
// steering defect: the opening prompt carried no image, so ownership keyed on
// the prompt treated everything in history as historical and stripped the
// image a mid-turn steering message had just admitted. That attachment belongs
// to the in-flight turn and must reach the text-only target for its actionable
// refusal, while the older transcript image stays out of the request.
func TestImageBlindTargetKeepsSteeringImageAdmittedAfterTurnStart(t *testing.T) {
	t.Parallel()
	profile := newMiniMaxProfile("MiniMax-M2.7")
	if profile.AcceptsImageInput() {
		t.Fatal("test premise: the MiniMax fixture row must not accept image input")
	}
	adapter := &fakeAdapter{name: "minimax"}
	sess := newSession(t,
		withProfile(profile),
		withAdapter(adapter),
		withStateDir(t.TempDir()),
	)
	sess.appendTurn(schema.TurnUserInput, userImageMessage("what was in the old image?"))

	steeringImage := []byte("steering-png")
	sawSecondRound := false
	adapter.setSteps([]func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			if got := sess.consumeSteeringMessage(steeringMessage{
				Text:   "look at the new one",
				Images: []ImageAttachment{{MediaType: "image/png", Data: steeringImage}},
			}); got != steeringDelivered {
				t.Errorf("consumeSteeringMessage = %v, want steeringDelivered", got)
			}
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID: "round-one", Name: "task_list", Arguments: []byte(`{}`), Type: "function",
			})
		},
		func(req llm.Request) llm.Response {
			sawSecondRound = true
			got := userMessageImages(req)
			if len(got) != 1 || !bytes.Equal(got[0], steeringImage) {
				t.Errorf("text-only target received %d user image(s), want only the steering attachment admitted after the turn started", len(got))
			}
			return agenttest.ToolCallResponse(communicateCall("done", "ok"))
		},
	})

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "keep going", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !sawSecondRound {
		t.Fatal("the round after the steering admission never ran, so the assertion was vacuous")
	}

	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	baseline := sess.turnHistoryBaseline
	sess.mu.Unlock()
	if got := userImagesInHistory(history); len(got) != 2 {
		t.Fatalf("live history carries %d user image(s), want the original plus the steering one: the request projection must not mutate stored turns", len(got))
	}
	steeringAt := -1
	for i, turn := range history {
		if turn.Kind == schema.TurnSteering && messageHasUserImage(turn.Message) {
			steeringAt = i
		}
	}
	if steeringAt < baseline {
		t.Fatalf("test premise: steering turn index %d is before the in-flight baseline %d", steeringAt, baseline)
	}
}

// TestImageBlindTargetKeepsOpeningPromptAndSteeringImages pins the composition
// of the in-flight turn: the image the opening input carried and the image a
// steering message admitted after the turn started both belong to it, so both
// reach the text-only provider for refusal instead of one displacing the other.
func TestImageBlindTargetKeepsOpeningPromptAndSteeringImages(t *testing.T) {
	t.Parallel()
	profile := newMiniMaxProfile("MiniMax-M2.7")
	if profile.AcceptsImageInput() {
		t.Fatal("test premise: the MiniMax fixture row must not accept image input")
	}
	adapter := &fakeAdapter{name: "minimax"}
	sess := newSession(t,
		withProfile(profile),
		withAdapter(adapter),
		withStateDir(t.TempDir()),
	)
	promptImage := []byte("prompt-png")
	steeringImage := []byte("steering-png")
	sawSecondRound := false
	adapter.setSteps([]func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			if got := sess.consumeSteeringMessage(steeringMessage{
				Text:   "and this one",
				Images: []ImageAttachment{{MediaType: "image/png", Data: steeringImage}},
			}); got != steeringDelivered {
				t.Errorf("consumeSteeringMessage = %v, want steeringDelivered", got)
			}
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID: "round-one", Name: "task_list", Arguments: []byte(`{}`), Type: "function",
			})
		},
		func(req llm.Request) llm.Response {
			sawSecondRound = true
			got := userMessageImages(req)
			if len(got) != 2 || !bytes.Equal(got[0], promptImage) || !bytes.Equal(got[1], steeringImage) {
				t.Errorf("text-only target received %d user image(s), want the opening prompt's and the steering one", len(got))
			}
			return agenttest.ToolCallResponse(communicateCall("done", "ok"))
		},
	})

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "look at this", []ImageAttachment{{MediaType: "image/png", Data: promptImage}}); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !sawSecondRound {
		t.Fatal("the round after the steering admission never ran, so the assertion was vacuous")
	}
}

// TestImageBlindTargetKeepsImageSteeredDuringOpeningPromptHook reproduces the
// opening-hook steering defect: the opening prompt is appended, the
// UserPromptSubmit hook runs, and only then is pending steering drained. A
// steer admitted while that hook blocks is therefore already history when
// round zero records the in-flight boundary, so the image-blind target strips
// it from the first request. The attachment belongs to the in-flight turn and
// must reach the target for its actionable refusal, while the older transcript
// image stays out and the transcript keeps both.
func TestImageBlindTargetKeepsImageSteeredDuringOpeningPromptHook(t *testing.T) {
	t.Parallel()
	steeringImage := []byte("steering-png")
	promptImage := []byte("prompt-png")
	for _, tc := range []struct {
		name        string
		images      []ImageAttachment
		wantImages  [][]byte
		wantHistory int
	}{
		{
			name:        "text-only opening prompt",
			wantImages:  [][]byte{steeringImage},
			wantHistory: 2,
		},
		{
			name:        "opening prompt carries an image",
			images:      []ImageAttachment{{MediaType: "image/png", Data: promptImage}},
			wantImages:  [][]byte{promptImage, steeringImage},
			wantHistory: 3,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := newMiniMaxProfile("MiniMax-M2.7")
			if profile.AcceptsImageInput() {
				t.Fatal("test premise: the MiniMax fixture row must not accept image input")
			}
			dir := t.TempDir()
			entered := filepath.Join(dir, "opening-hook-entered")
			released := filepath.Join(dir, "opening-hook-released")
			hooksJSON, err := json.Marshal(map[string]any{"hooks": map[string]any{
				"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{
					"type": "command",
					"command": "touch " + shellquote.Literal(entered) +
						"; while [ ! -e " + shellquote.Literal(released) + " ]; do sleep 0.02; done",
				}}}},
			}})
			if err != nil {
				t.Fatalf("encode hooks.json: %v", err)
			}
			pluginDir := writePluginHooks(t, "opening-steer-plugin", string(hooksJSON))
			adapter := &fakeAdapter{name: "minimax"}
			sess := newSession(t,
				withProfile(profile),
				withAdapter(adapter),
				withStateDir(t.TempDir()),
				withConfig(SessionConfig{
					PluginDirs: []string{pluginDir},
					testOnly:   testConfig{skipGitSnapshot: true},
				}),
			)
			if sess.hookRunner == nil {
				t.Fatal("test setup: the plugin's UserPromptSubmit hook did not reach a hook runner")
			}
			sess.appendTurn(schema.TurnUserInput, userImageMessage("what was in the old image?"))

			adapter.setSteps([]func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					got := userMessageImages(req)
					if len(got) != len(tc.wantImages) {
						t.Errorf("first request received %d user image(s), want %d", len(got), len(tc.wantImages))
					}
					for i := range tc.wantImages {
						if i >= len(got) || !bytes.Equal(got[i], tc.wantImages[i]) {
							t.Errorf("first request image %d does not match the in-flight attachment it should carry", i)
						}
					}
					return agenttest.ToolCallResponse(communicateCall("done", "ok"))
				},
			})

			// TRIPWIRE: the hook blocks on a marker file this test controls; the
			// 30s context and testwait's budget only catch a genuine hang.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := sess.ProcessInput(ctx, "keep going", tc.images)
				done <- err
			}()
			testwait.Until(t, "the opening UserPromptSubmit hook to block", func() bool {
				_, statErr := os.Stat(entered)
				return statErr == nil
			})
			if ok, err := sess.trySteerWithImages("look at the new one", []ImageAttachment{{MediaType: "image/png", Data: steeringImage}}); err != nil || !ok {
				t.Fatalf("trySteerWithImages = %t, %v; want the steering admitted while the opening hook blocks", ok, err)
			}
			if err := os.WriteFile(released, nil, 0o600); err != nil {
				t.Fatalf("release the opening hook: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}
			if got := len(adapter.Requests()); got == 0 {
				t.Fatal("no provider request recorded")
			}

			sess.mu.Lock()
			history := append([]schema.Turn(nil), sess.history...)
			sess.mu.Unlock()
			if got := userImagesInHistory(history); len(got) != tc.wantHistory {
				t.Fatalf("live history carries %d user image(s), want %d: the request projection must not mutate stored turns", len(got), tc.wantHistory)
			}
			data, err := readTranscriptFull(context.Background(), sess.TranscriptPath(), "")
			if err != nil {
				t.Fatalf("readTranscriptFull: %v", err)
			}
			if got := userImagesInHistory(ResumeHistory(data.Entries)); len(got) != tc.wantHistory {
				t.Fatalf("reloaded history carries %d user image(s), want %d", len(got), tc.wantHistory)
			}
		})
	}
}

// TestImageBlindFallbackKeepsSteeringImageAdmittedAfterTurnStart covers the
// continuation fallback: the round keeps unfiltered turns and each dispatch
// target projects its own copy, so a text-only fallback must keep the steering
// image admitted after the turn started just as the primary does.
func TestImageBlindFallbackKeepsSteeringImageAdmittedAfterTurnStart(t *testing.T) {
	t.Parallel()
	r, client, primary, _ := fallbackMediaProfiles(t,
		registry.ProtocolOpenAIChat, registry.Caps{InputModalities: []string{"text"}},
		registry.ProtocolOpenAIChat, registry.Caps{InputModalities: []string{"text"}})
	if primary.AcceptsImageInput() {
		t.Fatal("test premise: the primary fixture row must not accept image input")
	}
	steeringImage := []byte("steering-png")
	primaryCalls := 0
	steeringAdmitted := false
	var sess *Session
	primaryAdapter := &agenttest.ScriptedAdapter{
		Provider: "gw",
		FaultResponder: func(llm.Request) error {
			primaryCalls++
			if primaryCalls == 2 {
				return llm.ErrorFromHTTPStatus("gw", 404, "model not found", nil, nil)
			}
			return nil
		},
		Responder: func(llm.Request) llm.Response {
			if !steeringAdmitted {
				steeringAdmitted = true
				if got := sess.consumeSteeringMessage(steeringMessage{
					Text:   "look at the new one",
					Images: []ImageAttachment{{MediaType: "image/png", Data: steeringImage}},
				}); got != steeringDelivered {
					t.Errorf("consumeSteeringMessage = %v, want steeringDelivered", got)
				}
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID: "round-one", Name: "task_list", Arguments: []byte(`{}`), Type: "function",
				})
			}
			return agenttest.FinalResponse("primary")
		},
	}
	sawSteering := false
	fallbackAdapter := &agenttest.ScriptedAdapter{
		Provider: "gwb",
		Responder: func(req llm.Request) llm.Response {
			sawSteering = true
			got := userMessageImages(req)
			if len(got) != 1 || !bytes.Equal(got[0], steeringImage) {
				t.Errorf("text-only fallback received %d user image(s), want only the steering attachment admitted after the turn started", len(got))
			}
			return agenttest.FinalResponse("ok")
		},
	}
	client.Register(primaryAdapter)
	client.Register(fallbackAdapter)
	sess = newSession(t,
		withClient(client),
		withProfile(primary),
		withStateDir(t.TempDir()),
		withConfig(SessionConfig{
			ModelFallbacks: []string{"gwb/fallback"},
			ResolveProfile: func(ref string) (*provider.Profile, error) { return provider.Resolve(r, ref) },
			testOnly:       testConfig{skipGitSnapshot: true},
		}),
	)
	sess.appendTurn(schema.TurnUserInput, userImageMessage("what was in the old image?"))

	// TRIPWIRE: scripted in-process adapters; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "keep going", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if !sawSteering {
		t.Fatal("the fallback never ran, so the assertion was vacuous")
	}
	if got := len(primaryAdapter.Requests()); got != 2 {
		t.Fatalf("primary requests = %d, want 2 so the fault lands after the steering was admitted", got)
	}
}

// TestImageBlindSkillRebuildKeepsSteeringImageAdmittedMidTurn covers the
// mid-turn skill rebuild: an appended skill notification rebuilds the request
// from the history the round already projected, so the steering image admitted
// after the turn started must survive that rebuild while the older transcript
// image stays out.
func TestImageBlindSkillRebuildKeepsSteeringImageAdmittedMidTurn(t *testing.T) {
	t.Parallel()
	profile := newMiniMaxProfile("MiniMax-M2.7")
	if profile.AcceptsImageInput() {
		t.Fatal("test premise: the MiniMax fixture row must not accept image input")
	}
	adapter := &fakeAdapter{name: "minimax"}
	sess := newSession(t,
		withProfile(profile),
		withAdapter(adapter),
		withStateDir(t.TempDir()),
	)
	sess.appendTurn(schema.TurnUserInput, userImageMessage("what was in the old image?"))
	sess.appendTurn(schema.TurnUserInput, buildUserInputMessage("start", nil))
	steeringImage := []byte("steering-png")
	if got := sess.consumeSteeringMessage(steeringMessage{
		Text:   "look at the new one",
		Images: []ImageAttachment{{MediaType: "image/png", Data: steeringImage}},
	}); got != steeringDelivered {
		t.Fatalf("consumeSteeringMessage = %v, want steeringDelivered", got)
	}
	sess.mu.Lock()
	sess.turnHistoryBaseline = len(sess.history) - 1
	// One pending delivery obligation whose skill this session cannot follow
	// appends a typed failure notification, so the round rebuilds through
	// rebuildForAppendedSkillTurns.
	sess.skillLifecycle.Obligations = append(sess.skillLifecycle.Obligations, schema.SkillDeliveryObligation{
		InvocationID: "inv-steering-image",
		Identity:     schema.SkillContentIdentity{Name: skill.GardeningMemorySkillName},
		Route:        "model_tool",
	})
	sess.skillLifecycle.Revision++
	sess.mu.Unlock()
	adapter.setSteps([]func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return agenttest.FinalResponse("ok") },
	})

	// TRIPWIRE: scripted in-process adapter; 30s only catches a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var timings events.RoundTimings
	_, _, _, req, _, _, err := sess.prepareModelRequestWithError(ctx, 1, &timings)
	if err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}
	notificationJoined := false
	for _, m := range req.Messages {
		if note, ok := findSystemNotificationPart(m); ok && strings.Contains(note, skill.GardeningMemorySkillName) {
			notificationJoined = true
			break
		}
	}
	if !notificationJoined {
		t.Fatalf("the skill delivery notification for %q never joined the request, so the rebuild path did not run", skill.GardeningMemorySkillName)
	}
	got := userMessageImages(req)
	if len(got) != 1 || !bytes.Equal(got[0], steeringImage) {
		t.Fatalf("rebuilt text-only request received %d user image(s), want the steering attachment admitted after the turn started", len(got))
	}
	sess.mu.Lock()
	history := append([]schema.Turn(nil), sess.history...)
	sess.mu.Unlock()
	if images := userImagesInHistory(history); len(images) != 2 {
		t.Fatalf("live history carries %d user image(s), want the original plus the steering one", len(images))
	}
}
