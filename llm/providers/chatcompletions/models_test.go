package chatcompletions

import (
	"context"
	"encoding/json"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	c := listRow(t, `{"id":"no-reasoning","capabilities":{"reasoning":false},"client_compat":{"pi":{"thinkingLevelMap":{"low":"low","high":"high"}}}}`)
	if c.Reasoning == nil || *c.Reasoning {
		t.Fatalf("Reasoning = %v, want an explicit false", c.Reasoning)
	}
}

// TestListModelsGatewayToolsFollowsTheCapability pins the per-model tool
// verdict's precedence over the provider-wide parameter list. A row that lists
// tools in supported_parameters but states capabilities.tools false must
// resolve tool-less: the gateway's own verdict is the narrower fact.
func TestListModelsGatewayToolsFollowsTheCapability(t *testing.T) {
	t.Parallel()
	if c := listRow(t, `{"id":"tools-gateway","supported_parameters":["tools"],"capabilities":{"tools":false}}`); c.Tools == nil || *c.Tools {
		t.Fatalf("Tools = %v, want an explicit false from capabilities.tools", c.Tools)
	}
	if c := listRow(t, `{"id":"tools-gateway","capabilities":{"tools":true}}`); c.Tools == nil || !*c.Tools {
		t.Fatalf("Tools = %v, want true from capabilities.tools", c.Tools)
	}
}

// TestListModelsGatewayWithoutPiKeepsOtherFacts pins that a gateway which
// publishes no Pi compat block still contributes its capabilities and limits;
// the absence of a ladder must stay nil, not become an empty one.
func TestListModelsGatewayWithoutPiKeepsOtherFacts(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	c := listRow(t, `{"id":"no-effort","capabilities":{"reasoning":true},"client_compat":{"pi":{"thinkingLevelMap":{"low":null,"high":null}}}}`)
	if len(c.EffortValues) != 0 || c.EffortValues == nil {
		t.Fatalf("EffortValues = %#v, want a published empty ladder", c.EffortValues)
	}
}

// TestListModelsGatewayPiWithoutMapStatesNoLadder pins that the Pi block's
// other compat fields do not count as a published map: silence about effort
// must leave an inherited ladder alone rather than erase it.
func TestListModelsGatewayPiWithoutMapStatesNoLadder(t *testing.T) {
	t.Parallel()
	c := listRow(t, `{"id":"max-tokens-only","client_compat":{"pi":{"maxTokensField":"max_tokens","supportsReasoningEffort":true}}}`)
	if c.EffortValues != nil {
		t.Fatalf("EffortValues = %#v, want nil when the block states no map", c.EffortValues)
	}
}

// TestListModelsReadsGatewayDialect pins the two wire-shaping compat fields a
// gateway publishes per model. A lunaroute glm row says zai; without reading
// it the row spells the openai dialect and the thinking object never opens.
func TestListModelsReadsGatewayDialect(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	c := listRow(t, `{"id":"both-windows","context_length":1000000,"context_window":1048576}`)
	if c.ContextWindow == nil || *c.ContextWindow != 1000000 {
		t.Fatalf("ContextWindow = %v, want context_length 1000000", c.ContextWindow)
	}
}

// TestListModelsPrefersTopProviderMaxCompletionTokens keeps OpenRouter's
// precedence: the per-provider cap beats every top-level output spelling.
func TestListModelsPrefersTopProviderMaxCompletionTokens(t *testing.T) {
	t.Parallel()
	c := listRow(t, `{"id":"provider-cap","top_provider":{"max_completion_tokens":128000},"max_completion_tokens":262144,"max_output_tokens":999}`)
	if c.MaxOutputTokens == nil || *c.MaxOutputTokens != 128000 {
		t.Fatalf("MaxOutputTokens = %v, want top_provider 128000", c.MaxOutputTokens)
	}
}

