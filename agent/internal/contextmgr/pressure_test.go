package contextmgr

import (
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// testEstimateTokens runs the manager's history estimate with the package's
// standard test profile, so these arithmetic tests exercise the same resolved
// rule the compaction paths use.
func testEstimateTokens(t *testing.T, turns []schema.Turn) int {
	t.Helper()
	return NewManager(testProfile("openai", "gpt-5.2", 1_000_000), nil, cheapmodel.New(nil)).estimateTokens(turns)
}

// testEstimateUsedTokens runs the manager's used-token accounting with the same
// standard profile.
func testEstimateUsedTokens(t *testing.T, lastTokens, measuredLen int, history []schema.Turn, sysPromptChars int) int {
	t.Helper()
	return NewManager(testProfile("openai", "gpt-5.2", 1_000_000), nil, cheapmodel.New(nil)).estimateUsedTokens(lastTokens, measuredLen, history, sysPromptChars)
}

// TestEstimateTokens_DropsPlainChatThinkingLikeRequestAssembly pins the split
// between the two estimates: the compaction estimate applies the request
// assembly's replay policy (#4563), so plain chat reasoning does not size a
// window it never crosses, while text a signature-bearing reasoning item needs
// is still billed because the builder serializes it.
func TestEstimateTokens_DropsPlainChatThinkingLikeRequestAssembly(t *testing.T) {
	t.Parallel()
	reasoning := true
	chat := provider.FromResolved(registry.Resolved{
		Instance: "gw", ModelID: "m", Protocol: registry.ProtocolOpenAIChat,
		Caps: registry.Caps{Reasoning: &reasoning},
	}, nil)
	cm := NewManager(chat, nil, cheapmodel.New(nil))
	const text = 4000
	assistant := func(thinking llm.ContentPart) []schema.Turn {
		return []schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			thinking, {Kind: llm.ContentText, Text: "answer"},
		}})}
	}
	plain := assistant(llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: strings.Repeat("t", text)}})
	without := []schema.Turn{schema.NewTurn(schema.TurnAssistant, llm.Assistant("answer"))}
	if with, bare := cm.estimateTokens(plain), cm.estimateTokens(without); with != bare {
		t.Fatalf("chat compaction estimate = %d, want %d: request assembly strips the plain thinking", with, bare)
	}
	signed := assistant(llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{
		EncryptedContent: `[{"type":"reasoning.text","text":"","signature":"sig"}]`,
		Text:             strings.Repeat("t", text),
	}})
	if unsigned, withSigned := cm.estimateTokens(plain), cm.estimateTokens(signed); withSigned <= unsigned {
		t.Fatalf("signed reasoning estimate = %d, want more than %d: the builder serializes the text", withSigned, unsigned)
	}
}

func TestEstimateTokens_EmptyHistory(t *testing.T) {
	got := testEstimateTokens(t, nil)
	if got != 0 {
		t.Fatalf("EstimateTokens(nil) = %d, want 0", got)
	}
	got = testEstimateTokens(t, []schema.Turn{})
	if got != 0 {
		t.Fatalf("EstimateTokens([]) = %d, want 0", got)
	}
}

func TestEstimateTokens_SingleUserTurn(t *testing.T) {
	text := "Hello, world! This is a test message."
	turns := []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.User(text)}}
	got := testEstimateTokens(t, turns)
	want := len(text) / 4
	if got != want {
		t.Fatalf("EstimateTokens = %d, want %d (len=%d)", got, want, len(text))
	}
}

func TestEstimateTokens_WithToolResults(t *testing.T) {
	content := "file contents here with lots of text"
	turns := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("read a file")},
		{Kind: schema.TurnTool, Message: llm.ToolResultNamed("c1", "read_file", content, false)},
	}
	got := testEstimateTokens(t, turns)
	// messageCharCount counts: message.ToolCallID + part.ToolCallID + part.Name + content
	// Message-level ToolCallID = "c1" (2), part ToolCallID = "c1" (2), part Name = "read_file" (9), content (36)
	// User text = "read a file" (11)
	// Total = 11 + 2 + 2 + 9 + 36 = 60, /4 = 15
	want := 15
	if got != want {
		t.Fatalf("EstimateTokens = %d, want %d", got, want)
	}
}

