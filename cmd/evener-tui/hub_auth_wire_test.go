package tui

// The TUI is the hub's second credential client, and it decodes the same
// evener/auth/* wire the React pane does (spec §11.3). Every value here comes
// from cmd/evener-hub/testdata/authwire/responses.json, which
// cmd/evener-hub's TestAuthWireFixturesMatchTheHubHandler produces by driving
// the real registered handlers and re-verifies on every run. Nothing in this
// file hand-builds an authStatus: the TUI's tests once did, and that is how
// the TUI kept branching on auth/openai's pre-registry constants — rendering
// "signed out" for an instance the hub was reporting as env:OPENAI_API_KEY —
// with a green suite the whole time.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

// authWireFixture mirrors one record of the hub's corpus.
type authWireFixture struct {
	Case     string          `json:"case"`
	Note     string          `json:"note"`
	Method   string          `json:"method"`
	Field    string          `json:"field,omitempty"`
	Response json.RawMessage `json:"response"`
}

// authWireFixturePath is the hub's corpus, read from the hub's own testdata:
// the file belongs to the producer, and a copy in this package would be one
// more thing to drift.
const authWireFixturePath = "../evener-hub/testdata/authwire/responses.json"

func loadAuthWireFixtures(t *testing.T) map[string]authWireFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(authWireFixturePath))
	if err != nil {
		t.Fatalf("read %s: %v", authWireFixturePath, err)
	}
	var records []authWireFixture
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("decode %s: %v", authWireFixturePath, err)
	}
	out := make(map[string]authWireFixture, len(records))
	for _, rec := range records {
		out[rec.Case] = rec
	}
	if len(out) != len(records) {
		t.Fatalf("%s has duplicate case names", authWireFixturePath)
	}
	return out
}

// hubAuthStatus decodes one recorded evener/auth/status response.
func hubAuthStatus(t *testing.T, fixtures map[string]authWireFixture, name string) appwire.AuthStatusResponse {
	t.Helper()
	rec, ok := fixtures[name]
	if !ok {
		t.Fatalf("no fixture %q in %s", name, authWireFixturePath)
	}
	if rec.Method != appwire.MethodEvenerAuthStatus {
		t.Fatalf("fixture %q is %s, not a status response", name, rec.Method)
	}
	var resp appwire.AuthStatusResponse
	if err := json.Unmarshal(rec.Response, &resp); err != nil {
		t.Fatalf("decode fixture %q: %v", name, err)
	}
	return resp
}

// hubAuthLogout decodes one recorded evener/auth/logout response.
func hubAuthLogout(t *testing.T, fixtures map[string]authWireFixture, name string) appwire.AuthLogoutResponse {
	t.Helper()
	rec, ok := fixtures[name]
	if !ok {
		t.Fatalf("no fixture %q in %s", name, authWireFixturePath)
	}
	if rec.Method != appwire.MethodEvenerAuthLogout {
		t.Fatalf("fixture %q is %s, not a logout response", name, rec.Method)
	}
	var resp appwire.AuthLogoutResponse
	if err := json.Unmarshal(rec.Response, &resp); err != nil {
		t.Fatalf("decode fixture %q: %v", name, err)
	}
	return resp
}

// TestFormatAuthStatusSummarySpeaksTheRegistryVocabulary pins /auth's line
// for every credential source the hub can report.
func TestFormatAuthStatusSummarySpeaksTheRegistryVocabulary(t *testing.T) {
	fixtures := loadAuthWireFixtures(t)
	for _, tc := range []struct {
		fixture string
		want    string
	}{
		{"status/env", "openai auth: environment variable (OPENAI_API_KEY)"},
		{"status/store", "anthropic auth: stored API key"},
		{"status/api_key", "work auth: providers.toml"},
		{"status/credential_headers", "gateway auth: credential header"},
		{"status/oauth", "openai-codex auth: OAuth (bot@example.com)"},
		{"status/oauth-refreshable", "openai-codex auth: OAuth refreshable (bot@example.com)"},
		{"status/oauth-expired", "openai-codex auth: OAuth expired (bot@example.com)"},
		{"status/oauth-none", "openai-codex auth: not configured"},
		{"status/adc", "vertexish auth: application default credentials"},
		{"status/auth-none", "local auth: no credential required"},
		{"status/missing-key", "anthropic auth: not configured"},
		{"status/unsupported", `Auth is not supported for instance "not-a-provider".`},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			status := authStatusFromAppWire(hubAuthStatus(t, fixtures, tc.fixture))
			if got := formatAuthStatusSummary(status); got != tc.want {
				t.Fatalf("formatAuthStatusSummary(%s) = %q, want %q", tc.fixture, got, tc.want)
			}
		})
	}
}

