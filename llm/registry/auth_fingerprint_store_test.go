package registry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// adcAvailable resolves without the network: a GOOGLE_APPLICATION_CREDENTIALS
// pointing at a real file is available; one pointing nowhere is not.
func TestADCAvailableFromFile(t *testing.T) {
	t.Parallel()
	cred := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(cred, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": cred, "HOME": t.TempDir()}
	if !adcAvailable(mapEnv(env)) {
		t.Fatal("an existing GOOGLE_APPLICATION_CREDENTIALS file must make ADC available")
	}
	env["GOOGLE_APPLICATION_CREDENTIALS"] = filepath.Join(t.TempDir(), "missing.json")
	if adcAvailable(mapEnv(env)) {
		t.Fatal("a missing GOOGLE_APPLICATION_CREDENTIALS file must not make ADC available")
	}
}

func TestAuthFingerprintUnknownInstance(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, nil, "")
	if fp, ok := r.AuthFingerprint("no-such-instance"); ok || fp != "" {
		t.Fatalf("AuthFingerprint(unknown) = (%q, %v), want (\"\", false)", fp, ok)
	}
}

// A stored credential JSON is the material that rotates a gcp-adc
// instance's identity: changing the stored document must change the
// fingerprint, and an unchanged store must keep it stable.
func TestAuthFingerprintGCADCStoreRotates(t *testing.T) {
	t.Parallel()
	first := `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"one"}`
	second := `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"two"}`
	mk := func(t *testing.T, stored string) string {
		t.Helper()
		r := fixtureLoad(t, map[string]string{"HOME": t.TempDir()}, vertexUserInstanceToml, WithCredentials(fakeCreds{"vertex": stored}))
		fp, ok := r.AuthFingerprint("vertex")
		if !ok || fp == "" {
			t.Fatalf("AuthFingerprint(vertex) = (%q, %v)", fp, ok)
		}
		return fp
	}
	fpOne, fpTwo := mk(t, first), mk(t, first)
	if fpOne != fpTwo {
		t.Fatal("an unchanged stored credential must keep the fingerprint stable")
	}
	if fpOne == mk(t, second) {
		t.Fatal("a rotated stored credential must rotate the fingerprint")
	}
}

// An api_key that expands to nothing is inert material: its value edits must
// not rotate the identity, while a real key under the same name does.
func TestAuthFingerprintAPIKeyInert(t *testing.T) {
	t.Parallel()
	const config = "[providers.gw2]\nprotocol = \"openai-chat\"\nbase_url = \"https://x/v1\"\napi_key = \"$INERT\"\n"
	mk := func(t *testing.T, env map[string]string) string {
		t.Helper()
		r := fixtureLoad(t, env, config)
		fp, ok := r.AuthFingerprint("gw2")
		if !ok || fp == "" {
			t.Fatalf("AuthFingerprint(gw2) = (%q, %v)", fp, ok)
		}
		return fp
	}
	empty := mk(t, map[string]string{"INERT": ""})
	if empty != mk(t, map[string]string{"INERT": ""}) {
		t.Fatal("an inert api_key must hash stably")
	}
	if empty == mk(t, map[string]string{"INERT": "sk-live-real"}) {
		t.Fatal("a real api_key under the same variable must rotate the fingerprint")
	}
}

// With no auth-slot header and no api_key, the credential store is the
// terminal material: its rotation is the identity's rotation.
func TestAuthFingerprintStoreIsTerminalMaterial(t *testing.T) {
	t.Parallel()
	const config = "[providers.gw3]\nprotocol = \"openai-chat\"\nbase_url = \"https://x/v1\"\napi_key_env = [\"GW3_KEY\"]\n"
	mk := func(t *testing.T, stored string) string {
		t.Helper()
		r := fixtureLoad(t, nil, config, WithCredentials(fakeCreds{"gw3": stored}))
		fp, ok := r.AuthFingerprint("gw3")
		if !ok || fp == "" {
			t.Fatalf("AuthFingerprint(gw3) = (%q, %v)", fp, ok)
		}
		return fp
	}
	keyOne, keyTwo := mk(t, "sk-one"), mk(t, "sk-one")
	if keyOne == mk(t, "sk-two") {
		t.Fatal("a rotated stored key must rotate the fingerprint")
	}
	if keyOne != keyTwo {
		t.Fatal("an unchanged stored key must keep the fingerprint stable")
	}
}