func TestEstimateTokens_WithThinking(t *testing.T) {
	turns := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "let me think about this carefully"}},
				{Kind: llm.ContentText, Text: "answer"},
			},
		}},
	}
	got := testEstimateTokens(t, turns)
	// A thinking part with no replay metadata (no signature, no encrypted
	// content) is display-only — the adapter never re-sends it — so it must not
	// be billed to the context estimate (issue #653). Only the answer counts.
	want := len("answer") / 4
	if got != want {
		t.Fatalf("EstimateTokens = %d, want %d", got, want)
	}

	// Thinking the adapter will replay is billed. The signature-bearing block is
	// a shape only the Anthropic adapter emits (anthropic/request.go: the text
	// plus the signature), so it is billed against an Anthropic target; the
	// OpenAI Responses target this profile resolves to emits no signature-only
	// part at all (responses/input.go), so only the answer counts there.
	replayable := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "let me think about this carefully", Signature: "crypto-sig"}},
				{Kind: llm.ContentText, Text: "answer"},
			},
		}},
	}
	totalChars := len("let me think about this carefully") + len("crypto-sig") + len("answer")
	anthropic := NewManager(provider.FromResolved(registry.Resolved{Instance: "anthropic", Protocol: registry.ProtocolAnthropic}, nil), nil, cheapmodel.New(nil))
	if got := anthropic.estimateTokens(replayable); got != totalChars/4 {
		t.Fatalf("anthropic EstimateTokens = %d, want %d: the adapter emits text and signature", got, totalChars/4)
	}
	if got := testEstimateTokens(t, replayable); got != len("answer")/4 {
		t.Fatalf("responses EstimateTokens = %d, want %d: a signature-only part never rides", got, len("answer")/4)
	}
	row := registry.Resolved{Instance: "gateway", ModelID: "alias", WireID: "model", Protocol: registry.ProtocolOpenAIResponses,
		Transport: registry.Transport{BaseURL: "https://gateway.example.test", Endpoint: "/complete", StreamEndpoint: "/stream/a"},
	}
	const issuerStream = "/stream/a"
	t.Run("Anthropic fallback only prices its own native thinking", func(t *testing.T) {
		target := registry.Resolved{Instance: "claude", ModelID: "fallback", Protocol: registry.ProtocolAnthropic}
		cm := NewManager(provider.FromResolved(target, nil), nil, cheapmodel.New(nil))
		for _, thought := range []struct {
			protocol string
			data     llm.ThinkingData
		}{
			{registry.ProtocolOpenAIChat, llm.ThinkingData{Text: "chat-marker-17", Signature: "reasoning"}},
			{registry.ProtocolOpenAIResponses, llm.ThinkingData{Text: "responses-marker-23", ID: "rs_23"}},
			{"", llm.ThinkingData{Text: "older-chat-marker-33", Signature: "reasoning_content"}},
			{"", llm.ThinkingData{Text: "older-responses-marker-41", ID: "rs_41"}},
		} {
			turn := schema.Turn{Kind: schema.TurnAssistant, ResponseProtocol: thought.protocol, Message: llm.Assistant("answer")}
			want := llm.TokenPartEstimatorFor(target).Parts(turn.Message).Total()
			turn.Message.Content = append(turn.Message.Content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &thought.data})
			if got := cm.estimateTokens([]schema.Turn{turn}); got != want {
				t.Errorf("Anthropic priced %q at %d tokens, want only the answer's %d", thought.data.Text, got, want)
			}
		}
	})
	t.Run("assistant text on another Chat endpoint", func(t *testing.T) {
		source := row
		source.Protocol = registry.ProtocolOpenAIChat
		const scalar, detail, mirrored = " independent-scalar-41 ", " unsigned-display-27 ", " signed-and-scalar-71 "
		const signed = `[{"type":"reasoning.text","text":"signed-only-47","signature":"sig-47"}]`
		const opaque = `[{"type":"reasoning.encrypted","data":"cipher-39"}]`
		const opaqueWithText = `[{"type":"reasoning.encrypted","id":"r-71","data":"cipher-39","thinking":"private-84"}]`
		turn := schema.Turn{Kind: schema.TurnAssistant, ResponseProtocol: source.Protocol, ResponseProvider: source.Instance,
			ResponseReasoningRoute: source.ReasoningReplayRoute(), Message: llm.Assistant("answer")}
		for _, thinking := range []*llm.ThinkingData{
			{Text: scalar, Signature: "reasoning"},
			{Text: detail, EncryptedContent: opaque, ChatScalar: &llm.ChatReasoningScalar{Field: "reasoning", Text: "alternate-31"}},
			{Text: "signed-only-47", EncryptedContent: signed},
			{Text: "signed-only-47", EncryptedContent: signed, ChatScalar: &llm.ChatReasoningScalar{Field: "reasoning", Text: mirrored}},
			{Text: "private-84", EncryptedContent: opaqueWithText},
			{Text: "private-84", EncryptedContent: opaqueWithText, ChatScalar: &llm.ChatReasoningScalar{Field: "reasoning", Text: scalar}},
		} {
			turn.Message.Content = append(turn.Message.Content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: thinking})
		}
		target := source
		target.Transport.StreamEndpoint = "/stream/b"
		target.ModelID = "replacement"
		target.Caps.ThinkingAsText = new(true)
		for _, raw := range []bool{true, false} {
			target.Caps.ReplayReasoningText = new(raw)
			usage := estimateTokenPartsFor(provider.FromResolved(target, nil), []schema.Turn{turn})
			if want := len("answer") + 2*len(scalar) + len(detail) + len(mirrored); usage.Chars != want {
				t.Errorf("raw replay %t, context pressure counted %d chars, want %d", raw, usage.Chars, want)
			}
			otherProtocol := turn
			otherProtocol.ResponseProtocol = registry.ProtocolAnthropic
			if got := estimateTokenPartsFor(provider.FromResolved(target, nil), []schema.Turn{otherProtocol}); got.Chars != len("answer") {
				t.Errorf("raw replay %t, context pressure included foreign-protocol thinking: %+v", raw, got)
			}
		}
	})
	for _, tc := range []struct{ protocol, ciphertext string }{
		{registry.ProtocolOpenAIResponses, "sealed-48"},
		{registry.ProtocolOpenAIChat, `[{"type":"reasoning.text","text":"signed-48","signature":"sig-48"},{"type":"reasoning.encrypted","data":"cipher-48"}]`},
	} {
		row.Protocol, row.Transport.StreamEndpoint = tc.protocol, issuerStream
		sealed := []schema.Turn{{Kind: schema.TurnAssistant, ResponseProvider: row.Instance, ResponseRequestModel: row.ModelID,
			ResponseProtocol: row.Protocol, ResponseReasoningRoute: row.ReasoningReplayRoute(), Message: llm.Assistant("answer"),
		}}
		sealed[0].Message.Content = append(sealed[0].Message.Content, llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{EncryptedContent: tc.ciphertext}})
		for _, replay := range []bool{true, false} {
			row.Caps.ReplayReasoningText = new(replay)
			for _, stream := range []string{issuerStream, "/stream/b"} {
				row.Transport.StreamEndpoint = stream
				cm := NewManager(provider.FromResolved(row, nil), nil, cheapmodel.New(nil))
				message := llm.Assistant("answer")
				if stream == issuerStream {
					message = sealed[0].Message
				}
				want := llm.TokenPartEstimatorFor(row).Parts(message).Total()
				if got := cm.estimateTokens(sealed); got != want {
					t.Errorf("%s stream %s, raw replay %t: got %d tokens, want %d", tc.protocol, stream, replay, got, want)
				}
			}
		}
	}
}

