package chatcompletions

import (
	"context"
	"errors"
	"slices"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const openRouterModels = `{"data":[
 {"id":"anthropic/claude-opus-5","context_length":1000000,"supported_parameters":["tools","reasoning","temperature"],"architecture":{"input_modalities":["text","image"]},"reasoning":{"mandatory":true,"supported_efforts":["low","high","high"]},"pricing":{"prompt":"0.000005","completion":"0.000025"},"top_provider":{"max_completion_tokens":128000}},
 {"id":"plain/model","context_length":8192}
]}`

func TestListModelsMapsAdvertisedFacts(t *testing.T) {
	srv, got := server(t, 200, openRouterModels)
	res := liveRes(srv, nil)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res)
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/models" || got.header.Get("Authorization") != "Bearer k-1" {
		t.Fatalf("wire: %s %v", got.path, got.header)
	}
	if len(rows) != 2 || rows[0].ID != "anthropic/claude-opus-5" || rows[1].ID != "plain/model" {
		t.Fatalf("rows = %+v", rows)
	}
	c := rows[0].Caps
	if *c.ContextWindow != 1000000 || *c.MaxOutputTokens != 128000 || !*c.Tools || !*c.Reasoning || !*c.ThinkingAlwaysOn {
		t.Fatalf("caps = %+v", c)
	}
	if len(c.EffortValues) != 2 || c.EffortValues[1] != "high" || len(c.InputModalities) != 2 || c.Cost == nil || c.Cost.Input != 5 || c.Cost.Output != 25 {
		t.Fatalf("caps = %+v", c)
	}
	plain := rows[1].Caps
	if *plain.ContextWindow != 8192 || plain.Tools != nil || plain.Reasoning != nil || plain.Cost != nil {
		t.Fatalf("unadvertised facts must stay nil: %+v", plain)
	}
	res.Transport.ModelsEndpoint = registry.EndpointUnsupported
	if _, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), res); !errors.Is(err, llm.ErrModelListingUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

// TestListModelsRejectsNonFiniteCost covers the review finding on
// perTokenCostToPerMillion: strconv.ParseFloat accepts "NaN"/"Inf" without
// error, and neither is < 0, so a non-finite advertised price must be
// rejected explicitly or Caps.Cost ends up NaN/Inf, which then fails
// json.Marshal for the whole listing. Non-finite prompt or completion
// pricing must drop Cost to nil while every other advertised fact on that
// row still maps.
func TestListModelsRejectsNonFiniteCost(t *testing.T) {
	body := `{"data":[
 {"id":"nan-prompt","context_length":4096,"pricing":{"prompt":"NaN","completion":"0.000002"}},
 {"id":"inf-completion","context_length":8192,"pricing":{"prompt":"0.000001","completion":"Inf"}}
]}`
	srv, _ := server(t, 200, body)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]registry.Model{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	nanRow, ok := byID["nan-prompt"]
	if !ok || nanRow.Caps.Cost != nil || nanRow.Caps.ContextWindow == nil || *nanRow.Caps.ContextWindow != 4096 {
		t.Fatalf("nan-prompt row caps = %+v, want Cost=nil ContextWindow=4096", nanRow.Caps)
	}
	infRow, ok := byID["inf-completion"]
	if !ok || infRow.Caps.Cost != nil || infRow.Caps.ContextWindow == nil || *infRow.Caps.ContextWindow != 8192 {
		t.Fatalf("inf-completion row caps = %+v, want Cost=nil ContextWindow=8192", infRow.Caps)
	}
}

// TestListModelsNormalizesSupportedParameters pins the case- and
// whitespace-insensitive match openaicompat used: OpenRouter's
// supported_parameters are matched with strings.EqualFold on the trimmed
// value, so " Tools " still advertises tool support.
func TestListModelsNormalizesSupportedParameters(t *testing.T) {
	body := `{"data":[
 {"id":"padded/tools","supported_parameters":["Tools ","  Reasoning_Effort"]}
]}`
	srv, _ := server(t, 200, body)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	c := rows[0].Caps
	if c.Tools == nil || !*c.Tools || c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("caps = %+v, want Tools and Reasoning true", c)
	}
}

