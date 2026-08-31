package hub

// The hub is not the only client of the credential wire: the React
// credentials pane and the TUI both decode evener/auth/status,
// evener/auth/logout and evener/instance/list, and both key on the registry's
// vocabulary (spec §11.3: activeSource is one of api_key |
// credential_headers | store | env:<VAR> | oauth | adc | none; authModes says
// which sign-in affordances the instance offers). A consumer that hand-builds
// those responses in its own tests pins a vocabulary the hub may no longer
// send — which is exactly how the TUI came to render "signed out" for an
// instance the hub reported as env:OPENAI_API_KEY.
//
// This test is the corpus that closes that gap: every case drives the REAL
// registered RPC handler over a hermetic registry, encodes the answer the way
// the wire does, and pins it in testdata/authwire/responses.json. Both other
// consumers decode that same file — cmd/evener-tui/hub_auth_wire_test.go and
// the frontend's credentialLabels.wire.test.ts — so no client can drift from
// the hub's answers without this test failing first.
//
// Regenerate after an intentional wire change with:
//
//	go test ./cmd/evener-hub -run TestAuthWireFixtures -update-authwire

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/appserver"
)

var updateAuthWire = flag.Bool("update-authwire", false,
	"rewrite cmd/evener-hub/testdata/authwire/responses.json from the current auth and instance handlers")

// authWireFixturePath is the committed corpus every credential client reads.
const authWireFixturePath = "testdata/authwire/responses.json"

// authWireFixture is one recorded RPC answer. Field, when set, names the one
// member of the response the fixture keeps: evener/instance/list also carries
// a descriptor for every curated provider, which no credential display reads
// and which would churn this corpus on an unrelated overlay edit.
type authWireFixture struct {
	Case     string          `json:"case"`
	Note     string          `json:"note"`
	Method   string          `json:"method"`
	Field    string          `json:"field,omitempty"`
	Response json.RawMessage `json:"response"`
}

// authWireScenario builds one hub surface and asks it one question.
type authWireScenario struct {
	name  string
	note  string
	field string
	run   func(t *testing.T) (method string, response any)
}

// dispatchAuthWire runs one request through the registered handlers, which is
// the only path the wire has: a handler that unregistered itself, or a
// controller method the router no longer reaches, fails here rather than
// producing a fixture nothing serves.
func dispatchAuthWire(t *testing.T, register func(*appserver.Server), req appwire.Request) any {
	t.Helper()
	server := appserver.NewServer(appserver.ServerConfig{ServerName: "hub", SourceID: "local"})
	register(server)
	raw, err := server.Router().Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("%s: %v", req.Method, err)
	}
	return raw
}

func authWireRequest(t *testing.T, method string, params any) appwire.Request {
	t.Helper()
	return appwire.Request{ID: appwire.NewIntID(1), Method: method, Params: mustMarshal(t, params)}
}

// authRPC drives one evener/auth/* method against a controller the scenario
// builds.
func authRPC(build func(t *testing.T) *hubAuthController, method string, params any) func(*testing.T) (string, any) {
	return func(t *testing.T) (string, any) {
		t.Helper()
		ctrl := build(t)
		req := authWireRequest(t, method, params)
		return method, dispatchAuthWire(t, func(s *appserver.Server) { registerAuthHandlers(s, ctrl) }, req)
	}
}

// adcCredentialsFile writes a stand-in application-default-credentials file
// and returns its path; the registry's ADC probe is a file-existence check.
func adcCredentialsFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "application_default_credentials.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write ADC file: %v", err)
	}
	return path
}

// statusOf is the common shape: one controller, one evener/auth/status call.
func statusOf(provider string, build func(t *testing.T) *hubAuthController) func(*testing.T) (string, any) {
	return authRPC(build, appwire.MethodEvenerAuthStatus, appwire.AuthStatusParams{Provider: provider})
}

// logoutOf is the common shape: one controller, one evener/auth/logout call.
func logoutOf(provider string, build func(t *testing.T) *hubAuthController) func(*testing.T) (string, any) {
	return authRPC(build, appwire.MethodEvenerAuthLogout, appwire.AuthLogoutParams{Provider: provider})
}

// authControllerOver builds a hermetic controller over one providers.toml
// body and one environment.
func authControllerOver(toml string, env map[string]string) func(t *testing.T) *hubAuthController {
	return func(t *testing.T) *hubAuthController {
		t.Helper()
		dir := t.TempDir()
		path := ""
		if toml != "" {
			path = writeProvidersToml(t, dir, toml)
		}
		return newTestAuthController(t, dir, t.TempDir(), path, env)
	}
}