// History accounting follows the resolved row rather than a provider name: an
// Anthropic row's adapter replays an unsigned thinking block, so its text must
// be billed; a Responses row keeps that text for display only, so it must not.
// Billing neither (the state before this) under-reported pressure and let
// compaction defer past the window an oversized request would cross.
func TestEstimateTokens_BillsUnsignedThinkingByResolvedProtocol(t *testing.T) {
	const text = 400 // chars → 100 tokens
	history := []schema.Turn{{Kind: schema.TurnAssistant, Message: llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: strings.Repeat("t", text)}},
		},
	}}}

	anthropic := NewManager(provider.FromResolved(registry.Resolved{Instance: "anthropic", Protocol: registry.ProtocolAnthropic}, nil), nil, cheapmodel.New(nil))
	if got, want := anthropic.estimateTokens(history), text/4; got != want {
		t.Fatalf("anthropic history = %d, want %d: the adapter replays the unsigned thinking block", got, want)
	}
	responses := NewManager(provider.FromResolved(registry.Resolved{Instance: "openai", Protocol: registry.ProtocolOpenAIResponses}, nil), nil, cheapmodel.New(nil))
	if got := responses.estimateTokens(history); got != 0 {
		t.Fatalf("responses history = %d, want 0: the adapter keeps raw reasoning text for display only", got)
	}
	for _, tc := range []struct {
		protocol string
		want     int
	}{
		{registry.ProtocolOpenAIChat, 2 * text / 4},
		{registry.ProtocolOpenAIResponses, 2 * (text + len("rs_1")) / 4},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			row := registry.Resolved{
				Instance: "gateway", ModelID: "model", WireID: "served-current", Protocol: tc.protocol,
				Caps: registry.Caps{ReplayReasoningText: new(true)},
			}
			cm := NewManager(provider.FromResolved(row, nil), nil, cheapmodel.New(nil))
			turn := history[0]
			turn.ResponseProvider, turn.ResponseRequestModel, turn.ResponseProtocol = row.Instance, row.ModelID, tc.protocol
			turn.Message.Content[0].Thinking.ID = "rs_1"
			foreign := turn
			foreign.ResponseProvider = "another-gateway"
			redirected := turn
			redirected.ResponseWireModel = "served-previous"
			current := turn
			current.ResponseWireModel = row.WireID
			if got := cm.estimateTokens([]schema.Turn{turn, foreign, redirected, current}); got != tc.want {
				t.Fatalf("history estimate = %d, want %d for the unstamped and current-wire turns", got, tc.want)
			}
		})
	}
}