// TestListModelsPrefersTopLevelMaxCompletionTokens pins the top-level order:
// max_completion_tokens before max_output_tokens.
func TestListModelsPrefersTopLevelMaxCompletionTokens(t *testing.T) {
	t.Parallel()
	c := listRow(t, `{"id":"two-spellings","max_completion_tokens":262144,"max_output_tokens":999}`)
	if c.MaxOutputTokens == nil || *c.MaxOutputTokens != 262144 {
		t.Fatalf("MaxOutputTokens = %v, want max_completion_tokens 262144", c.MaxOutputTokens)
	}
}

// TestListModelsLeavesLimitsNilWhenUnadvertised pins that only positive values
// become caps: absent, zero and negative spellings all leave the pointer nil.
func TestListModelsLeavesLimitsNilWhenUnadvertised(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// TestListModelsDecodeFailure pins the listing's decode arm: a 200 body that
// is not the models JSON shape fails the listing rather than yielding empty
// rows silently.
func TestListModelsDecodeFailure(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, 200, `not json at all`)
	if _, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil)); err == nil {
		t.Fatal("an undecodable models listing must fail")
	}
}

// inklingModels is the /v1/models body a local OpenAI-compatible server
// publishes for a reasoning model: the reasoning fact is a plain boolean,
// the effort ladder and its default arrive in the Codex backend's
// supported_reasoning_levels / default_reasoning_level spellings, and the
// output cap lives under a limits object. Captured verbatim from the live
// listing; the reader used to fail the whole refresh on the boolean
// reasoning field.
const inklingModels = `{"object":"list","data":[{"object":"model","id":"Inkling-2-Mini","created":1795417889,"owned_by":"local","architecture":{"input_modalities":["text"],"output_modalities":["text"]},"input_modalities":["text"],"output_modalities":["text"],"reasoning":true,"supported_reasoning_levels":["none","minimal","low","medium","high","xhigh","max"],"default_reasoning_level":"high","context_length":262144,"limits":{"max_output_tokens":262144},"loom":{"kind":"text","endpoint":"/v1/chat/completions","tokenize":true,"input_tokens":true,"variants":{"selected":1,"count":1},"incarnation":1,"registration":"ready","device":"gpu","pool":"weights"}}]}`

// TestListModelsLoadsInklingMiniRow pins the full decode of that listing:
// a row whose every reasoning fact arrives in a spelling the
// OpenRouter-shaped reader never modeled must load, with its ladder,
// default effort, output cap and window intact.
func TestListModelsLoadsInklingMiniRow(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, 200, inklingModels)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "Inkling-2-Mini" {
		t.Fatalf("rows = %+v", rows)
	}
	c := rows[0].Caps
	if c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("Reasoning = %v, want true from the boolean verdict", c.Reasoning)
	}
	if got, want := c.EffortValues, []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}; !slices.Equal(got, want) {
		t.Fatalf("EffortValues = %v, want %v", got, want)
	}
	if got := registry.StringValue(c.DefaultEffort); got != "high" {
		t.Fatalf("DefaultEffort = %q, want high", got)
	}
	if c.ContextWindow == nil || *c.ContextWindow != 262144 {
		t.Fatalf("ContextWindow = %v, want 262144 from context_length", c.ContextWindow)
	}
	if c.MaxOutputTokens == nil || *c.MaxOutputTokens != 262144 {
		t.Fatalf("MaxOutputTokens = %v, want 262144 from limits.max_output_tokens", c.MaxOutputTokens)
	}
	if got, want := c.InputModalities, []string{"text"}; !slices.Equal(got, want) {
		t.Fatalf("InputModalities = %v, want %v", got, want)
	}
}