// withStoredKey stores a key for name before the question is asked.
func withStoredKey(build func(t *testing.T) *hubAuthController, name string) func(t *testing.T) *hubAuthController {
	return func(t *testing.T) *hubAuthController {
		t.Helper()
		ctrl := build(t)
		if err := ctrl.setCredential(name, "sk-stored"); err != nil {
			t.Fatalf("store credential for %s: %v", name, err)
		}
		if err := ctrl.reloadRegistry(); err != nil {
			t.Fatalf("reload: %v", err)
		}
		return ctrl
	}
}

// withOAuthRecord signs name in before the question is asked. expiresIn
// shifts the record's expiry so the fixture can carry the refresh-due and
// expired states as well as the healthy one; the response reports those as
// booleans, so no timestamp reaches the corpus.
func withOAuthRecord(build func(t *testing.T) *hubAuthController, name, email string, expiresIn time.Duration) func(t *testing.T) *hubAuthController {
	return func(t *testing.T) *hubAuthController {
		t.Helper()
		ctrl := build(t)
		record := makeOAuthRecord(name, email)
		record.Expiry = time.Now().Add(expiresIn)
		if err := ctrl.saveAuth(ctrl.stateDir, name, record); err != nil {
			t.Fatalf("save OAuth record for %s: %v", name, err)
		}
		if err := ctrl.reloadRegistry(); err != nil {
			t.Fatalf("reload: %v", err)
		}
		return ctrl
	}
}

// gatewayToml is one authored gateway carrying its own Authorization header.
const gatewayToml = `[providers.gateway]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
[providers.gateway.credential_headers]
Authorization = "Bearer $GATEWAY_KEY"
`

// mixedInstancesToml is one providers.toml exercising several credential
// sources at once, so the instance-list fixture carries more than one row.
const mixedInstancesToml = `[providers.authored]
base = "anthropic"
api_key = "$AUTHORED_KEY"

[providers.gateway]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "none"

[providers.vertexish]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
auth = "gcp-adc"

[providers.headered]
base = "openai-compatible"
base_url = "http://127.0.0.1:9/v1"
[providers.headered.credential_headers]
Authorization = "Bearer $GATEWAY_KEY"

[providers.unkeyed]
base = "anthropic"
api_key_env = ["UNKEYED_KEY"]
`