func TestEstimateTokens_ImageDataDoesNotScaleWithByteLength(t *testing.T) {
	turnsWithImage := func(size int) []schema.Turn {
		return []schema.Turn{{Kind: schema.TurnUserInput, Message: llm.Message{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "describe this image"},
				{Kind: llm.ContentImage, Image: &llm.ImageData{
					Data:      make([]byte, size),
					MediaType: "image/png",
				}},
			},
		}}}
	}

	small := testEstimateTokens(t, turnsWithImage(1))
	large := testEstimateTokens(t, turnsWithImage(1_500_000))

	if large != small {
		t.Fatalf("EstimateTokens scaled with raw image bytes: small=%d large=%d", small, large)
	}
	if large > 1_000 {
		t.Fatalf("EstimateTokens counted raw image payload: got %d", large)
	}
}

// A measurement belongs to the profile that produced it: a SetProfile that lands
// while a request is in flight invalidates the measurement that request belonged
// to, so recording it afterwards must not attribute one model's count to another.
func TestRecordInputTokensForRejectsAStaleAnsweringProfile(t *testing.T) {
	live := provider.FromResolved(registry.Resolved{Instance: "openai", ModelID: "gpt-5.2", Protocol: registry.ProtocolOpenAIResponses}, nil)
	previous := provider.FromResolved(registry.Resolved{Instance: "anthropic", ModelID: "claude-opus-5", Protocol: registry.ProtocolAnthropic}, nil)
	cm := NewManager(live, nil, cheapmodel.New(nil))

	if !cm.RecordInputTokensFor(live, 321, 7) {
		t.Fatal("the live profile's own measurement was refused")
	}
	if got := cm.LastInputTokens(); got != 321 {
		t.Fatalf("LastInputTokens = %d, want 321", got)
	}
	if cm.RecordInputTokensFor(previous, 999, 9) {
		t.Fatal("a measurement from a profile the manager no longer holds was accepted")
	}
	if got := cm.LastInputTokens(); got != 321 {
		t.Fatalf("LastInputTokens = %d after the stale write, want the live measurement 321", got)
	}
}

// The estimate readers must stay total without a profile: a Manager built with
// none (NewManager(nil, ...), as the session-log strategy tests do) has no window
// and no resolved row to read, which is "unknown", not a crash. These paths were
// pure and total before the profile-aware accounting.
func TestContextManager_NilProfileEstimatesStayTotal(t *testing.T) {
	cm := NewManager(nil, nil, cheapmodel.New(nil))
	const text = "hello from a manager with no profile"
	history := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User(text))}

	if usage := cm.EstimateUsage(history, 0); usage != (schema.ContextMetrics{}) {
		t.Fatalf("EstimateUsage with no profile = %+v, want the zero metrics: no window is known", usage)
	}
	if got := cm.EstimatePressure(history, 0); got != 0 {
		t.Fatalf("EstimatePressure with no profile = %v, want 0: no window is known", got)
	}
	messages := []llm.Message{llm.User(text)}
	if got, want := cm.estimateTokens(history), llm.EstimateMessagesInputTokens(messages).Tokens; got != want {
		t.Fatalf("estimateTokens with no profile = %d, want the targetless estimate %d", got, want)
	}
}

