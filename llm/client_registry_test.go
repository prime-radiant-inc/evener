package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/llm"
	_ "primeradiant.com/evener/llm/providers/all"
	"primeradiant.com/evener/llm/registry"
)

// fixtureRegistry injects an openai (responses) and a work (chat) instance
// that both point at srvURL, with no environment and no user layer.
func fixtureRegistry(t *testing.T, srvURL string, extra map[string]registry.Provider) *registry.Registry {
	t.Helper()
	instances := map[string]registry.Provider{
		"openai": {Base: "openai", APIKey: "test-key", Transport: registry.Transport{BaseURL: srvURL}},
		"work":   {Base: "openai", Protocol: registry.ProtocolOpenAIChat, Surface: registry.SurfaceGeneric, APIKey: "work-key", Transport: registry.Transport{BaseURL: srvURL}},
	}
	maps.Copy(instances, extra)
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return r
}

type recordedRequest struct {
	Path string
	Auth string
	Body map[string]any
}

// responsesServer answers /responses with one assistant message and
// /chat/completions with one choice, and records every request.
func responsesServer(t *testing.T) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seen = append(seen, recordedRequest{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
		mu.Unlock()
		if stream, _ := body["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-1\",\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n" +
				"data: {\"id\":\"chatcmpl-1\",\"model\":\"glm-5\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n" +
				"data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/responses":
			_, _ = w.Write([]byte(`{"id":"resp_1","model":"gpt-5.5","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
		case "/chat/completions":
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"glm-5","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		case "/responses/input_tokens":
			_, _ = w.Write([]byte(`{"input_tokens":42}`))
		case "/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.5"},{"id":"live-only-model"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedRequest { mu.Lock(); defer mu.Unlock(); return append([]recordedRequest(nil), seen...) }
}

type recordingAdapter struct {
	name string
	mu   sync.Mutex
	reqs []llm.Request
}

func (a *recordingAdapter) Name() string { return a.name }
func (a *recordingAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reqs = append(a.reqs, req)
	return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("ok")}, nil
}
func (a *recordingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func (a *recordingAdapter) last() llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reqs[len(a.reqs)-1]
}

func userRequest(provider, model string) llm.Request {
	return llm.Request{Provider: provider, Model: model, Messages: []llm.Message{llm.User("hello")}}
}

func TestClientDispatchesByProtocolWithCredential(t *testing.T) {
	srv, seen := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	resp, err := c.Complete(context.Background(), userRequest("openai", "gpt-5.5"))
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Provider != "openai" || resp.Message.Text() != "hi" {
		t.Fatalf("response: %+v", resp)
	}
	reqs := seen()
	if len(reqs) != 1 || reqs[0].Path != "/responses" || reqs[0].Auth != "Bearer test-key" {
		t.Fatalf("wire: %+v", reqs)
	}
	resp, err = c.Complete(context.Background(), userRequest("work", "glm-5"))
	if err != nil || resp.Provider != "work" {
		t.Fatalf("chat instance: %v %+v", err, resp)
	}
	if reqs := seen(); reqs[1].Path != "/chat/completions" || reqs[1].Auth != "Bearer work-key" {
		t.Fatalf("chat wire: %+v", reqs[1])
	}
}

// TestClientStreamsThroughTheProtocol is Complete's twin: Stream reaches the
// same resolved record, and providerStampStream puts the instance name on the
// terminal response.
func TestClientStreamsThroughTheProtocol(t *testing.T) {
	srv, seen := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	st, err := c.Stream(context.Background(), userRequest("work", "glm-5"))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer func() { _ = st.Close() }()
	var text strings.Builder
	var final *llm.Response
	for ev := range st.Events() {
		switch ev.Type {
		case llm.StreamEventTextDelta:
			text.WriteString(ev.Delta)
		case llm.StreamEventError:
			t.Fatalf("stream error: %v", ev.Err)
		case llm.StreamEventFinish:
			final = ev.Response
		}
	}
	if text.String() != "hi" || final == nil || final.Provider != "work" {
		t.Fatalf("stream text=%q final=%+v", text.String(), final)
	}
	if reqs := seen(); len(reqs) != 1 || reqs[0].Path != "/chat/completions" || reqs[0].Auth != "Bearer work-key" {
		t.Fatalf("stream wire: %+v", reqs)
	}
}

func TestClientOverrideUnderResolvableNameSeesShapedRequest(t *testing.T) {
	srv, _ := responsesServer(t)
	r := fixtureRegistry(t, srv.URL, map[string]registry.Provider{
		"capped": {Base: "openai", APIKey: "k", Transport: registry.Transport{BaseURL: srv.URL}, Caps: registry.Caps{MaxOutputTokens: new(123), Sampling: new(false)}},
	})
	c := llm.NewClient(llm.WithRegistry(r))
	fake := &recordingAdapter{name: "capped"}
	c.Register(fake)
	req := userRequest("capped", "gpt-5.5")
	req.Temperature = new(0.5)
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := fake.last()
	if got.MaxTokens == nil || *got.MaxTokens != 123 {
		t.Fatalf("ShapeRequest must apply MaxOutputTokens before the override sees the request: %+v", got.MaxTokens)
	}
	if got.Temperature != nil {
		t.Fatal("ShapeRequest must drop sampling the row turned off")
	}
}

func TestClientOverrideUnderUnresolvableNamePassesThrough(t *testing.T) {
	c := llm.NewClient()
	fake := &recordingAdapter{name: "fake"}
	c.Register(fake)
	req := userRequest("fake", "anything")
	req.Temperature = new(0.5)
	resp, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("an unresolvable override must not error: %v", err)
	}
	if resp.Provider != "fake" {
		t.Fatalf("provider stamp: %q", resp.Provider)
	}
	got := fake.last()
	if got.MaxTokens != nil || got.Temperature == nil {
		t.Fatalf("request must pass through untouched: %+v", got)
	}
	if c.DefaultProvider() != "fake" {
		t.Fatalf("the first override is the default when the registry has none: %q", c.DefaultProvider())
	}
}

// TestClientCountInputTokensThroughTheProtocol covers both halves of the
// resolved counting path: an instance whose transport has a count-tokens
// endpoint answers exactly, and one whose endpoint is "-" falls back to the
// local estimate rather than failing.
func TestClientCountInputTokensThroughTheProtocol(t *testing.T) {
	srv, seen := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	got, err := c.CountInputTokens(context.Background(), userRequest("openai", "gpt-5.5"))
	if err != nil {
		t.Fatalf("CountInputTokens: %v", err)
	}
	if got.Tokens != 42 || !got.Exact || got.Source != llm.TokenCountSourceProvider || got.Provider != "openai" {
		t.Fatalf("exact count: %+v", got)
	}
	if reqs := seen(); len(reqs) != 1 || reqs[0].Path != "/responses/input_tokens" {
		t.Fatalf("count wire: %+v", reqs)
	}
	got, err = c.CountInputTokens(context.Background(), userRequest("work", "glm-5"))
	if err != nil {
		t.Fatalf("an instance without a count-tokens endpoint estimates: %v", err)
	}
	if got.Exact || got.Source != llm.TokenCountSourceLocalEstimate || got.Provider != "work" || got.Tokens == 0 {
		t.Fatalf("estimated count: %+v", got)
	}
}

func TestClientUnknownInstanceNamesAvailableOnes(t *testing.T) {
	srv, _ := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	_, err := c.Complete(context.Background(), userRequest("nope", "gpt-5.5"))
	var cfgErr *llm.ConfigurationError
	if !errors.As(err, &cfgErr) || !strings.Contains(err.Error(), `unknown instance "nope"`) || !strings.Contains(err.Error(), "openai") {
		t.Fatalf("want ConfigurationError naming the available instances, got %v", err)
	}
}

func TestClientEmbeddedRegistryIsHermetic(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "leak")
	c := llm.NewClient()
	res, err := c.Resolve("openai/gpt-5.5")
	if err != nil {
		t.Fatalf("a curated implicit id resolves without a credential (spec §5.2): %v", err)
	}
	if res.Credential.Value != "" || res.Credential.Source != "none" {
		t.Fatalf("the lazy registry must not read the process environment: %+v", res.Credential)
	}
	if c.DefaultProvider() != "" {
		t.Fatalf("no instances, no default: %q", c.DefaultProvider())
	}
}

func TestClientModelsAppliesLiveListingAndHidesToolLessRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"tools-ok","supported_parameters":["tools","temperature"]},{"id":"no-tools","supported_parameters":["temperature"]}]}`))
	}))
	t.Cleanup(srv.Close)
	r := fixtureRegistry(t, srv.URL, nil)
	c := llm.NewClient(llm.WithRegistry(r))
	listing, err := c.Models(context.Background(), "work")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if !listing.Live {
		t.Fatal("a transport with a models endpoint yields a live listing")
	}
	ids := map[string]bool{}
	for _, m := range listing.Models {
		ids[m.ModelID] = true
		if m.Instance != "work" {
			t.Fatalf("every row is resolved on the instance: %+v", m)
		}
	}
	if !ids["tools-ok"] || ids["no-tools"] {
		t.Fatalf("live Tools=false hides the row (spec §5, §7.5): %v", ids)
	}
	res, err := c.Resolve("work/tools-ok")
	if err != nil || res.Provenance["model"] != "live" {
		t.Fatalf("the live row must be resolvable afterwards: %v %v", err, res.Provenance["model"])
	}
}