// listRow runs the live /models reader against a single-row body and returns
// that row's caps.
func listRow(t *testing.T, row string) registry.Caps {
	t.Helper()
	srv, _ := server(t, 200, `{"data":[`+row+`]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	return rows[0].Caps
}

// TestListModelsMapsGatewayTopLevelLimits pins the lunaroute-shaped row Evener
// depends on: the gateway advertises its limits at the top level, with no
// top_provider object and no supported_parameters. Dropping
// max_input_tokens/max_output_tokens here left Profile.MaxOutputTokens() at 0,
// so llm.ApplyTokenBudget fell back to asking for the whole remaining window.
func TestListModelsMapsGatewayTopLevelLimits(t *testing.T) {
	c := listRow(t, `{"id":"deepseek-4.1-flash","object":"model","created":0,"owned_by":"lunaroute","context_window":1048576,"context_length":1048576,"max_input_tokens":1048576,"max_output_tokens":262144,"max_completion_tokens":262144,"capabilities":{"json_schema":true,"openai_chat":true,"openai_responses":true,"reasoning":true,"tools":true,"vision":true},"client_compat":{"streaming":true}}`)
	if c.ContextWindow == nil || *c.ContextWindow != 1048576 {
		t.Fatalf("ContextWindow = %v, want 1048576", c.ContextWindow)
	}
	if c.MaxInputTokens == nil || *c.MaxInputTokens != 1048576 {
		t.Fatalf("MaxInputTokens = %v, want 1048576", c.MaxInputTokens)
	}
	if c.MaxOutputTokens == nil || *c.MaxOutputTokens != 262144 {
		t.Fatalf("MaxOutputTokens = %v, want 262144", c.MaxOutputTokens)
	}
}

// TestListModelsReadsGatewayReasoningLadder pins lunaroute's per-model effort
// ladder, which it publishes only under client_compat.pi.thinkingLevelMap. The
// model is absent from models.dev, so without this read the resolved row has an
// empty EffortValues ladder: the session picker falls back to
// low/medium/high, and the request builder omits the effort name entirely.
func TestListModelsReadsGatewayReasoningLadder(t *testing.T) {
	c := listRow(t, `{"id":"deepseek-4.1-flash","capabilities":{"reasoning":true,"tools":true},"client_compat":{"streaming":true,"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingFormat":"openai","thinkingLevelMap":{"high":"high","low":"low","max":"max","medium":"medium","minimal":null,"off":null,"xhigh":"xhigh"}}}}`)
	if got, want := c.EffortValues, []string{"low", "medium", "high", "xhigh", "max"}; !slices.Equal(got, want) {
		t.Fatalf("EffortValues = %v, want %v", got, want)
	}
	if c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("Reasoning = %v, want true from capabilities.reasoning", c.Reasoning)
	}
}

// TestListModelsReadsGatewayReasoningOffLevel covers a gateway model that can
// be told to stop reasoning: the map's off key names the wire value, which is
// the ladder's off entry (EffortOffCapable reads "none").
func TestListModelsReadsGatewayReasoningOffLevel(t *testing.T) {
	c := listRow(t, `{"id":"toggle-model","client_compat":{"pi":{"thinkingLevelMap":{"off":"none","low":"low","high":"high"}}}}`)
	if got, want := c.EffortValues, []string{"none", "low", "high"}; !slices.Equal(got, want) {
		t.Fatalf("EffortValues = %v, want %v", got, want)
	}
}

// TestListModelsGatewayReasoningFollowsTheCapability pins that a gateway
// stating reasoning = false keeps its row non-reasoning: the map is ignored,
// because capabilities.reasoning is the model's own verdict and derivation
// clears a ladder on an off row.
func TestListModelsGatewayReasoningFollowsTheCapability(t *testing.T) {
	c := listRow(t, `{"id":"no-reasoning","capabilities":{"reasoning":false},"client_compat":{"pi":{"thinkingLevelMap":{"low":"low","high":"high"}}}}`)
	if c.Reasoning == nil || *c.Reasoning {
		t.Fatalf("Reasoning = %v, want an explicit false", c.Reasoning)
	}
}

// TestListModelsGatewayWithoutPiKeepsOtherFacts pins that a gateway which
// publishes no Pi compat block still contributes its capabilities and limits;
// the absence of a ladder must stay nil, not become an empty one.
func TestListModelsGatewayWithoutPiKeepsOtherFacts(t *testing.T) {
	c := listRow(t, `{"id":"plain-gateway","context_window":8192,"capabilities":{"reasoning":true,"tools":true},"client_compat":{"streaming":true}}`)
	if c.EffortValues != nil {
		t.Fatalf("EffortValues = %#v, want nil when no map is published", c.EffortValues)
	}
	if c.Reasoning == nil || !*c.Reasoning || c.ContextWindow == nil || *c.ContextWindow != 8192 {
		t.Fatalf("caps = %+v", c)
	}
}

// TestListModelsGatewayLadderRaisesInferredFalse pins that a usable ladder is
// evidence the model tunes effort, even when supported_parameters inferred
// reasoning = false first. Without the raise, registry derivation discards the
// ladder and request shaping drops the selected effort.
func TestListModelsGatewayLadderRaisesInferredFalse(t *testing.T) {
	c := listRow(t, `{"id":"no-reasoning-param","supported_parameters":["tools"],"client_compat":{"pi":{"thinkingLevelMap":{"low":"low","high":"high"}}}}`)
	if c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("Reasoning = %v, want true from the ladder", c.Reasoning)
	}
	if got, want := c.EffortValues, []string{"low", "high"}; !slices.Equal(got, want) {
		t.Fatalf("EffortValues = %v, want %v", got, want)
	}
}

// TestListModelsGatewayPublishedEmptyMapStatesNoEffort covers a gateway map
// whose every level is null: the gateway is saying the model takes no effort
// control, so the row must not carry a ladder at all rather than inherit one
// from a curated row at resolve time.
func TestListModelsGatewayPublishedEmptyMapStatesNoEffort(t *testing.T) {
	c := listRow(t, `{"id":"no-effort","capabilities":{"reasoning":true},"client_compat":{"pi":{"thinkingLevelMap":{"low":null,"high":null}}}}`)
	if len(c.EffortValues) != 0 || c.EffortValues == nil {
		t.Fatalf("EffortValues = %#v, want a published empty ladder", c.EffortValues)
	}
}

// TestListModelsGatewayPiWithoutMapStatesNoLadder pins that the Pi block's
// other compat fields do not count as a published map: silence about effort
// must leave an inherited ladder alone rather than erase it.
func TestListModelsGatewayPiWithoutMapStatesNoLadder(t *testing.T) {
	c := listRow(t, `{"id":"max-tokens-only","client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true}}}`)
	if c.EffortValues != nil {
		t.Fatalf("EffortValues = %#v, want nil when the block states no map", c.EffortValues)
	}
}

// TestListModelsReadsGatewayDialect pins the two wire-shaping compat fields a
// gateway publishes per model. A lunaroute glm row says zai; without reading
// it the row spells the openai dialect and the thinking object never opens.
func TestListModelsReadsGatewayDialect(t *testing.T) {
	c := listRow(t, `{"id":"glm-5.3","capabilities":{"reasoning":true},"client_compat":{"pi":{"thinkingFormat":"zai","maxTokensField":"max_tokens","supportsReasoningEffort":true,"thinkingLevelMap":{"low":"low","high":"high"}}}}`)
	if got := registry.StringValue(c.ThinkingFormat); got != "zai" {
		t.Fatalf("ThinkingFormat = %q, want zai", got)
	}
	if got := registry.StringValue(c.MaxTokensField); got != "max_tokens" {
		t.Fatalf("MaxTokensField = %q, want max_tokens", got)
	}
}

// TestListModelsGatewayEffortUnsupportedIsToggleOnly pins a model the gateway
// says takes no effort parameter: the row records the verdict and publishes an
// empty ladder, so no effort control survives resolution and requests spell no
// level. Derivation, not the reader, turns the verdict into toggle-only
// controls, because only it can preserve a numeric budget from another layer.
func TestListModelsGatewayEffortUnsupportedIsToggleOnly(t *testing.T) {
	c := listRow(t, `{"id":"toggle-model","supported_parameters":["reasoning"],"client_compat":{"pi":{"supportsReasoningEffort":false,"thinkingLevelMap":{"low":"low","high":"high"}}}}`)
	if !registry.BoolValue(c.EffortParameterUnsupported) {
		t.Fatal("the row must record the no-effort verdict")
	}
	if len(c.EffortValues) != 0 {
		t.Fatalf("ladder = %#v, want a published empty list", c.EffortValues)
	}
	if c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("Reasoning = %v, want true", c.Reasoning)
	}
}