// TestListModelsReadsBoolReasoningVerdict pins the plain-boolean reasoning
// spelling: a server states the verdict itself rather than through
// OpenRouter's object, so `true` records a reasoning model and `false` a
// non-reasoning one that carries no ladder, whatever levels it lists. Null
// and shapes evener cannot read state nothing.
func TestListModelsReadsBoolReasoningVerdict(t *testing.T) {
	t.Parallel()
	if c := listRow(t, `{"id":"verdict-true","reasoning":true}`); c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("Reasoning = %v, want true", c.Reasoning)
	}
	if c := listRow(t, `{"id":"verdict-false","reasoning":false}`); c.Reasoning == nil || *c.Reasoning {
		t.Fatalf("Reasoning = %v, want an explicit false", c.Reasoning)
	}
	if c := listRow(t, `{"id":"verdict-false-with-levels","reasoning":false,"supported_reasoning_levels":["low","high"]}`); c.Reasoning == nil || *c.Reasoning || c.EffortValues != nil {
		t.Fatalf("a false verdict must clear the ladder: Reasoning = %v EffortValues = %v", c.Reasoning, c.EffortValues)
	}
	if c := listRow(t, `{"id":"verdict-null","reasoning":null}`); c.Reasoning != nil {
		t.Fatalf("a null verdict must state nothing: %v", c.Reasoning)
	}
	if c := listRow(t, `{"id":"verdict-string","reasoning":"always"}`); c.Reasoning != nil {
		t.Fatalf("an unreadable verdict shape must state nothing: %v", c.Reasoning)
	}
}

// TestListModelsObjectSilentAboutEffortsKeepsLadder pins the mixed-spelling
// row: a row may spell its ladder in supported_reasoning_levels while the
// reasoning object states only mandatory or default facts. An
// efforts-silent object must not wipe the ladder the levels spelled; only
// an object that publishes its own supported_efforts replaces it.
func TestListModelsObjectSilentAboutEffortsKeepsLadder(t *testing.T) {
	t.Parallel()
	c := listRow(t, `{"id":"levels-plus-object","supported_reasoning_levels":["low","high"],"reasoning":{"mandatory":false}}`)
	if got, want := c.EffortValues, []string{"low", "high"}; !slices.Equal(got, want) {
		t.Fatalf("EffortValues = %v, want %v from the levels", got, want)
	}
	if c.Reasoning == nil || !*c.Reasoning {
		t.Fatalf("Reasoning = %v, want true", c.Reasoning)
	}
	if c.ThinkingAlwaysOn != nil {
		t.Fatalf("ThinkingAlwaysOn = %v, want unset when mandatory is false", c.ThinkingAlwaysOn)
	}
	c = listRow(t, `{"id":"object-owns-efforts","supported_reasoning_levels":["low","high"],"reasoning":{"supported_efforts":["minimal"]}}`)
	if got, want := c.EffortValues, []string{"minimal"}; !slices.Equal(got, want) {
		t.Fatalf("EffortValues = %v, want %v: the object that publishes efforts is the later spelling", got, want)
	}
}

// TestListModelsIsolatesUndecodableRows pins the listing's row isolation:
// a row whose shape the reader cannot decode is dropped while its siblings
// load, so one alien row cannot fail the whole live refresh the way the
// boolean reasoning verdict once did. A row with no id stays silently
// skipped as before.
func TestListModelsIsolatesUndecodableRows(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, 200, `{"data":[
	 {"id":"alien-row","pricing":{"prompt":5}},
	 {"object":"model","created":1},
	 {"id":"healthy-row","context_length":8192}
	]}`)
	rows, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "healthy-row" || rows[0].Caps.ContextWindow == nil || *rows[0].Caps.ContextWindow != 8192 {
		t.Fatalf("rows = %+v, want only healthy-row with its window", rows)
	}
}

// TestListModelsFailsWhenNoRowDecodes keeps the tolerant decode honest: a
// body whose rows are all undecodable is an alien server, and the refresh
// must fail with the row's decode error rather than silently succeed with
// no models.
func TestListModelsFailsWhenNoRowDecodes(t *testing.T) {
	t.Parallel()
	srv, _ := server(t, 200, `{"data":[
	 {"id":"a","pricing":{"prompt":5}},
	 {"id":"b","context_length":"big"}
	]}`)
	_, err := (&Protocol{Client: srv.Client()}).ListModels(context.Background(), liveRes(srv, nil))
	if err == nil {
		t.Fatal("a listing with no decodable row must fail")
	}
	if _, ok := errors.AsType[*json.UnmarshalTypeError](err); !ok {
		t.Fatalf("err = %v, want it to wrap the row's decode error", err)
	}
}