func TestClientModelsRegistryOnlyWhenUnsupported(t *testing.T) {
	r := fixtureRegistry(t, "http://127.0.0.1:9", map[string]registry.Provider{
		"nolist": {Base: "openai", APIKey: "k", Transport: registry.Transport{BaseURL: "http://127.0.0.1:9", ModelsEndpoint: registry.EndpointUnsupported}},
	})
	c := llm.NewClient(llm.WithRegistry(r))
	listing, err := c.Models(context.Background(), "nolist")
	if err != nil {
		t.Fatalf("an unsupported models endpoint is not a failure: %v", err)
	}
	if listing.Live || len(listing.Models) == 0 {
		t.Fatalf("registry-only listing must return the catalog rows: live=%v n=%d", listing.Live, len(listing.Models))
	}
}

func TestClientModelsOverrideLister(t *testing.T) {
	c := llm.NewClient()
	lister := &listingAdapter{models: []registry.Model{{ID: "m1"}, {ID: "m0"}}}
	lister.name = "fake"
	c.Register(lister)
	listing, err := c.Models(context.Background(), "fake")
	if err != nil || !listing.Live || len(listing.Models) != 2 || listing.Models[0].ModelID != "m0" {
		t.Fatalf("override listing: %v %+v", err, listing)
	}
	c.Register(&recordingAdapter{name: "mute"})
	if _, err := c.Models(context.Background(), "mute"); err == nil {
		t.Fatal("an unresolvable override without LiveModels cannot list")
	}
}

