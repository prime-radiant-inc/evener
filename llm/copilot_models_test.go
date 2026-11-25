package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// copilotRefreshRoster is a subscription account's /models answer: two rows
// Evener can route, plus rows the picker, policy, or endpoint vocabulary must
// keep out of the catalog.
const copilotRefreshRoster = `{"data":[
 {"id":"claude-haiku-5.5","supported_endpoints":["/v1/messages","/chat/completions"],"model_picker_enabled":true,"capabilities":{"family":"claude","limits":{"max_prompt_tokens":200000,"max_output_tokens":32000},"supports":{"tool_calls":true,"vision":true}}},
 {"id":"gpt-5.6-sol","supported_endpoints":["/responses","/chat/completions"],"model_picker_enabled":true,"capabilities":{"family":"gpt","limits":{"max_output_tokens":64000},"supports":{"tool_calls":true}}},
 {"id":"picker-hidden","supported_endpoints":["/chat/completions"],"model_picker_enabled":false},
 {"id":"policy-disabled","supported_endpoints":["/chat/completions"],"policy":{"state":"disabled"}},
 {"id":"embedding-only","supported_endpoints":["/embeddings"]}
]}`

// copilotRefreshClient builds a client over one explicit Copilot instance whose
// /models endpoint is srv. defaultModel is the instance's configured default
// row, the row whose generation protocol a refresh must not adopt.
func copilotRefreshClient(t *testing.T, srv *httptest.Server, defaultModel string) *Client {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "providers.toml")
	cfg := "[providers.work]\nbase = \"github-copilot\"\ndefault_model = \"" + defaultModel + "\"\nbase_url = \"" + srv.URL + "/v1\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(
		registry.WithConfigPath(cfgPath), registry.WithStateRoot(t.TempDir()),
		registry.WithOffline(true), registry.WithoutCache(),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return NewClient(WithRegistry(r))
}

// copilotRefreshServer answers every /models request with the roster above,
// whatever generation endpoint the client reached.
func copilotRefreshServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	hits := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		*hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(copilotRefreshRoster))
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

// TestCopilotRefreshKeepsConfiguredListingTransport pins that a refresh never
// adopts the default row's generation protocol: the account roster assigns
// Claude rows the Messages path, so a Claude default would otherwise route the
// second /models fetch through /v1/messages and parse it as Anthropic pages,
// losing every advertised route and the picker/policy filter.
func TestCopilotRefreshKeepsConfiguredListingTransport(t *testing.T) {
	t.Parallel()
	srv, hits := copilotRefreshServer(t)
	c := copilotRefreshClient(t, srv, "claude-haiku-5.5")
	ctx := WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, copilotTestAuthenticator{})

	first, err := c.Models(ctx, "work")
	if err != nil {
		t.Fatalf("first listing: %v", err)
	}
	if !first.Live || !first.Usable {
		t.Fatalf("first listing = live %v usable %v, want a usable account listing", first.Live, first.Usable)
	}
	second, err := c.Models(ctx, "work")
	if err != nil {
		t.Fatalf("second listing: %v", err)
	}
	if got := *hits; got != 2 {
		t.Fatalf("listing fetches = %d, want 2", got)
	}
	if !second.Live || !second.Usable {
		t.Fatalf("second listing = live %v usable %v, want the same usable account listing", second.Live, second.Usable)
	}
	if got, want := modelIDs(second.Models), modelIDs(first.Models); !slices.Equal(got, want) {
		t.Fatalf("second listing ids = %v, want the first listing's %v", got, want)
	}
	for _, hidden := range []string{"picker-hidden", "policy-disabled", "embedding-only"} {
		if slices.Contains(modelIDs(second.Models), hidden) {
			t.Fatalf("second listing offered %q: the account's picker/policy filter was lost", hidden)
		}
	}
	for _, row := range second.Models {
		if row.ModelID != "gpt-5.6-sol" {
			continue
		}
		if row.Protocol != registry.ProtocolOpenAIResponses {
			t.Fatalf("gpt-5.6-sol protocol = %q, want the account's %q", row.Protocol, registry.ProtocolOpenAIResponses)
		}
		return
	}
	t.Fatalf("second listing ids = %v, want gpt-5.6-sol", modelIDs(second.Models))
}

