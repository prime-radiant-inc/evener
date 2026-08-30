package main

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

// providersTestEnv isolates `evener providers` from the developer's machine: a
// temp config root the commands read and write, a temp state root, and a
// registry whose environment holds only what the case sets. It returns the
// config root (the directory holding providers.toml and credentials.toml).
func providersTestEnv(t *testing.T, env map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	lookup := func(name string) (string, bool) {
		if v, ok := env[name]; ok {
			return v, true
		}
		if name == "XDG_CONFIG_HOME" {
			return home, true
		}
		return "", false
	}
	old := cliRegistryOptions
	t.Cleanup(func() { cliRegistryOptions = old })
	cliRegistryOptions = []registry.Option{registry.WithoutCache(), registry.WithEnv(lookup)}
	root := filepath.Join(home, "evener")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeProvidersToml(t *testing.T, root, body string) string {
	t.Helper()
	path := filepath.Join(root, "providers.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// openAIProbeServer answers the three endpoints spec §11.2's probe touches:
// the model listing, Chat Completions (accepted), and Responses (a 400 that
// names the max-tokens field, which is inconclusive rather than unsupported).
func openAIProbeServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
		case "/chat/completions":
			_, _ = w.Write([]byte(`{"id":"c","model":"m1","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		case "/responses":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unsupported parameter: 'max_output_tokens'","type":"invalid_request_error","param":"max_output_tokens"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProvidersListShowsInstancesCredentialSourcesAndStrayEntries(t *testing.T) {
	root := providersTestEnv(t, map[string]string{"GROQ_API_KEY": "gk"})
	path := writeProvidersToml(t, root, "[providers.work]\nbase = \"openai\"\nbase_url = \"https://gw.example.com/v1\"\n")
	creds := "schema = 1\n[providers.kimi]\napi_key = \"old\"\n[providers.work]\napi_key = \"w\"\n"
	if err := os.WriteFile(filepath.Join(root, "credentials.toml"), []byte(creds), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"list"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("list: %v\n%s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"groq", "openai-chat", "env:GROQ_API_KEY", "work", "store", "https://gw.example.com/v1", "user layer: " + path} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "gk") || strings.Contains(out, "\"w\"") {
		t.Fatalf("list prints credential sources, never values (spec §11.2):\n%s", out)
	}
	if !strings.Contains(stderr.String(), `credentials.toml entry "kimi" names no instance`) {
		t.Fatalf("stray entries are reported (spec §14.1):\n%s", stderr.String())
	}
}

// The tri-state's third state is visible, not silent (spec §14.1).
func TestProvidersListReportsAnEmptyProvidersConfigAsNoUserLayer(t *testing.T) {
	providersTestEnv(t, map[string]string{"EVENER_PROVIDERS_CONFIG": ""})
	t.Setenv("EVENER_PROVIDERS_CONFIG", "")

	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"list"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("list: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "user layer: none (EVENER_PROVIDERS_CONFIG is empty)") {
		t.Fatalf("the empty tri-state must be named in the output:\n%s", stdout.String())
	}
}

// Flag day (spec §14.1): the CLI exits with the pointer rather than starting
// against an implicit-only registry the user did not ask for.
func TestProvidersListOnAnOldSchemaFileIsTheFlagDayError(t *testing.T) {
	root := providersTestEnv(t, nil)
	path := writeProvidersToml(t, root, "[instances.openai]\ntype = \"openai\"\n")

	var stdout, stderr bytes.Buffer
	err := runProviders([]string{"list"}, nil, &stdout, &stderr)
	if err == nil {
		t.Fatalf("an old-schema providers.toml must fail the command; stdout:\n%s", stdout.String())
	}
	if !errors.Is(err, registry.ErrOldSchema) || !strings.Contains(err.Error(), "§14.1") || !strings.Contains(err.Error(), path) {
		t.Fatalf("error must be the §14.1 pointer naming the file: %v", err)
	}
}

func TestProvidersListCheckReportsLiveReachability(t *testing.T) {
	srv := openAIProbeServer(t)
	// Every instance is checked, so the one implicit instance that needs no
	// credential (ollama) is pointed at the same server rather than the
	// developer's machine.
	root := providersTestEnv(t, map[string]string{"OLLAMA_HOST": srv.URL})
	writeProvidersToml(t, root, "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \""+srv.URL+"\"\n")

	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"list", "--check"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("list --check: %v\n%s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "LIVE") || !strings.Contains(out, "ok (1 models)") {
		t.Fatalf("--check must report live reachability:\n%s", out)
	}
	// An instance whose endpoint answers badly is a row, not a failed command:
	// ollama's URL carries the /v1 the test server does not serve.
	if !strings.Contains(out, "error: ") {
		t.Fatalf("--check must report an unreachable instance in its own row:\n%s", out)
	}
}

func TestProvidersProbeReportsProtocols(t *testing.T) {
	srv := openAIProbeServer(t)
	root := providersTestEnv(t, nil)
	writeProvidersToml(t, root, "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \""+srv.URL+"\"\n")

	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"probe", "gw"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("probe: %v\n%s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "openai-chat: ok") || !strings.Contains(out, "openai-responses: inconclusive") || !strings.Contains(out, "m1") {
		t.Fatalf("report:\n%s", out)
	}
	// Discovered models are printed, never written (spec §11.2).
	l, _, err := registry.ReadConfigFile(filepath.Join(root, "providers.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Providers["gw"].Models) != 0 || l.Providers["gw"].Protocol != "" {
		t.Fatalf("a probe without --write changed the entry: %+v", l.Providers["gw"])
	}
}

// An endpoint that never answered says nothing about the protocol: a refused
// connection is an error, not a verdict that the protocol is unsupported.
func TestProvidersProbeReportsAnUnreachableEndpointAsAnError(t *testing.T) {
	// A port with nothing listening: the server claims one, then hands it back.
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := closed.URL
	closed.Close()

	root := providersTestEnv(t, nil)
	writeProvidersToml(t, root, "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \""+addr+"\"\ndefault_model = \"m1\"\n")

	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"probe", "gw"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("probe: %v\n%s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "openai-chat: error") || !strings.Contains(out, "openai-responses: error") {
		t.Fatalf("an unreachable endpoint must be reported as an error:\n%s", out)
	}
	if strings.Contains(out, "unsupported") {
		t.Fatalf("unsupported is for provider-side rejections only:\n%s", out)
	}
}

// Only the two OpenAI protocols are interchangeable: every other protocol is
// probed as itself alone (spec §11.2).
func TestProvidersProbeOnANonOpenAIProtocolProbesOnlyItsOwn(t *testing.T) {
	srv := openAIProbeServer(t)
	root := providersTestEnv(t, map[string]string{"ANTHROPIC_API_KEY": "sk"})
	writeProvidersToml(t, root, "[providers.anth]\nbase = \"anthropic\"\nbase_url = \""+srv.URL+"\"\n")

	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"probe", "anth"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("probe: %v\n%s", err, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "anthropic: ") {
		t.Fatalf("the instance's own protocol must be probed:\n%s", out)
	}
	if strings.Contains(out, "openai-chat") || strings.Contains(out, "openai-responses") {
		t.Fatalf("an anthropic instance must not be probed on the OpenAI protocols:\n%s", out)
	}
}

func TestProvidersProbeWriteRecordsTheOneProtocolThatSucceeded(t *testing.T) {
	srv := openAIProbeServer(t)
	root := providersTestEnv(t, nil)
	path := writeProvidersToml(t, root, "[providers.gw]\nbase = \"openai-compatible\"\nbase_url = \""+srv.URL+"\"\n")

	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"probe", "gw", "--write"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("probe --write: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "wrote protocol = openai-chat to "+path) {
		t.Fatalf("probe --write must say what it wrote:\n%s", stdout.String())
	}
	l, exists, err := registry.ReadConfigFile(path)
	if err != nil || !exists || l.Providers["gw"].Protocol != "openai-chat" {
		t.Fatalf("written entry: %v %v %+v", err, exists, l.Providers["gw"])
	}
}

func TestProvidersAddWritesEntryAndSkipsProbeWithoutCredential(t *testing.T) {
	root := providersTestEnv(t, nil)

	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"add", "gw", "--base", "openai", "--base-url", "https://gw.example.com/v1",
		"--protocol", "openai-chat", "--surface", "generic",
		"--credential-header", "Authorization=Bearer $PORTKEY_KEY"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("add: %v\n%s", err, stderr.String())
	}
	l, exists, err := registry.ReadConfigFile(filepath.Join(root, "providers.toml"))
	if err != nil || !exists || l.Providers["gw"].CredentialHeaders["Authorization"] != "Bearer $PORTKEY_KEY" || l.Providers["gw"].Protocol != "openai-chat" {
		t.Fatalf("written entry: %v %v %+v", err, exists, l.Providers["gw"])
	}
	out := stdout.String()
	if !strings.Contains(out, "PORTKEY_KEY") || !strings.Contains(out, "no credential resolves for gw") {
		t.Fatalf("add prints what to set when no credential resolves:\n%s", out)
	}
	// An entry carrying credential_headers never reaches the store or the
	// environment (spec §10 resolution order), so suggesting them would send
	// the user down a dead end — only PORTKEY_KEY resolves this instance.
	if strings.Contains(out, "GW_API_KEY") || strings.Contains(out, "--api-key-env") || strings.Contains(out, "credentials pane") {
		t.Fatalf("the pointer must not suggest layers this entry can never reach:\n%s", out)
	}
}

// The pointer is printed when it is the truth: an entry with no api_key and
// no credential_headers really is resolved by the store or the environment.
func TestProvidersAddPrintsTheCredentialPointerWhenItHelps(t *testing.T) {
	providersTestEnv(t, nil)
	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"add", "gw", "--base", "openai", "--base-url", "https://gw.example.com/v1"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("add: %v\n%s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"no credential resolves for gw", "GW_API_KEY", "--api-key-env", "credentials.toml [providers.gw]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
}

// A probe that cannot run is a report, not a failed add: providers.toml
// changed, so a script must not see the command fail.
func TestProvidersAddExitsZeroWhenTheProbeCannotRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	root := providersTestEnv(t, nil)

	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"add", "gw", "--base", "openai-compatible", "--base-url", srv.URL}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("the entry was written, so add succeeded: %v", err)
	}
	if !strings.Contains(stdout.String(), "wrote [providers.gw]") {
		t.Fatalf("the write must still be reported:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no model to send") {
		t.Fatalf("the probe failure must be reported on stderr:\n%s", stderr.String())
	}
	l, exists, err := registry.ReadConfigFile(filepath.Join(root, "providers.toml"))
	if err != nil || !exists || l.Providers["gw"].Base != "openai-compatible" {
		t.Fatalf("the entry must survive a failed probe: %v %v %+v", err, exists, l.Providers["gw"])
	}
}

// Secrets never appear on the command line (spec §11.2): a credential header
// carries $VARIABLE references, never the key itself. The accepted shape
// (`Bearer $PORTKEY_KEY`) is pinned by
// TestProvidersAddWritesEntryAndSkipsProbeWithoutCredential.
func TestProvidersAddRefusesALiteralCredentialHeader(t *testing.T) {
	for _, tt := range []struct {
		name   string
		header string
		secret string
	}{
		{"no reference at all", "Authorization=Bearer literal-secret", "literal-secret"},
		{"a literal key smuggled beside a reference", "Authorization=Bearer sk-live-abc$X", "sk-live-abc"},
		{"a literal key as its own word", "Authorization=Bearer sk-live-abc $X", "sk-live-abc"},
		{"an unterminated reference", "Authorization=Bearer ${PORTKEY", "${PORTKEY"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := providersTestEnv(t, nil)
			var stdout, stderr bytes.Buffer
			err := runProviders([]string{"add", "bad", "--base", "openai", "--credential-header", tt.header}, nil, &stdout, &stderr)
			if err == nil {
				t.Fatal("a credential header that is not a $VARIABLE reference is refused")
			}
			if !strings.Contains(err.Error(), "$VARIABLE") {
				t.Fatalf("the refusal must say what is accepted: %v", err)
			}
			if strings.Contains(err.Error(), tt.secret) || strings.Contains(stdout.String(), tt.secret) || strings.Contains(stderr.String(), tt.secret) {
				t.Fatalf("the refusal must not echo the value: %v %q %q", err, stdout.String(), stderr.String())
			}
			if _, statErr := os.Stat(filepath.Join(root, "providers.toml")); !os.IsNotExist(statErr) {
				t.Fatalf("a refused add wrote providers.toml (stat err = %v)", statErr)
			}
		})
	}
}

func TestProvidersAddRefusesABadNameAnUnknownBaseAndADuplicate(t *testing.T) {
	root := providersTestEnv(t, nil)
	writeProvidersToml(t, root, "[providers.gw]\nbase = \"openai\"\nbase_url = \"https://gw.example.com/v1\"\n")

	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"invalid name", []string{"add", "Work", "--base", "openai"}, "invalid instance name"},
		{"missing base", []string{"add", "work"}, "--base is required"},
		{"unknown base", []string{"add", "work", "--base", "nope"}, "unknown base provider"},
		{"duplicate", []string{"add", "gw", "--base", "openai"}, "already exists"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runProviders(tt.args, nil, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("add = %v, want an error mentioning %q", err, tt.want)
			}
		})
	}
}

// A no-probe add still writes and still says what resolved (spec §11.2).
func TestProvidersAddNoProbeWritesWithoutTouchingTheEndpoint(t *testing.T) {
	root := providersTestEnv(t, map[string]string{"GW_KEY": "k"})
	var stdout, stderr bytes.Buffer
	if err := runProviders([]string{"add", "gw", "--base", "openai", "--base-url", "http://127.0.0.1:1/v1",
		"--api-key-env", "GW_KEY", "--var", "REGION=eu", "--no-probe"}, nil, &stdout, &stderr); err != nil {
		t.Fatalf("add --no-probe: %v\n%s", err, stderr.String())
	}
	l, _, err := registry.ReadConfigFile(filepath.Join(root, "providers.toml"))
	if err != nil {
		t.Fatal(err)
	}
	gw := l.Providers["gw"]
	if len(gw.APIKeyEnv) != 1 || gw.APIKeyEnv[0] != "GW_KEY" || gw.Transport.Vars["REGION"] != "eu" {
		t.Fatalf("written entry: %+v", gw)
	}
	if strings.Contains(stdout.String(), "no credential resolves") {
		t.Fatalf("GW_KEY resolves the credential:\n%s", stdout.String())
	}
}

func TestProvidersUsageAndUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runProviders(nil, nil, &stdout, &stderr); err != nil {
		t.Fatalf("no arguments prints usage: %v", err)
	}
	for _, want := range []string{"list", "probe", "add"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("usage must list %q:\n%s", want, stderr.String())
		}
	}
	stderr.Reset()
	if err := runProviders([]string{"nope"}, nil, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), `unknown providers command "nope"`) {
		t.Fatalf("unknown command = %v", err)
	}
}

func TestProvidersDispatchesFromTopLevel(t *testing.T) {
	providersTestEnv(t, nil)
	var stdout, stderr bytes.Buffer
	handled, label, err := dispatchCLICommand([]string{"providers", "list"}, strings.NewReader(""), &stdout, &stderr)
	if !handled || label != "evener providers" || err != nil {
		t.Fatalf("dispatch = %v %q %v", handled, label, err)
	}
	if !strings.Contains(stdout.String(), "NAME") {
		t.Fatalf("list ran through the dispatcher:\n%s", stdout.String())
	}
}