func TestContextManager_SetProfileInvalidatesMeasurementsOnlyWhenTargetChanges(t *testing.T) {
	profile := testProfile("openai", "gpt-5.2", 0)
	resolved := profile.Resolved()

	t.Run("same target", func(t *testing.T) {
		cm := NewManager(profile, nil, cheapmodel.New(nil))
		cm.RecordInputTokens(321, 7)
		cm.SetProfile(profile.WithResolved(resolved))
		if got := cm.LastInputTokens(); got != 321 {
			t.Fatalf("LastInputTokens = %d, want retained measurement 321", got)
		}
		if got := cm.historyLenAtMeasure; got != 7 {
			t.Fatalf("historyLenAtMeasure = %d, want retained measurement length 7", got)
		}
	})

	t.Run("replay issuer wire model", func(t *testing.T) {
		row := resolved
		row.Caps.ReplayReasoningText = new(true)
		row.WireID = "served-a"
		cm := NewManager(profile.WithResolved(row), nil, cheapmodel.New(nil))
		cm.RecordInputTokens(321, 7)
		row.WireID = "served-b"
		cm.SetProfile(profile.WithResolved(row))
		if got := cm.LastInputTokens(); got != 0 {
			t.Fatalf("changing the wire model retained a count from the prior issuer: %d", got)
		}
	})

	// Reasoning is the estimator's predicate, not the raw capability: a row that
	// never stated it bills exactly as one that states it permits, so the
	// measurement stands for that refresh -- and is cleared when the row disables
	// reasoning, because then the adapter drops the thinking text the count
	// included.
	t.Run("reasoning metadata refresh", func(t *testing.T) {
		silent := registry.Resolved{Instance: "openai", ModelID: "test", Protocol: registry.ProtocolOpenAIChat}
		permits := silent
		permits.Caps.Reasoning = new(true)
		disabled := silent
		disabled.Caps.Reasoning = new(false)

		cm := NewManager(provider.FromResolved(silent, nil), nil, cheapmodel.New(nil))
		cm.RecordInputTokens(321, 7)
		cm.SetProfile(provider.FromResolved(permits, nil))
		if got := cm.LastInputTokens(); got != 321 {
			t.Fatalf("silent -> explicit true: LastInputTokens = %d, want retained measurement 321", got)
		}
		cm.SetProfile(provider.FromResolved(disabled, nil))
		if got := cm.LastInputTokens(); got != 0 {
			t.Fatalf("silent -> explicit false: LastInputTokens = %d, want the measurement cleared", got)
		}
	})

	t.Run("Chat scalar replay field", func(t *testing.T) {
		row := func(field *string, replay bool) *provider.Profile {
			return provider.FromResolved(registry.Resolved{
				Instance: "gateway", ModelID: "reasoner", Protocol: registry.ProtocolOpenAIChat,
				Caps: registry.Caps{ReplayReasoningText: &replay, ReasoningField: field},
			}, nil)
		}
		for _, tc := range []struct {
			name          string
			before, after *string
			replay        bool
			want          int
		}{
			{"follow observed field", nil, nil, true, 321},
			{"empty setting follows observed field", nil, new(""), true, 321},
			{"same explicit field", new("reasoning"), new("reasoning"), true, 321},
			{"observed to explicit", nil, new("reasoning_content"), true, 0},
			{"literal observed field from a resolved caller", nil, new("observed"), true, 0},
			{"explicit to observed", new("reasoning_content"), nil, true, 0},
			{"replace explicit field", new("reasoning"), new("reasoning_content"), true, 0},
			{"alternate spelling", new("reasoning_content"), new("reasoning_text"), true, 0},
			{"scalar replay off", nil, new("reasoning_content"), false, 321},
		} {
			t.Run(tc.name, func(t *testing.T) {
				cm := NewManager(row(tc.before, tc.replay), nil, cheapmodel.New(nil))
				cm.RecordInputTokens(321, 7)
				tokens, prefix, key := cm.InputMeasurement()
				next := row(tc.after, tc.replay)
				cm.SetProfile(next)
				cold := NewManager(next, nil, cheapmodel.New(nil))
				cold.RestoreInputMeasurement(tokens, prefix, key)
				if live, saved := cm.LastInputTokens(), cold.LastInputTokens(); live != tc.want || saved != tc.want {
					t.Errorf("live and restored counts = %d, %d; want %d", live, saved, tc.want)
				}
			})
		}
		// These saved keys identify whether this row replayed a scalar, but
		// carry no record of which field the gateway received.
		const savedScalar = "66bb07f910e805adb1f43afbe8156162cb19f2f7f9f27b768b38e0f0be95b9af"
		const savedWithoutScalar = "81ecf5fc94caacf8f269a5290a2073f9177851064e2d068af90914ce28dcb15c"
		for _, tc := range []struct {
			profile *provider.Profile
			key     string
			want    int
		}{
			{row(nil, true), savedScalar, 0},
			{row(new("reasoning_content"), true), savedScalar, 0},
			{row(new("reasoning_content"), false), savedWithoutScalar, 321},
		} {
			cold := NewManager(tc.profile, nil, cheapmodel.New(nil))
			cold.RestoreInputMeasurement(321, 7, tc.key)
			if got := cold.LastInputTokens(); got != tc.want {
				t.Errorf("restored count without a recorded field selection = %d, want %d", got, tc.want)
			}
		}
	})

	for _, tc := range []struct {
		name   string
		mutate func(*registry.Resolved)
	}{
		{name: "instance", mutate: func(res *registry.Resolved) { res.Instance = "other" }},
		{name: "model", mutate: func(res *registry.Resolved) { res.ModelID = "other-model" }},
		{name: "protocol", mutate: func(res *registry.Resolved) { res.Protocol = registry.ProtocolAnthropic }},
		{name: "surface", mutate: func(res *registry.Resolved) { res.Surface = registry.SurfaceAnthropic }},
		{name: "model family", mutate: func(res *registry.Resolved) { res.Model.Family = "claude" }},
		{name: "thinking as text", mutate: func(res *registry.Resolved) { res.Caps.ThinkingAsText = new(true) }},
		{name: "plain reasoning replay", mutate: func(res *registry.Resolved) { res.Caps.ReplayReasoningText = new(true) }},
		{name: "reasoning", mutate: func(res *registry.Resolved) { res.Caps.Reasoning = new(false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cm := NewManager(profile, nil, cheapmodel.New(nil))
			cm.RecordInputTokens(321, 7)
			next := resolved
			tc.mutate(&next)
			cm.SetProfile(provider.FromResolved(next, nil))
			if got := cm.LastInputTokens(); got != 0 {
				t.Fatalf("LastInputTokens = %d after target change, want 0", got)
			}
			if got := cm.historyLenAtMeasure; got != 0 {
				t.Fatalf("historyLenAtMeasure = %d after target change, want 0", got)
			}
		})
	}
}

// Token-based pressure: Manager should use actual InputTokens from API
// responses for pressure calculation instead of relying solely on char/4.
func TestContextManager_UsesLastInputTokensForPressure(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil, cheapmodel.New(nil))

	// Record that the last API response reported 550 input tokens for a 10-turn history.
	cm.RecordInputTokens(550, 10)

	// Add 2 new turns since the measurement (~20 chars = ~5 tokens).
	history := make([]schema.Turn, 12)
	for i := range 10 {
		history[i] = schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("x")}
	}
	history[10] = schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("y", 20))}
	history[11] = schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("z", 20))}

	// Pressure should be based on: 550 (known) + ~10 (new turns) = ~560 tokens
	// Not char/4 of entire history which would be much less.
	pressure := cm.estimatePressure(history, 0)

	// With 1000 token window, pressure should be ~0.56 (not the ~0.03 from char/4).
	if pressure < 0.5 {
		t.Fatalf("pressure should use lastInputTokens, got %.2f (expected ~0.56)", pressure)
	}
}