// TestApplyThinkingFormat_ToggleOnlyOpenRouterSendsNoEffort pins the OpenRouter
// dialect's vouch-gate exception against the toggle-only verdict. That branch
// passes the requested level through unconditionally because OpenRouter
// normalizes it, but a model the gateway says takes no effort parameter must
// still get the enable object instead of a level it does not accept.
func TestApplyThinkingFormat_ToggleOnlyOpenRouterSendsNoEffort(t *testing.T) {
	high, on := "high", true
	format := "openrouter"
	unsupported := true
	body := map[string]any{}
	applyThinkingFormat(body, llm.Request{ReasoningEffort: &high}, registry.Caps{
		Reasoning:                  &on,
		ReasoningControls:          []string{"toggle"},
		ThinkingFormat:             &format,
		EffortParameterUnsupported: &unsupported,
	})
	r, _ := body["reasoning"].(map[string]any)
	if r == nil {
		t.Fatalf("body = %v, want a reasoning object", body)
	}
	if _, has := r["effort"]; has {
		t.Fatalf("reasoning = %v, a toggle-only row must send no effort", r)
	}
	if r["enabled"] != true {
		t.Fatalf("reasoning = %v, want the enable object", r)
	}
}

// TestApplyThinkingFormat_CatalogToggleKeepsOpenRouterEffort pins the other
// side: a toggle-only ladder the catalog states (86 OpenRouter rows, e.g.
// anthropic/claude-sonnet-4.5) is not a gateway rejection of effort. OpenRouter
// translates the level into a budget, so suppressing it would silently discard
// the user's setting.
func TestApplyThinkingFormat_CatalogToggleKeepsOpenRouterEffort(t *testing.T) {
	high, on := "high", true
	format := "openrouter"
	body := map[string]any{}
	applyThinkingFormat(body, llm.Request{ReasoningEffort: &high}, registry.Caps{
		Reasoning:         &on,
		ReasoningControls: []string{"toggle"},
		ThinkingFormat:    &format,
	})
	r, _ := body["reasoning"].(map[string]any)
	if r == nil || r["effort"] != "high" {
		t.Fatalf("reasoning = %v, want effort high passed through", body["reasoning"])
	}
}