// TestClientModelsResolvableOverrideWithoutListerCannotList pins the rule that
// an override owns its instance name for listing too: a registered adapter
// that cannot list models never falls through to a registry-only listing,
// even when the registry knows the name.
func TestClientModelsResolvableOverrideWithoutListerCannotList(t *testing.T) {
	srv, _ := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	c.Register(&recordingAdapter{name: "openai"})
	_, err := c.Models(context.Background(), "openai")
	var cfgErr *llm.ConfigurationError
	if !errors.As(err, &cfgErr) || !strings.Contains(err.Error(), "does not support listing models") {
		t.Fatalf("want a ConfigurationError from the override, got %v", err)
	}
}

type listingAdapter struct {
	recordingAdapter
	models []registry.Model
}

func (a *listingAdapter) LiveModels(context.Context) ([]registry.Model, error) { return a.models, nil }

func TestClientProviderNamesUnionsOverridesAndInstances(t *testing.T) {
	srv, _ := responsesServer(t)
	c := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, srv.URL, nil)))
	c.Register(&recordingAdapter{name: "fake"})
	names := strings.Join(c.ProviderNames(), ",")
	for _, want := range []string{"fake", "openai", "work"} {
		if !strings.Contains(names, want) {
			t.Fatalf("missing %s in %s", want, names)
		}
	}
	if c.DefaultProvider() != "openai" {
		t.Fatalf("the registry ranks openai first (spec §5.1): %q", c.DefaultProvider())
	}
}