func TestContextManager_EstimateUsageReportsRemainingWindow(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil, cheapmodel.New(nil))
	cm.RecordInputTokens(550, 10)

	history := make([]schema.Turn, 11)
	history[10] = schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("y", 40))}

	got := cm.EstimateUsage(history, 0)
	if got.Used != 560 || got.Window != 1000 || got.Remaining != 440 {
		t.Fatalf("EstimateUsage = %+v, want used=560 window=1000 remaining=440", got)
	}
}

// A usage or pressure reading pairs one profile's window with the same
// profile's token accounting. SetProfile runs on a model switch; a swap landing
// between the window read and the token estimate would otherwise blend one
// model's window with another model's rules. The invariant under a concurrent
// swap is that every result equals one of the two single-profile outcomes and
// never a blend of both.
func TestContextManager_UsageEstimatePairsOneProfilesWindow(t *testing.T) {
	openai := testProfile("openai", "gpt-5.2", 1_000_000)
	anthropic := testProfile("anthropic", "claude-opus-4-6", 200_000)

	// A long history widens the window between the estimator's profile reads,
	// and replayable thinking text makes the two profiles' accounting differ.
	history := thinkingHeavyHistory()

	cm := NewManager(openai, nil, cheapmodel.New(nil))
	wantOpenAI := NewManager(openai, nil, cheapmodel.New(nil)).EstimateUsage(history, 0)
	wantAnthropic := NewManager(anthropic, nil, cheapmodel.New(nil)).EstimateUsage(history, 0)
	if wantOpenAI == wantAnthropic {
		t.Fatalf("degenerate fixture: both profiles estimate %+v", wantOpenAI)
	}
	wantOpenAIPressure := float64(wantOpenAI.Used) / float64(wantOpenAI.Window)
	wantAnthropicPressure := float64(wantAnthropic.Used) / float64(wantAnthropic.Window)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				cm.SetProfile(anthropic)
			} else {
				cm.SetProfile(openai)
			}
		}
	})

	for range 200 {
		got := cm.EstimateUsage(history, 0)
		if got != wantOpenAI && got != wantAnthropic {
			close(stop)
			wg.Wait()
			t.Fatalf("EstimateUsage = %+v, want %+v or %+v: the window and the estimate must come from one profile snapshot", got, wantOpenAI, wantAnthropic)
		}
		gotPressure := cm.Pressure(history, 0)
		if gotPressure != wantOpenAIPressure && gotPressure != wantAnthropicPressure {
			close(stop)
			wg.Wait()
			t.Fatalf("Pressure = %v, want %v or %v: the window and the estimate must come from one profile snapshot", gotPressure, wantOpenAIPressure, wantAnthropicPressure)
		}
	}
	close(stop)
	wg.Wait()
}

