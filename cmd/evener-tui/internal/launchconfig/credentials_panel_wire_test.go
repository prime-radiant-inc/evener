package launchconfig

// The credentials panel is one of three clients that key on InstanceEntry's
// activeSource/credentialRequired vocabulary (spec §11.3); the other two are
// the React pane and the TUI's /auth line. Every entry here is decoded from
// cmd/evener-hub/testdata/authwire/responses.json, which the hub's own
// TestAuthWireFixturesMatchTheHubHandler produces by driving the real
// evener/instance/list handler and re-verifies on every run.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// authWireFixturePath is the hub's own corpus; the file belongs to the
// producer, and a copy here would be one more thing to drift.
const authWireFixturePath = "../../../evener-hub/testdata/authwire/responses.json"

type authWireFixture struct {
	Case     string          `json:"case"`
	Method   string          `json:"method"`
	Field    string          `json:"field,omitempty"`
	Response json.RawMessage `json:"response"`
}

// hubInstanceEntries decodes the recorded evener/instance/list rows.
func hubInstanceEntries(t *testing.T) []appwire.InstanceEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(authWireFixturePath))
	if err != nil {
		t.Fatalf("read %s: %v", authWireFixturePath, err)
	}
	var records []authWireFixture
	if err := json.Unmarshal(data, &records); err != nil {
		t.Fatalf("decode %s: %v", authWireFixturePath, err)
	}
	for _, rec := range records {
		if rec.Method != appwire.MethodEvenerInstanceList || rec.Field != "instances" {
			continue
		}
		var entries []appwire.InstanceEntry
		if err := json.Unmarshal(rec.Response, &entries); err != nil {
			t.Fatalf("decode fixture %q: %v", rec.Case, err)
		}
		if len(entries) == 0 {
			t.Fatalf("fixture %q carries no instances", rec.Case)
		}
		return entries
	}
	t.Fatalf("no evener/instance/list fixture in %s", authWireFixturePath)
	return nil
}

// TestCredentialBadgeMatchesEveryHubSource pins the badge for each row the
// hub actually sends: the badge text is the wire's own source word, except
// for the one case the wire cannot say in a word — nothing resolved, and
// whether that is a gap or the design depends on credentialRequired.
func TestCredentialBadgeMatchesEveryHubSource(t *testing.T) {
	want := map[string]string{
		"anthropic":    "STORE",
		"openai-codex": "OAUTH",
		"openai":       "ENV:OPENAI_API_KEY",
		"ollama":       "OPTIONAL",
		"authored":     "API_KEY",
		"gateway":      "OPTIONAL",
		"headered":     "CREDENTIAL_HEADERS",
		"vertexish":    "ADC",
		"unkeyed":      "NONE",
	}
	panel := CredentialsPanel{}
	seen := map[string]bool{}
	for _, inst := range hubInstanceEntries(t) {
		expected, ok := want[inst.Name]
		if !ok {
			t.Errorf("the hub now sends an instance %q the badge table does not cover", inst.Name)
			continue
		}
		seen[inst.Name] = true
		if got := panel.credentialBadge(inst); !strings.Contains(got, expected) {
			t.Errorf("credentialBadge(%s, activeSource %q) = %q, want it to carry %q", inst.Name, inst.ActiveSource, got, expected)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("the fixture corpus no longer covers instance %q", name)
		}
	}
}

// TestCredentialBadgeSeparatesMissingFromKeyless: "none" means two different
// things, and the hub distinguishes them with credentialRequired — an
// auth-none or optional-bearer instance is not missing anything.
func TestCredentialBadgeSeparatesMissingFromKeyless(t *testing.T) {
	var required, keyless *appwire.InstanceEntry
	for _, inst := range hubInstanceEntries(t) {
		if inst.ActiveSource != "none" {
			continue
		}
		if inst.CredentialRequired {
			required = &inst
		} else {
			keyless = &inst
		}
	}
	if keyless == nil {
		t.Fatal("the corpus no longer carries an instance that wants no credential")
	}
	panel := CredentialsPanel{}
	if got := panel.credentialBadge(*keyless); !strings.Contains(got, "OPTIONAL") {
		t.Fatalf("credentialBadge(%s) = %q, want OPTIONAL", keyless.Name, got)
	}
	if required == nil {
		t.Fatal("the corpus no longer carries an instance whose credential is genuinely missing")
	}
	if got := panel.credentialBadge(*required); !strings.Contains(got, "NONE") {
		t.Fatalf("credentialBadge(%s) = %q, want NONE", required.Name, got)
	}
}