// TestAuthSourceLabelCoversEveryHubSource is the exhaustiveness guard: an
// activeSource the hub sends that the TUI has no words for falls through to
// the raw wire value, which is a vocabulary gap, not a rendering.
func TestAuthSourceLabelCoversEveryHubSource(t *testing.T) {
	fixtures := loadAuthWireFixtures(t)
	seen := map[string]bool{}
	for name, rec := range fixtures {
		if rec.Method != appwire.MethodEvenerAuthStatus {
			continue
		}
		status := authStatusFromAppWire(hubAuthStatus(t, fixtures, name))
		if !status.Supported {
			continue
		}
		seen[status.ActiveSource] = true
		if got := authSourceLabel(status); got == status.ActiveSource {
			t.Errorf("authSourceLabel(%q) fell through to the raw wire value; the TUI has no words for it", status.ActiveSource)
		}
	}
	for _, source := range []string{"env:OPENAI_API_KEY", "store", "api_key", "credential_headers", "oauth", "adc", "none"} {
		if !seen[source] {
			t.Errorf("the fixture corpus no longer covers activeSource %q", source)
		}
	}
}

// TestHandleAuthLogoutReportsWhatTheHubDid: /logout used to announce an
// OpenAI OAuth sign-out whatever the hub had actually removed, including a
// stored API key it had just deleted. The message now follows the response.
func TestHandleAuthLogoutReportsWhatTheHubDid(t *testing.T) {
	fixtures := loadAuthWireFixtures(t)
	for _, tc := range []struct {
		fixture string
		want    string
	}{
		{
			fixture: "logout/oauth-removed",
			want:    "Removed the stored credential for openai-codex. openai-codex auth: not configured",
		},
		{
			fixture: "logout/stored-key-cleared",
			want:    "Removed the stored credential for anthropic. anthropic auth: not configured",
		},
		{
			fixture: "logout/nothing-to-remove",
			want:    "No stored credential to remove for openai-codex. openai-codex auth: not configured",
		},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			m := hubModel{session: newModel(nil)}
			got, _ := m.handleAuthLogout(hubAuthLogoutMsg{resp: hubAuthLogout(t, fixtures, tc.fixture)})
			if line := lastSessionSystemLine(t, got.(hubModel)); line != tc.want {
				t.Fatalf("logout message = %q, want %q", line, tc.want)
			}
		})
	}
}

// TestHandleAuthStatusAnnouncesEveryInstance: the old summary rendered only
// the literal provider "openai" and answered every other name with "Auth is
// not supported", though the hub reports Supported: true for each of them.
func TestHandleAuthStatusAnnouncesEveryInstance(t *testing.T) {
	fixtures := loadAuthWireFixtures(t)
	m := hubModel{session: newModel(nil)}
	got, _ := m.handleAuthStatus(hubAuthStatusMsg{status: hubAuthStatus(t, fixtures, "status/adc")})
	if line := lastSessionSystemLine(t, got.(hubModel)); line != "vertexish auth: application default credentials" {
		t.Fatalf("/auth vertexish = %q", line)
	}
}