func TestContextManager_FallsBackToCharHeuristicWithoutMeasurement(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil, cheapmodel.New(nil))

	// No RecordInputTokens called — should fall back to char/4.
	history := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("x", 400))},
	}

	pressure := cm.estimatePressure(history, 0)

	// 400 chars / 4 = 100 tokens, 100/1000 = 0.10
	if pressure < 0.09 || pressure > 0.11 {
		t.Fatalf("expected pressure ~0.10 from char/4 fallback, got %.2f", pressure)
	}
}

func TestPressure_ReturnsEstimate(t *testing.T) {
	profile := testProfile("openai", "test", 1000)
	cm := NewManager(profile, nil, cheapmodel.New(nil))

	history := []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("x", 400))},
	}

	p := cm.Pressure(history, 0)
	// 400 chars / 4 = 100 tokens, 100/1000 = 0.10
	if p < 0.09 || p > 0.11 {
		t.Fatalf("Pressure() = %.2f, want ~0.10", p)
	}
}

func TestPressure_ZeroContextWindow(t *testing.T) {
	profile := &provider.Profile{} // zero value reports ContextWindowSize() == 0: unknown
	cm := NewManager(profile, nil, cheapmodel.New(nil))

	p := cm.Pressure(nil, 0)
	if p != 0 {
		t.Fatalf("Pressure() = %.2f, want 0 with nothing in history", p)
	}
	// Unknown no longer means "no budget" (issue #4564): with history present,
	// the same zero-window profile pressure-reads against the assumed default,
	// so the fraction is low but no longer pinned to 0.
	history := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("token ", 4_000)))}
	if m := cm.EstimateUsage(history, 0); m.Window != AssumedContextWindow {
		t.Fatalf("EstimateUsage window = %d, want the assumed %d", m.Window, AssumedContextWindow)
	}
	p = cm.Pressure(history, 0)
	if p <= 0 || p >= 1 {
		t.Fatalf("Pressure() = %v, want a fraction of the assumed window", p)
	}
}

