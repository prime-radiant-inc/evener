package registry

import "testing"

// Credential-header drop rules, judged through a full resolve: a raw-empty
// entry is the authored removal (silently skipped), an entry whose expansion
// is empty warns, and an entry that assembles to nothing but an auth scheme
// word warns — only the fully-materialized header reaches the wire.
const credHeadersConfig = `
[providers.p]
protocol = "openai-chat"
base_url = "https://x/v1"
api_key = "$K"
credential_headers = { X-Empty = "", X-Full = "$FULL", X-Gone = "$GONE", X-Scheme = "${SCHEME:-Bearer}" }
[providers.p.models.m]
family = "mm"
`

func TestResolveCredentialHeaderDropRules(t *testing.T) {
	t.Parallel()
	env := map[string]string{"K": "k", "FULL": "v", "GONE": ""}
	r := fixtureLoad(t, env, credHeadersConfig)
	res, err := r.Resolve("p/m")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"X-Full": "v"}
	if len(res.CredentialHeaders) != 1 || res.CredentialHeaders["X-Full"] != "v" {
		t.Fatalf("CredentialHeaders = %v, want %v", res.CredentialHeaders, want)
	}
	if len(res.Warnings) < 2 {
		t.Fatalf("the empty and scheme-word expansions must warn: %v", res.Warnings)
	}
}

// The presence view names the headers a launch would send under the same
// drop rules: the raw-empty removal names nothing.
func TestResolveInstancePresenceNamesSentHeaders(t *testing.T) {
	t.Parallel()
	env := map[string]string{"K": "k", "FULL": "v", "GONE": ""}
	r := fixtureLoad(t, env, credHeadersConfig)
	res, err := r.ResolveInstancePresence("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CredentialHeaderNames) != 1 || res.CredentialHeaderNames[0] != "X-Full" {
		t.Fatalf("CredentialHeaderNames = %v, want [X-Full]", res.CredentialHeaderNames)
	}
}

// A raw-empty credential header never rotates the auth fingerprint: it is
// the authored removal, on any header name.
func TestAuthFingerprintIgnoresRawEmptyHeader(t *testing.T) {
	t.Parallel()
	env := map[string]string{"K": "k", "FULL": "v", "GONE": ""}
	mk := func(t *testing.T, withEmpty bool) string {
		t.Helper()
		config := `
[providers.p]
protocol = "openai-chat"
base_url = "https://x/v1"
api_key = "$K"
credential_headers = { X-Full = "$FULL", X-Gone = "$GONE", X-Scheme = "${SCHEME:-Bearer}" }
[providers.p.models.m]
family = "mm"
`
		if withEmpty {
			config = `
[providers.p]
protocol = "openai-chat"
base_url = "https://x/v1"
api_key = "$K"
credential_headers = { X-Empty = "", X-Full = "$FULL", X-Gone = "$GONE", X-Scheme = "${SCHEME:-Bearer}" }
[providers.p.models.m]
family = "mm"
`
		}
		r := fixtureLoad(t, env, config)
		fp, ok := r.AuthFingerprint("p")
		if !ok || fp == "" {
			t.Fatalf("AuthFingerprint(p) = (%q, %v)", fp, ok)
		}
		return fp
	}
	if mk(t, true) != mk(t, false) {
		t.Fatal("a raw-empty credential header must not rotate the fingerprint")
	}
}

// A present-but-empty auth-slot header is the authored removal: the api_key
// it would have overridden supplies the credential, while a filled one
// owns the slot.
func TestEmptyAuthSlotHeaderFallsBackToAPIKey(t *testing.T) {
	t.Parallel()
	const config = `
[providers.gw]
protocol = "openai-chat"
base_url = "https://x/v1"
api_key = "$K"
credential_headers = { Authorization = "" }
[providers.gw.models.m]
family = "mm"
`
	r := fixtureLoad(t, map[string]string{"K": "k"}, config)
	res, err := r.Resolve("gw/m")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Value != "k" {
		t.Fatalf("an empty auth-slot header must leave the api_key supplying the credential, got %q", res.Credential.Value)
	}

	const filled = `
[providers.gw]
protocol = "openai-chat"
base_url = "https://x/v1"
api_key = "$K"
credential_headers = { Authorization = "$OTHER" }
[providers.gw.models.m]
family = "mm"
`
	r2 := fixtureLoad(t, map[string]string{"K": "k", "OTHER": "other"}, filled)
	res2, err := r2.Resolve("gw/m")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Credential.Value != "other" {
		t.Fatalf("a filled auth-slot header must own the slot, got %q", res2.Credential.Value)
	}
}