// TestCopilotRosterMergedModalitiesStayTextOnly pins the merged catalog's
// modalities: the bundled models.dev row advertises images for gpt-5.6-sol, and
// an unproven live row that leaves modalities nil lets that bundle restore
// vision. Only the exact live-proven Haiku row, with the account advertising
// vision, may accept images.
func TestCopilotRosterMergedModalitiesStayTextOnly(t *testing.T) {
	t.Parallel()
	srv, _ := copilotRefreshServer(t)
	c := copilotRefreshClient(t, srv, "gpt-5.6-sol")
	ctx := WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, copilotTestAuthenticator{})
	if _, err := c.Models(ctx, "work"); err != nil {
		t.Fatalf("listing: %v", err)
	}

	gpt, err := c.Resolve("work/gpt-5.6-sol")
	if err != nil {
		t.Fatalf("resolve gpt-5.6-sol: %v", err)
	}
	if gpt.Caps.AcceptsModality("image") {
		t.Fatalf("gpt-5.6-sol accepts images after the merge: %+v", gpt.Caps.InputModalities)
	}
	if len(gpt.Caps.InputModalities) != 1 || gpt.Caps.InputModalities[0] != "text" {
		t.Fatalf("gpt-5.6-sol input modalities = %+v, want an explicit text-only row", gpt.Caps.InputModalities)
	}

	haiku, err := c.Resolve("work/claude-haiku-5.5")
	if err != nil {
		t.Fatalf("resolve claude-haiku-5.5: %v", err)
	}
	if !haiku.Caps.AcceptsModality("image") {
		t.Fatalf("claude-haiku-5.5 lost its advertised, live-proven image input: %+v", haiku.Caps.InputModalities)
	}
}

// TestCopilotRosterParserFollowsAuthScheme pins that a user protocol override
// retargets generation only: the account's listing is still parsed by the
// Copilot roster parser, so its picker/policy filter and per-row routing
// survive, and the configured models URL is the fetch destination.
func TestCopilotRosterParserFollowsAuthScheme(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(copilotRefreshRoster))
	}))
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "providers.toml")
	cfg := "[providers.work]\nbase = \"github-copilot\"\nprotocol = \"openai-responses\"\nmodels_endpoint = \"/configured/models\"\nbase_url = \"" + srv.URL + "/v1\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(
		registry.WithConfigPath(cfgPath), registry.WithStateRoot(t.TempDir()),
		registry.WithOffline(true), registry.WithoutCache(),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	c := NewClient(WithRegistry(r))
	ctx := WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, copilotTestAuthenticator{})
	listing, err := c.Models(ctx, "work")
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if !listing.Live || !listing.Usable {
		t.Fatalf("listing = live %v usable %v, want a usable account listing", listing.Live, listing.Usable)
	}
	if gotPath != "/v1/configured/models" {
		t.Fatalf("listing path = %q, want the configured /v1/configured/models", gotPath)
	}
	for _, hidden := range []string{"picker-hidden", "policy-disabled", "embedding-only"} {
		if slices.Contains(modelIDs(listing.Models), hidden) {
			t.Fatalf("listing offered %q: the account's picker/policy filter was lost", hidden)
		}
	}
	for _, row := range listing.Models {
		if row.ModelID != "claude-haiku-5.5" {
			continue
		}
		if row.Protocol != registry.ProtocolAnthropic {
			t.Fatalf("claude row protocol = %q, want the account's %q", row.Protocol, registry.ProtocolAnthropic)
		}
		return
	}
	t.Fatalf("listing ids = %v, want claude-haiku-5.5", modelIDs(listing.Models))
}