func TestContextManager_SetProfile_UpdatesContextWindow(t *testing.T) {
	// Start with a 200K profile, then update the same target to 1M. Pressure
	// should retain that target's measurement and reflect the new window.
	smallProfile := testProfile("anthropic", "claude-opus-4-6", 200_000)
	cm := NewManager(smallProfile, nil, cheapmodel.New(nil))

	// Record 100K tokens.
	cm.RecordInputTokens(100_000, 5)
	history := make([]schema.Turn, 5)
	for i := range history {
		history[i] = schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant("x")}
	}

	// With 200K window: pressure ≈ 100K/200K = 0.50
	p1 := cm.estimatePressure(history, 0)
	if p1 < 0.45 || p1 > 0.55 {
		t.Fatalf("pressure before SetProfile = %.2f, expected ~0.50", p1)
	}

	// Apply a 1M window override without changing the target identity.
	bigProfile := withContextWindow(smallProfile, 1_000_000)
	cm.SetProfile(bigProfile)

	// With 1M window: pressure ≈ 100K/1M = 0.10
	p2 := cm.estimatePressure(history, 0)
	if p2 < 0.08 || p2 > 0.12 {
		t.Fatalf("pressure after SetProfile = %.2f, expected ~0.10", p2)
	}
}

// The API measurement belongs to the profile that produced it, and SetProfile
// swaps the profile and clears the measurement in one critical section. A
// pressure or usage reading must take that pair in one section too: reading the
// profile and the measurement separately can pair one model's window with
// another model's cleared measurement. The invariant under a concurrent swap is
// that every result is one of the three states a single critical section can
// produce — the starting profile with its measurement, or either profile with
// the measurement already cleared. The torn pair itself is not reproducible in
// a bounded run (the reads sit nanoseconds apart and Go's mutex barging keeps
// them together), so this pins the invariant the single-section snapshot
// guarantees rather than reproducing the blend.
func TestContextManager_UsageEstimateKeepsProfileAndMeasurementTogether(t *testing.T) {
	openai := testProfile("openai", "gpt-5.2", 1_000_000)
	anthropic := testProfile("anthropic", "claude-opus-4-6", 200_000)

	history := thinkingHeavyHistory()

	measured := func(prof *provider.Profile) schema.ContextMetrics {
		cm := NewManager(prof, nil, cheapmodel.New(nil))
		cm.RecordInputTokens(500, 3)
		return cm.EstimateUsage(history, 0)
	}
	cleared := func(prof *provider.Profile) schema.ContextMetrics {
		return NewManager(prof, nil, cheapmodel.New(nil)).EstimateUsage(history, 0)
	}
	wantMeasured := measured(openai)
	wantClearedOpenAI := cleared(openai)
	wantClearedAnthropic := cleared(anthropic)
	if wantMeasured == wantClearedOpenAI || wantMeasured == wantClearedAnthropic || wantClearedOpenAI == wantClearedAnthropic {
		t.Fatalf("degenerate fixture: outcomes collide: %+v %+v %+v", wantMeasured, wantClearedOpenAI, wantClearedAnthropic)
	}
	allowed := map[schema.ContextMetrics]bool{
		wantMeasured:         true,
		wantClearedOpenAI:    true,
		wantClearedAnthropic: true,
	}

	cm := NewManager(openai, nil, cheapmodel.New(nil))
	cm.RecordInputTokens(500, 3)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if i%2 == 0 {
				cm.SetProfile(anthropic)
			} else {
				cm.SetProfile(openai)
			}
		}
	})

	for range 200 {
		got := cm.EstimateUsage(history, 0)
		if !allowed[got] {
			close(stop)
			wg.Wait()
			t.Fatalf("EstimateUsage = %+v, want %+v, %+v or %+v: the profile and its measurement must come from one snapshot", got, wantMeasured, wantClearedOpenAI, wantClearedAnthropic)
		}
	}
	close(stop)
	wg.Wait()
}

// thinkingHeavyHistory is the shared input for the profile-read concurrency
// tests: a long history whose turns are replayable thinking text, so the two
// profiles' accounting differs across any window between their reads.
func thinkingHeavyHistory() []schema.Turn {
	history := make([]schema.Turn, 0, 3000)
	for range 3000 {
		history = append(history, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reasoning the anthropic adapter replays and the responses adapter does not"}},
			},
		}})
	}
	return history
}
