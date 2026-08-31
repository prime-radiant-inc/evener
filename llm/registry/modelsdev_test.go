package registry

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func loadFixture(t *testing.T) map[string]Provider {
	t.Helper()
	data, err := os.ReadFile("testdata/models.dev.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	provs, err := FromModelsDev(data)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Provider{}
	for _, p := range provs {
		byID[p.ID] = p
	}
	return byID
}

func TestFromModelsDev_ProviderLevel(t *testing.T) {
	p := loadFixture(t)

	groq := p["groq"]
	if groq.Protocol != ProtocolOpenAIChat || groq.Transport.Auth != AuthBearer {
		t.Fatalf("groq: protocol=%q auth=%q", groq.Protocol, groq.Transport.Auth)
	}
	if groq.Transport.BaseURL != "" {
		t.Fatalf("groq has no api upstream; BaseURL must stay empty, got %q", groq.Transport.BaseURL)
	}
	if !reflect.DeepEqual(groq.APIKeyEnv, []string{"GROQ_API_KEY"}) {
		t.Fatalf("groq APIKeyEnv = %v", groq.APIKeyEnv)
	}

	openai := p["openai"]
	if openai.Protocol != ProtocolOpenAIResponses {
		t.Fatalf("openai protocol = %q", openai.Protocol)
	}
	anth := p["anthropic"]
	if anth.Protocol != ProtocolAnthropic || anth.Transport.Auth != AuthHeader || anth.Transport.AuthHeader != "x-api-key" {
		t.Fatalf("anthropic: %+v", anth.Transport)
	}
	goog := p["google"]
	if goog.Protocol != ProtocolGoogle || goog.Transport.AuthHeader != "x-goog-api-key" {
		t.Fatalf("google: %+v", goog.Transport)
	}
	if p["cohere"].Hidden != true || p["watsonx"].Hidden != true {
		t.Fatalf("cohere/watsonx must be hidden by npm")
	}

	zai := p["zai"]
	if zai.Transport.BaseURL != "https://api.z.ai/api/paas/v4" {
		t.Fatalf("zai BaseURL = %q", zai.Transport.BaseURL)
	}
	if !reflect.DeepEqual(zai.APIKeyEnv, []string{"ZHIPU_API_KEY"}) {
		t.Fatalf("zai APIKeyEnv = %v", zai.APIKeyEnv)
	}

	cf := p["cloudflare-workers-ai"]
	if cf.Transport.BaseURL != "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1" {
		t.Fatalf("template placeholders must become {VAR}: %q", cf.Transport.BaseURL)
	}
	if cf.Transport.VarsEnv["CLOUDFLARE_ACCOUNT_ID"] != "CLOUDFLARE_ACCOUNT_ID" {
		t.Fatalf("template var must reach VarsEnv: %v", cf.Transport.VarsEnv)
	}

	azure := p["azure"]
	if azure.Protocol != ProtocolOpenAIResponses || azure.Transport.Auth != AuthHeader || azure.Transport.AuthHeader != "api-key" {
		t.Fatalf("azure: %+v", azure.Transport)
	}
	if !reflect.DeepEqual(azure.APIKeyEnv, []string{"AZURE_API_KEY"}) || azure.Transport.VarsEnv["AZURE_RESOURCE_NAME"] != "AZURE_RESOURCE_NAME" {
		t.Fatalf("azure env split wrong: key=%v vars=%v", azure.APIKeyEnv, azure.Transport.VarsEnv)
	}

	bedrock := p["amazon-bedrock"]
	if bedrock.Protocol != ProtocolAnthropic || bedrock.Transport.AuthHeader != "x-api-key" {
		t.Fatalf("bedrock: %+v", bedrock.Transport)
	}
	// The heuristic misfires on AWS_SECRET_ACCESS_KEY; the overlay pins this
	// later. Here we only assert the raw heuristic result is deterministic.
	if len(bedrock.APIKeyEnv) == 0 {
		t.Fatalf("bedrock heuristic should pick at least one *_KEY/*_TOKEN var")
	}

	vertex := p["google-vertex"]
	if vertex.Protocol != ProtocolGoogle || vertex.Transport.Auth != AuthGCPADC {
		t.Fatalf("google-vertex: %+v", vertex.Transport)
	}
	if len(vertex.APIKeyEnv) != 0 {
		t.Fatalf("vertex has no key var; got %v", vertex.APIKeyEnv)
	}
}

func TestFromModelsDev_ModelLevel(t *testing.T) {
	p := loadFixture(t)

	gpt5 := p["openai"].Models["gpt-5"]
	if gpt5.Caps.ContextWindow == nil || *gpt5.Caps.ContextWindow != 272000 {
		t.Fatalf("gpt-5 ContextWindow must be limit.input (272000), got %v", gpt5.Caps.ContextWindow)
	}
	if gpt5.Family != "gpt" || gpt5.Surface != SurfaceOpenAI {
		t.Fatalf("gpt-5 family/surface: %q/%q", gpt5.Family, gpt5.Surface)
	}

	opus45 := p["anthropic"].Models["claude-opus-4-5"]
	if !reflect.DeepEqual(opus45.Caps.ReasoningControls, []string{"effort", "budget_tokens"}) {
		t.Fatalf("opus-4-5 controls = %v", opus45.Caps.ReasoningControls)
	}
	if opus45.Caps.Sampling != nil {
		t.Fatalf("opus-4-5 accepts temperature; Sampling must stay unset")
	}
	if opus45.Surface != SurfaceAnthropic || opus45.Family != "claude-opus" {
		t.Fatalf("opus-4-5 surface/family: %q/%q", opus45.Surface, opus45.Family)
	}

	sonnet45 := p["anthropic"].Models["claude-sonnet-4-5"]
	if !reflect.DeepEqual(sonnet45.Caps.ReasoningControls, []string{"budget_tokens"}) {
		t.Fatalf("sonnet-4-5 controls = %v", sonnet45.Caps.ReasoningControls)
	}

	opus5 := p["anthropic"].Models["claude-opus-5"]
	if opus5.Caps.Sampling == nil || *opus5.Caps.Sampling {
		t.Fatalf("opus-5 has temperature:false upstream; Sampling must be false")
	}
	if !reflect.DeepEqual(opus5.Caps.EffortValues, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("opus-5 effort values = %v", opus5.Caps.EffortValues)
	}

	gpt55 := p["openai"].Models["gpt-5.5"]
	// The off level is carried through verbatim: it is what tells the
	// adapters this model can be turned off (spec §8.4). opus-5 above states
	// no off level and stays a ladder of thinking tiers only.
	if !reflect.DeepEqual(gpt55.Caps.EffortValues, []string{"none", "low", "medium", "high", "xhigh"}) {
		t.Fatalf("gpt-5.5 effort values = %v, want the off level kept", gpt55.Caps.EffortValues)
	}
	if gpt55.Caps.Cost == nil || gpt55.Caps.Cost.Input != 5 || len(gpt55.Caps.Cost.Tiers) != 1 || gpt55.Caps.Cost.Tiers[0].InputTokensAbove != 272000 {
		t.Fatalf("gpt-5.5 cost/tiers = %+v", gpt55.Caps.Cost)
	}

	k25 := p["moonshotai"].Models["kimi-k2.5"]
	if k25.Caps.MaxOutputTokens == nil || *k25.Caps.MaxOutputTokens != 262144 {
		t.Fatalf("converter must not clear the junk cap (derivation does): %v", k25.Caps.MaxOutputTokens)
	}
	if k25.Caps.ReasoningField == nil || *k25.Caps.ReasoningField != "reasoning_content" {
		t.Fatalf("interleaved.field must map to ReasoningField: %v", k25.Caps.ReasoningField)
	}

	azClaude := p["azure"].Models["claude-opus-4-5"]
	if azClaude.Protocol != ProtocolAnthropic {
		t.Fatalf("azure claude row must carry protocol anthropic from per-model npm, got %q", azClaude.Protocol)
	}
	if azClaude.Transport == nil || azClaude.Transport.BaseURL != "https://{AZURE_RESOURCE_NAME}.services.ai.azure.com/anthropic/v1" {
		t.Fatalf("azure claude per-model api: %+v", azClaude.Transport)
	}
	if azClaude.Transport.Auth != "" {
		t.Fatalf("per-model overrides never change Auth, got %q", azClaude.Transport.Auth)
	}

	mantle := p["amazon-bedrock"].Models["openai.gpt-oss-120b"]
	if mantle.Protocol != ProtocolOpenAIResponses || mantle.Transport == nil || mantle.Transport.Preset != "bedrock-mantle-openai" {
		t.Fatalf("mantle row: protocol=%q transport=%+v", mantle.Protocol, mantle.Transport)
	}
	if mantle.Transport.BaseURL != "https://bedrock-mantle.{AWS_REGION}.api.aws/v1" {
		t.Fatalf("mantle BaseURL = %q", mantle.Transport.BaseURL)
	}
	if !p["amazon-bedrock"].Models["global.openai.gpt-5.6-sol"].Hidden {
		t.Fatalf("OpenAI bedrock row without a mantle override must be hidden")
	}
	if p["amazon-bedrock"].Models["anthropic.claude-opus-5"].Hidden {
		t.Fatalf("Claude bedrock rows must not be hidden")
	}
	if p["amazon-bedrock"].Models["us.anthropic.claude-fable-5"].Hidden {
		t.Fatalf("region-prefixed Claude rows must not be hidden")
	}

	vc := p["google-vertex"].Models["claude-opus-5"]
	if vc.ID != "claude-opus-5" || vc.WireID != "claude-opus-5" {
		t.Fatalf("@default rows must be re-keyed: %+v", vc)
	}
	if vc.Protocol != ProtocolAnthropic || vc.Transport == nil || vc.Transport.Preset != "vertex-anthropic" {
		t.Fatalf("vertex claude row preset: %+v", vc.Transport)
	}
	if _, ok := p["google-vertex-anthropic"].Models["claude-sonnet-4-5@20250929"]; !ok {
		t.Fatalf("dated @version ids are kept verbatim")
	}
	if !p["google-vertex"].Models["openai/gpt-oss-120b-maas"].Hidden {
		t.Fatalf("vertex openai/*-maas rows without a template must be hidden")
	}

	zen := p["zenifra"].Models["alibaba/qwen3.6-35b-a3b"]
	if zen.Protocol != ProtocolOpenAIChat {
		t.Fatalf("shape completions must map to openai-chat, got %q", zen.Protocol)
	}

	if _, ok := p["groq"].Models["canopylabs/orpheus-v1-english"]; ok {
		t.Fatalf("rows without text output must be dropped")
	}
	if p["groq"].Models["llama-3.3-70b-versatile"].Caps.Reasoning == nil {
		t.Fatalf("reasoning fact must be set from the row")
	}
}

func TestFromModelsDev_SurfaceRule(t *testing.T) {
	cases := map[string]string{
		"claude-opus": SurfaceAnthropic, "gpt": SurfaceOpenAI, "gpt-oss": SurfaceGeneric,
		"o": SurfaceOpenAI, "o-mini": SurfaceOpenAI, "o-pro": SurfaceOpenAI,
		"gemini": SurfaceGoogle, "gemma": SurfaceGoogle, "kimi-k3": SurfaceGeneric, "": "",
	}
	for fam, want := range cases {
		if got := surfaceForFamily(fam); got != want {
			t.Errorf("surfaceForFamily(%q) = %q, want %q", fam, got, want)
		}
	}
}

func TestStripRegionPrefix(t *testing.T) {
	if stripRegionPrefix("global.anthropic.claude-opus-5") != "anthropic.claude-opus-5" ||
		stripRegionPrefix("anthropic.claude-opus-5") != "anthropic.claude-opus-5" ||
		stripRegionPrefix("au.anthropic.x") != "anthropic.x" {
		t.Fatal("stripRegionPrefix wrong")
	}
}

func TestFromModelsDev_Deterministic(t *testing.T) {
	data, err := os.ReadFile("testdata/models.dev.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := FromModelsDev(data)
	b, _ := FromModelsDev(data)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("conversion must be deterministic")
	}
	for i := 1; i < len(a); i++ {
		if a[i-1].ID >= a[i].ID {
			t.Fatalf("providers must be sorted by id: %q >= %q", a[i-1].ID, a[i].ID)
		}
	}
}

func TestNPMProtocol_Table(t *testing.T) {
	cases := []struct {
		npm      string
		protocol string
		auth     string
		hidden   bool
		known    bool
	}{
		{"@ai-sdk/groq", ProtocolOpenAIChat, AuthBearer, false, true},
		{"@ai-sdk/openai", ProtocolOpenAIResponses, AuthBearer, false, true},
		{"@ai-sdk/azure", ProtocolOpenAIResponses, AuthHeader, false, true},
		{"@ai-sdk/anthropic", ProtocolAnthropic, AuthHeader, false, true},
		{"@ai-sdk/google-vertex/anthropic", ProtocolAnthropic, AuthGCPADC, false, true},
		{"@ai-sdk/google", ProtocolGoogle, AuthHeader, false, true},
		{"@ai-sdk/google-vertex", ProtocolGoogle, AuthGCPADC, false, true},
		{"@ai-sdk/cohere", "", "", true, true},
		{"some-unknown-sdk", ProtocolOpenAIChat, AuthBearer, false, false},
	}
	for _, c := range cases {
		protocol, auth, hidden, known := npmProtocol(c.npm)
		if protocol != c.protocol || auth != c.auth || hidden != c.hidden || known != c.known {
			t.Errorf("npmProtocol(%q) = (%q, %q, %v, %v), want (%q, %q, %v, %v)",
				c.npm, protocol, auth, hidden, known, c.protocol, c.auth, c.hidden, c.known)
		}
	}
}

func TestIsKeyVar(t *testing.T) {
	for name, want := range map[string]bool{
		"OPENAI_API_KEY": true, "AWS_BEARER_TOKEN_BEDROCK": true, "AWS_SECRET_ACCESS_KEY": true,
		"GITHUB_PAT": true, "WATSONX_AI_APIKEY": true,
		"AWS_REGION": false, "AZURE_RESOURCE_NAME": false, "WATSONX_AI_PROJECT_ID": false,
	} {
		if got := isKeyVar(name); got != want {
			t.Errorf("isKeyVar(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestFromModelsDev_UnknownNPMFallsBackWithNote(t *testing.T) {
	data := []byte(`{"acme":{"id":"acme","npm":"some-unknown-sdk","models":{"m":{"id":"m","modalities":{"output":["text"]}}}}}`)
	provs, err := FromModelsDev(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 1 {
		t.Fatalf("want 1 provider, got %d", len(provs))
	}
	p := provs[0]
	if p.Protocol != ProtocolOpenAIChat {
		t.Fatalf("protocol = %q, want %q", p.Protocol, ProtocolOpenAIChat)
	}
	if p.Transport.Auth != AuthBearer {
		t.Fatalf("auth = %q, want %q", p.Transport.Auth, AuthBearer)
	}
	if p.Hidden {
		t.Fatalf("provider must not be hidden")
	}
	found := false
	for _, n := range p.notes {
		if strings.Contains(n, "protocol unverified") && strings.Contains(n, "some-unknown-sdk") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notes = %v, want a note containing %q and %q", p.notes, "protocol unverified", "some-unknown-sdk")
	}
}