// authHeaderKey: the exact-case key wins, a present-but-empty one is the
// authored removal and resolves as absence, and among case variants the
// lexicographically first is chosen.
func TestAuthHeaderKeyChoices(t *testing.T) {
	t.Parallel()
	if got := authHeaderKey(map[string]string{"Authorization": ""}, "Authorization"); got != "" {
		t.Fatalf("an empty exact-case entry is absence, got %q", got)
	}
	if got := authHeaderKey(map[string]string{"Authorization": "x"}, "Authorization"); got != "Authorization" {
		t.Fatalf("the exact-case key must win, got %q", got)
	}
	if got := authHeaderKey(map[string]string{"authorization": "x", "AUTHORIZATION": "y"}, "Authorization"); got != "AUTHORIZATION" {
		t.Fatalf("among case variants the first by byte order must win, got %q", got)
	}
	if got := authHeaderKey(map[string]string{"authorization": ""}, "Authorization"); got != "" {
		t.Fatalf("an empty case-variant entry must not supply the slot, got %q", got)
	}
}

func TestResolveGateCredential(t *testing.T) {
	t.Parallel()
	const config = "[providers.gw4]\nprotocol = \"openai-chat\"\nbase_url = \"https://x/v1\"\n[providers.gw4.models.m]\nfamily = \"mm\"\n"
	t.Run("unknown instance", func(t *testing.T) {
		t.Parallel()
		r := fixtureLoad(t, nil, config)
		_, err := r.ResolveGateCredential("no-such", "")
		if !errors.Is(err, ErrUnknownInstance) {
			t.Fatalf("ResolveGateCredential(unknown) err = %v, want ErrUnknownInstance", err)
		}
	})
	t.Run("instance-prefixed model is stripped", func(t *testing.T) {
		t.Parallel()
		r := fixtureLoad(t, nil, config)
		bare, err := r.ResolveGateCredential("gw4", "m")
		if err != nil {
			t.Fatal(err)
		}
		prefixed, err := r.ResolveGateCredential("gw4", "gw4/m")
		if err != nil {
			t.Fatal(err)
		}
		if bare.Protocol != prefixed.Protocol || bare.Transport.BaseURL != prefixed.Transport.BaseURL {
			t.Fatalf("prefixed form drifted: %+v vs %+v", bare, prefixed)
		}
	})
}

func TestHasInstance(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, nil, "[providers.gw5]\nprotocol = \"openai-chat\"\nbase_url = \"https://x/v1\"\n")
	if !r.HasInstance("gw5") {
		t.Fatal("a configured provider is an instance")
	}
	if r.HasInstance("no-such") {
		t.Fatal("an unknown name is not an instance")
	}
}

// With no auth directory under the state root there are no stray records.
func TestStrayOAuthRecordsWithoutAuthDir(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, nil, "")
	if got := r.StrayOAuthRecords(); got != nil {
		t.Fatalf("StrayOAuthRecords = %v, want nil", got)
	}
}

// A default naming an explicit instance loads without warnings and answers
// DefaultInstance directly.
func TestDefaultNamingExplicitInstance(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, nil, "default = \"gw6\"\n[providers.gw6]\nprotocol = \"openai-chat\"\nbase_url = \"https://x/v1\"\n")
	name, warnings, err := r.DefaultInstance()
	if err != nil {
		t.Fatal(err)
	}
	if name != "gw6" || len(warnings) != 0 {
		t.Fatalf("DefaultInstance = (%q, %v), want (gw6, no warnings)", name, warnings)
	}
}