// TestApplyThinkingFormat_ToggleOnlyZaiKeepsThinkingObject pins the wire half
// of the vision-route rule: a toggle-only row carries a requested effort so the
// dialect opens its thinking object, and the adapter withholds the level name
// the row cannot take. Suppressing the effort at the call site instead would
// leave thinking off entirely.
func TestApplyThinkingFormat_ToggleOnlyZaiKeepsThinkingObject(t *testing.T) {
	high, on := "high", true
	format := "zai"
	body := map[string]any{}
	applyThinkingFormat(body, llm.Request{ReasoningEffort: &high}, registry.Caps{
		Reasoning:         &on,
		ReasoningControls: []string{"toggle"},
		ThinkingFormat:    &format,
	})
	thinking, _ := body["thinking"].(map[string]any)
	if thinking == nil || thinking["type"] != "enabled" {
		t.Fatalf("thinking = %v, want the enable object on a toggle-only zai row", body["thinking"])
	}
	if _, has := body["reasoning_effort"]; has {
		t.Fatalf("body carries reasoning_effort = %v, want no level on a toggle-only row", body["reasoning_effort"])
	}
}

// TestListModelsPrefersContextLengthOverContextWindow keeps OpenRouter's
// ordering: context_length wins when both spellings are present.
func TestListModelsPrefersContextLengthOverContextWindow(t *testing.T) {
	c := listRow(t, `{"id":"both-windows","context_length":1000000,"context_window":1048576}`)
	if c.ContextWindow == nil || *c.ContextWindow != 1000000 {
		t.Fatalf("ContextWindow = %v, want context_length 1000000", c.ContextWindow)
	}
}

