package registry

import "testing"

// One config exercising the command-mint predicate from every side: a
// provider with no command anywhere, an api_key command under a scheme
// that sends it, a credential-header command, an api_key command under a
// never-send scheme whose row resolution fails on a disabled alias, and
// glob rows pinning never-send and sendable schemes.
const mintsConfig = `
[providers.plain]
protocol = "openai-chat"
base_url = "https://x/v1"
[providers.keycmd]
protocol = "openai-chat"
base_url = "https://x/v1"
api_key = "$(true)"
[providers.headercmd]
protocol = "openai-chat"
base_url = "https://x/v1"
credential_headers = { X-Auth = "$(true)" }
[providers.rowfail]
protocol = "openai-chat"
base_url = "https://x/v1"
auth = "none"
api_key = "$(true)"
[providers.rowfail.models.t]
disabled = true
[providers.rowfail.models.a]
alias_of = "t"
[providers.globnone]
protocol = "openai-chat"
base_url = "https://x/v1"
auth = "none"
api_key = "$(true)"
[providers.globsend]
protocol = "openai-chat"
base_url = "https://x/v1"
auth = "none"
api_key = "$(true)"
[providers.globsend.models."m*"]
auth = "header"
auth_header = "x-api-key"
[models."x*"]
auth = "none"
`

func TestLaunchMintsCredentialCommand(t *testing.T) {
	t.Parallel()
	r := fixtureLoad(t, nil, mintsConfig)
	for _, tc := range []struct {
		instance string
		want     bool
	}{
		{"plain", false},    // no command material anywhere
		{"keycmd", true},    // the listing's own transport would run it
		{"headercmd", true}, // a credential header goes on the wire whatever the scheme
		{"rowfail", false},  // never-send scheme; the one row that would is a disabled alias
		{"globnone", false}, // a never-send glob pins nothing
		{"globsend", true},  // a sendable-auth glob can make an unseen row consume the key
		{"no-such", false},  // an unknown instance vouches for nothing
	} {
		if got := r.LaunchMintsCredentialCommand(tc.instance); got != tc.want {
			t.Errorf("LaunchMintsCredentialCommand(%q) = %v, want %v", tc.instance, got, tc.want)
		}
	}
}