func authWireScenarios() []authWireScenario {
	return []authWireScenario{
		{
			name: "status/env",
			note: "the curated openai instance authenticated by OPENAI_API_KEY",
			run:  statusOf("openai", authControllerOver("", map[string]string{"OPENAI_API_KEY": "sk-env"})),
		},
		{
			name: "status/store",
			note: "a key entered in the pane and kept in credentials.toml",
			run:  statusOf("anthropic", withStoredKey(authControllerOver("", nil), "anthropic")),
		},
		{
			name: "status/api_key",
			note: "an instance carrying its own api_key reference in providers.toml",
			run: statusOf("work", authControllerOver(
				"[providers.work]\nbase = \"anthropic\"\napi_key = \"$WORK_KEY\"\n",
				map[string]string{"WORK_KEY": "sk-authored"})),
		},
		{
			name: "status/credential_headers",
			note: "a gateway authenticated by an authored Authorization header",
			run:  statusOf("gateway", authControllerOver(gatewayToml, map[string]string{"GATEWAY_KEY": "sk-gateway"})),
		},
		{
			name: "status/oauth",
			note: "the Codex instance with a live OAuth record",
			run:  statusOf("openai-codex", withOAuthRecord(authControllerOver("", nil), "openai-codex", "bot@example.com", time.Hour)),
		},
		{
			name: "status/oauth-refreshable",
			note: "a Codex record inside its refresh window",
			run:  statusOf("openai-codex", withOAuthRecord(authControllerOver("", nil), "openai-codex", "bot@example.com", 2*time.Minute)),
		},
		{
			name: "status/oauth-expired",
			note: "a Codex record whose access token has expired",
			run:  statusOf("openai-codex", withOAuthRecord(authControllerOver("", nil), "openai-codex", "bot@example.com", -time.Minute)),
		},
		{
			name: "status/oauth-none",
			note: "the Codex instance with no record: /login's starting state",
			run:  statusOf("openai-codex", authControllerOver("", nil)),
		},
		{
			name: "status/adc",
			note: "an instance on application-default credentials",
			run: statusOf("vertexish", func(t *testing.T) *hubAuthController {
				t.Helper()
				return authControllerOver(
					"[providers.vertexish]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\nauth = \"gcp-adc\"\n",
					map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": adcCredentialsFile(t)})(t)
			}),
		},
		{
			name: "status/auth-none",
			note: "a local endpoint that wants no credential at all",
			run: statusOf("local", authControllerOver(
				"[providers.local]\nbase = \"openai-compatible\"\nbase_url = \"http://127.0.0.1:9/v1\"\nauth = \"none\"\n", nil)),
		},
		{
			name: "status/missing-key",
			note: "a curated instance with no credential anywhere",
			run:  statusOf("anthropic", authControllerOver("", nil)),
		},
		{
			name: "status/unsupported",
			note: "a name that is no instance and no curated provider",
			run:  statusOf("not-a-provider", authControllerOver("", nil)),
		},
		{
			name: "logout/oauth-removed",
			note: "signing out of the Codex instance deletes its OAuth record",
			run:  logoutOf("openai-codex", withOAuthRecord(authControllerOver("", nil), "openai-codex", "bot@example.com", time.Hour)),
		},
		{
			name: "logout/stored-key-cleared",
			note: "signing out of a key-authenticated instance clears the stored key",
			run:  logoutOf("anthropic", withStoredKey(authControllerOver("", nil), "anthropic")),
		},
		{
			name: "logout/nothing-to-remove",
			note: "signing out of the Codex instance that was never signed in",
			run:  logoutOf("openai-codex", authControllerOver("", nil)),
		},
		{
			name:  "instances/mixed-sources",
			note:  "the instance rows the credentials panes render, one per credential source",
			field: "instances",
			run: func(t *testing.T) (string, any) {
				t.Helper()
				dir := t.TempDir()
				stateDir := t.TempDir()
				tomlPath := filepath.Join(dir, "providers.toml")
				if err := os.WriteFile(tomlPath, []byte(mixedInstancesToml), 0o644); err != nil {
					t.Fatalf("write providers.toml: %v", err)
				}
				ctl := newTestInstancesController(t, tomlPath, dir, stateDir, map[string]string{
					"AUTHORED_KEY":                   "sk-authored",
					"GATEWAY_KEY":                    "sk-gateway",
					"OPENAI_API_KEY":                 "sk-env",
					"GOOGLE_APPLICATION_CREDENTIALS": adcCredentialsFile(t),
				})
				if err := ctl.auth.setCredential("anthropic", "sk-stored"); err != nil {
					t.Fatalf("store credential: %v", err)
				}
				if err := ctl.auth.saveAuth(stateDir, "openai-codex", makeOAuthRecord("openai-codex", "bot@example.com")); err != nil {
					t.Fatalf("save OAuth record: %v", err)
				}
				if err := ctl.reg.Reload(); err != nil {
					t.Fatalf("reload: %v", err)
				}
				req := authWireRequest(t, appwire.MethodEvenerInstanceList, appwire.EmptyParams{})
				raw := dispatchAuthWire(t, func(s *appserver.Server) { registerInstanceHandlers(s, ctl) }, req)
				resp, ok := raw.(appwire.InstanceListResponse)
				if !ok {
					t.Fatalf("evener/instance/list returned %T", raw)
				}
				return appwire.MethodEvenerInstanceList, resp.Instances
			},
		},
	}
}

// TestAuthWireFixturesMatchTheHubHandler regenerates or verifies the corpus
// every credential client decodes.
func TestAuthWireFixturesMatchTheHubHandler(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	scenarios := authWireScenarios()
	got := make([]authWireFixture, 0, len(scenarios))
	for _, sc := range scenarios {
		method, response := sc.run(t)
		encoded, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("%s: encode %s response: %v", sc.name, method, err)
		}
		got = append(got, authWireFixture{Case: sc.name, Note: sc.note, Method: method, Field: sc.field, Response: encoded})
	}
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("encode corpus: %v", err)
	}
	encoded = append(encoded, '\n')

	if *updateAuthWire {
		if err := os.MkdirAll(filepath.Dir(authWireFixturePath), 0o755); err != nil {
			t.Fatalf("create fixture dir: %v", err)
		}
		if err := os.WriteFile(authWireFixturePath, encoded, 0o644); err != nil {
			t.Fatalf("write fixtures: %v", err)
		}
		return
	}

	want, err := os.ReadFile(authWireFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with -update-authwire)", authWireFixturePath, err)
	}
	if !bytes.Equal(want, encoded) {
		t.Fatalf("the credential wire drifted from %s.\n got: %s\nwant: %s\nRegenerate with `go test ./cmd/evener-hub -run TestAuthWireFixtures -update-authwire`, then re-run the TUI and frontend tests that decode it.",
			authWireFixturePath, encoded, want)
	}
}