// TestSessionAuthReadinessLabelUsesTheHubSource: the status line's dead
// "signed-out" case is gone; the hub's own source word is what shows.
func TestSessionAuthReadinessLabelUsesTheHubSource(t *testing.T) {
	fixtures := loadAuthWireFixtures(t)
	for _, tc := range []struct {
		fixture string
		want    string
	}{
		{"status/env", "auth: openai env:OPENAI_API_KEY"},
		{"status/oauth", "auth: openai-codex oauth"},
		{"status/missing-key", "auth: anthropic none"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			m := hubModel{authStatusSeen: true, authStatus: authStatusFromAppWire(hubAuthStatus(t, fixtures, tc.fixture))}
			if got := m.sessionAuthReadinessLabel(); got != tc.want {
				t.Fatalf("sessionAuthReadinessLabel(%s) = %q, want %q", tc.fixture, got, tc.want)
			}
		})
	}
}

// TestAuthSummaryUsesTheHubSource pins the session-status pane's one-line
// form, which shares the vocabulary rather than carrying a second copy.
func TestAuthSummaryUsesTheHubSource(t *testing.T) {
	fixtures := loadAuthWireFixtures(t)
	for _, tc := range []struct {
		fixture string
		want    string
	}{
		{"status/env", "openai environment variable (OPENAI_API_KEY)"},
		{"status/oauth", "openai-codex OAuth bot@example.com"},
		{"status/auth-none", "local no credential required"},
		{"status/missing-key", "anthropic not configured"},
		{"status/unsupported", "not-a-provider not supported"},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			if got := authSummary(hubAuthStatus(t, fixtures, tc.fixture)); got != tc.want {
				t.Fatalf("authSummary(%s) = %q, want %q", tc.fixture, got, tc.want)
			}
		})
	}
}

// TestHubAuthLoginEligibilityFollowsAuthModes: /login is offered for the
// instances whose authModes carry "oauth" and refused locally for the rest,
// rather than being sent to a hub that can only answer "OAuth is not
// supported".
func TestHubAuthLoginEligibilityFollowsAuthModes(t *testing.T) {
	fixtures := loadAuthWireFixtures(t)
	for _, tc := range []struct {
		fixture string
		target  string
		blocked bool
	}{
		{fixture: "status/oauth-none", target: "openai-codex", blocked: false},
		{fixture: "status/env", target: "openai", blocked: true},
		{fixture: "status/auth-none", target: "local", blocked: true},
		{fixture: "status/env", target: "some-other-instance", blocked: false},
	} {
		t.Run(tc.fixture+"/"+tc.target, func(t *testing.T) {
			m := hubModel{authStatusSeen: true, authStatus: authStatusFromAppWire(hubAuthStatus(t, fixtures, tc.fixture))}
			reason := m.hubAuthLoginBlockedReason(tc.target)
			if tc.blocked && reason == "" {
				t.Fatalf("/login %s should be refused: the hub reports authModes %v", tc.target, m.authStatus.AuthModes)
			}
			if !tc.blocked && reason != "" {
				t.Fatalf("/login %s refused with %q", tc.target, reason)
			}
		})
	}
}

// TestAuthProviderArgDefersToTheHub: an unqualified /auth, /login or /logout
// sends no provider, so the hub's normalizeAuthProvider picks the Codex
// instance. Naming "openai" here is what made /logout delete the platform
// API key stored under that name.
func TestAuthProviderArgDefersToTheHub(t *testing.T) {
	if got := authProviderArg("   "); got != "" {
		t.Fatalf("authProviderArg(blank) = %q, want the empty string", got)
	}
	if got := authProviderArg("  anthropic  extra"); got != "anthropic" {
		t.Fatalf("authProviderArg = %q, want anthropic", got)
	}
}

func lastSessionSystemLine(t *testing.T, m hubModel) string {
	t.Helper()
	for i := len(m.session.messages) - 1; i >= 0; i-- {
		if m.session.messages[i].Kind == transcript.MsgSystem {
			return strings.TrimSpace(m.session.messages[i].Text)
		}
	}
	t.Fatal("no system message was added")
	return ""
}