// TestCopilotRosterStaysOnTheConfiguredTransportBesideGatewayGeneration pins
// the listing half of the connection/generation boundary: a default row that
// generates through an API-key gateway must not move the roster fetch or the
// roster's authority. Two consecutive listings keep the configured models URL
// and every advertised route and visibility, and an empty account roster stays
// empty instead of resurrecting the static catalog.
func TestCopilotRosterStaysOnTheConfiguredTransportBesideGatewayGeneration(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var paths []string
	body := copilotRefreshRoster
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		response := body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "providers.toml")
	cfg := "[providers.work]\nbase = \"github-copilot\"\nbase_url = \"" + srv.URL + "/v1\"\n" +
		"api_key_env = [\"GATEWAY_KEY\"]\ndefault_model = \"gateway-gen\"\n" +
		"[providers.work.models.\"gateway-gen\"]\nauth = \"bearer\"\nbase_url = \"https://gateway.example.com/v1\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(
		registry.WithConfigPath(cfgPath), registry.WithStateRoot(t.TempDir()),
		registry.WithOffline(true), registry.WithoutCache(),
		registry.WithEnv(func(name string) (string, bool) {
			if name == "GATEWAY_KEY" {
				return "gw-secret", true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	c := NewClient(WithRegistry(r))
	ctx := WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, copilotTestAuthenticator{})

	first, err := c.Models(ctx, "work")
	if err != nil {
		t.Fatalf("first listing: %v", err)
	}
	second, err := c.Models(ctx, "work")
	if err != nil {
		t.Fatalf("second listing: %v", err)
	}
	for _, listing := range []ModelListing{first, second} {
		if !listing.Live || !listing.Usable {
			t.Fatalf("listing = live %v usable %v, want a usable account listing", listing.Live, listing.Usable)
		}
	}
	if got, want := modelIDs(second.Models), modelIDs(first.Models); !slices.Equal(got, want) {
		t.Fatalf("second listing ids = %v, want the first listing's %v", got, want)
	}
	for _, hidden := range []string{"picker-hidden", "policy-disabled", "embedding-only"} {
		if slices.Contains(modelIDs(second.Models), hidden) {
			t.Fatalf("second listing offered %q: the account's picker/policy filter was lost", hidden)
		}
	}
	routes := map[string]string{}
	for _, row := range second.Models {
		routes[row.ModelID] = row.Protocol
	}
	if routes["claude-haiku-5.5"] != registry.ProtocolAnthropic || routes["gpt-5.6-sol"] != registry.ProtocolOpenAIResponses {
		t.Fatalf("second listing routes = %v, want the account's per-row protocols", routes)
	}
	mu.Lock()
	gotPaths := slices.Clone(paths)
	body = `{"data":[]}`
	mu.Unlock()
	if want := []string{"/v1/models", "/v1/models"}; !slices.Equal(gotPaths, want) {
		t.Fatalf("listing paths = %v, want the configured models URL %v", gotPaths, want)
	}

	empty, err := c.Models(ctx, "work")
	if err != nil {
		t.Fatalf("empty roster listing: %v", err)
	}
	mu.Lock()
	emptyPath := paths[len(paths)-1]
	mu.Unlock()
	if emptyPath != "/v1/models" {
		t.Fatalf("empty roster path = %q, want the configured models URL", emptyPath)
	}
	if !empty.Live || empty.Usable || len(empty.Models) != 0 {
		t.Fatalf("empty roster listing = live %v usable %v rows %v, want an authoritative empty answer",
			empty.Live, empty.Usable, modelIDs(empty.Models))
	}
	if !c.ListingProvesUnavailable("work", empty) {
		t.Fatal("the empty account roster did not prove unavailability; the static catalog was resurrected")
	}
}

// gatewayCommandRoster is one account answer carrying a live-only id and the
// instance's gateway-backed default row, whose merged generation transport
// sends the instance's command-backed api_key.
const gatewayCommandRoster = `{"data":[
 {"id":"gateway-gen","supported_endpoints":["/chat/completions"],"model_picker_enabled":true,"capabilities":{"family":"gpt","supports":{"tool_calls":true}}},
 {"id":"roster-only","supported_endpoints":["/chat/completions"],"model_picker_enabled":true,"capabilities":{"family":"gpt","supports":{"tool_calls":true}}}
]}`

// commandRuns reports how many times the credential command appended to
// marker, zero when it never ran.
func commandRuns(t *testing.T, marker string) int {
	t.Helper()
	data, err := os.ReadFile(marker)
	if err != nil {
		return 0
	}
	return len(data)
}

// TestCopilotRosterListingRunsNoGenerationCommand pins the listing half of the
// gateway boundary: the roster lists, the live-only id it advertises is
// returned, and no listing pass executes the default row's generation command —
// neither the live resolve, LiveListingUsable's candidate judgment, nor the
// fallback a failed fetch takes. Generation through the gateway still runs it.
func TestCopilotRosterListingRunsNoGenerationCommand(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "cred-ran")
	var mu sync.Mutex
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		down := fail
		mu.Unlock()
		if down {
			http.Error(w, "roster unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(gatewayCommandRoster))
	}))
	t.Cleanup(srv.Close)

	cfgPath := filepath.Join(t.TempDir(), "providers.toml")
	cfg := "[providers.work]\nbase = \"github-copilot\"\nbase_url = \"" + srv.URL + "/v1\"\n" +
		"api_key = '''$(sh -c 'printf x >> " + marker + "; printf gw-secret')'''\n" +
		"default_model = \"gateway-gen\"\n" +
		"[providers.work.models.\"gateway-gen\"]\nauth = \"bearer\"\nbase_url = \"https://gateway.example.com/v1\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := registry.Load(
		registry.WithConfigPath(cfgPath), registry.WithStateRoot(t.TempDir()),
		registry.WithOffline(true), registry.WithoutCache(),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	c := NewClient(WithRegistry(r))
	ctx := WithScopedAuthenticator(context.Background(), registry.AuthOAuthGitHubCopilot, copilotTestAuthenticator{})

	listing, err := c.Models(ctx, "work")
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if !listing.Live || !listing.Usable {
		t.Fatalf("listing = live %v usable %v, want a usable account listing", listing.Live, listing.Usable)
	}
	for _, want := range []string{"roster-only", "gateway-gen"} {
		if !slices.Contains(modelIDs(listing.Models), want) {
			t.Fatalf("listing ids = %v, want the roster's %q", modelIDs(listing.Models), want)
		}
	}
	if got := commandRuns(t, marker); got != 0 {
		t.Fatalf("the live listing executed the generation command %d time(s), want 0", got)
	}

	mu.Lock()
	fail = true
	mu.Unlock()
	fallback, err := c.Models(ctx, "work")
	if err == nil {
		t.Fatal("a failed roster fetch must surface its error beside the fallback")
	}
	if fallback.Live || !slices.Contains(modelIDs(fallback.Models), "gateway-gen") {
		t.Fatalf("fallback listing = live %v ids %v, want the static rows including gateway-gen",
			fallback.Live, modelIDs(fallback.Models))
	}
	if got := commandRuns(t, marker); got != 0 {
		t.Fatalf("the fallback listing executed the generation command %d time(s), want 0", got)
	}

	// Control: the launch's own resolve still runs the gateway's command.
	gen, err := c.Resolve("work/gateway-gen")
	if err != nil {
		t.Fatalf("Resolve(work/gateway-gen): %v", err)
	}
	if gen.Transport.Auth != registry.AuthBearer || gen.Credential.Value != "gw-secret" {
		t.Fatalf("generation transport/credential = %q/%q, want the gateway's bearer key",
			gen.Transport.Auth, gen.Credential.Value)
	}
	if got := commandRuns(t, marker); got != 1 {
		t.Fatalf("generation ran the command %d time(s), want 1", got)
	}
}