// TestListModelsPrefersTopProviderMaxCompletionTokens keeps OpenRouter's
// precedence: the per-provider cap beats every top-level output spelling.
func TestListModelsPrefersTopProviderMaxCompletionTokens(t *testing.T) {
	c := listRow(t, `{"id":"provider-cap","top_provider":{"max_completion_tokens":128000},"max_completion_tokens":262144,"max_output_tokens":999}`)
	if c.MaxOutputTokens == nil || *c.MaxOutputTokens != 128000 {
		t.Fatalf("MaxOutputTokens = %v, want top_provider 128000", c.MaxOutputTokens)
	}
}

// TestListModelsPrefersTopLevelMaxCompletionTokens pins the top-level order:
// max_completion_tokens before max_output_tokens.
func TestListModelsPrefersTopLevelMaxCompletionTokens(t *testing.T) {
	c := listRow(t, `{"id":"two-spellings","max_completion_tokens":262144,"max_output_tokens":999}`)
	if c.MaxOutputTokens == nil || *c.MaxOutputTokens != 262144 {
		t.Fatalf("MaxOutputTokens = %v, want max_completion_tokens 262144", c.MaxOutputTokens)
	}
}

// TestListModelsLeavesLimitsNilWhenUnadvertised pins that only positive values
// become caps: absent, zero and negative spellings all leave the pointer nil.
func TestListModelsLeavesLimitsNilWhenUnadvertised(t *testing.T) {
	for _, row := range []string{
		`{"id":"absent"}`,
		`{"id":"nonpositive","context_length":0,"context_window":-1,"max_input_tokens":0,"max_completion_tokens":0,"max_output_tokens":-5,"top_provider":{"max_completion_tokens":0}}`,
	} {
		c := listRow(t, row)
		if c.ContextWindow != nil || c.MaxInputTokens != nil || c.MaxOutputTokens != nil {
			t.Fatalf("%s: limits must stay nil: %+v", row, c)
		}
	}
}

// TestListModelsReadsContextWindowAlone pins the top-level context_window as a
// window source on its own. The other tests that touch it also set
// context_length, so without this one a regression that dropped the
// context_window field, or its use in row(), would still pass every test while
// a gateway publishing only that spelling lost its window: ApplyTokenBudget
// would then skip the total-context clamp and allocate output with no ceiling.
func TestListModelsReadsContextWindowAlone(t *testing.T) {
	c := listRow(t, `{"id":"window-only","context_window":1048576}`)
	if c.ContextWindow == nil || *c.ContextWindow != 1048576 {
		t.Fatalf("ContextWindow = %v, want 1048576", c.ContextWindow)
	}
}

// TestListModelsReadsGatewayVisionCapability pins that a gateway advertising
// vision under the capabilities object (lunaroute's shape) yields image input,
// so the model is not resolved as image-blind. An explicit vision:false, or no
// capabilities object at all, must not advertise image input.
func TestListModelsReadsGatewayVisionCapability(t *testing.T) {
	if c := listRow(t, `{"id":"vision-model","capabilities":{"vision":true,"tools":true}}`); !c.AcceptsModality("image") {
		t.Fatalf("capabilities.vision:true must advertise image input: %+v", c.InputModalities)
	}
	if c := listRow(t, `{"id":"blind-model","capabilities":{"vision":false,"tools":true}}`); c.AcceptsModality("image") {
		t.Fatalf("capabilities.vision:false must not advertise image input: %+v", c.InputModalities)
	}
	if c := listRow(t, `{"id":"quiet-model","context_length":8192}`); c.AcceptsModality("image") {
		t.Fatalf("a row with no capabilities must not advertise image input: %+v", c.InputModalities)
	}
	if c := listRow(t, `{"id":"both-model","architecture":{"input_modalities":["text"]},"capabilities":{"vision":true}}`); c.AcceptsModality("image") || len(c.InputModalities) != 1 {
		t.Fatalf("architecture.input_modalities must win over capabilities.vision: %+v", c.InputModalities)
	}
}
