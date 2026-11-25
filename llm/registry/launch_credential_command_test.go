package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

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

// rosterGatewayCommandConfig is one Copilot instance whose default model
// generates through a command-backed API-key gateway while discovery stays on
// the account's configured Copilot transport.
const rosterGatewayCommandConfig = `
[providers.work]
base = "github-copilot"
api_key = '''$(sh -c 'printf x >> %s; printf gw-secret')'''
default_model = "claude-x"
[providers.work.models."claude-x"]
auth = "bearer"
base_url = "https://gateway.example.com/v1"
`

// TestLaunchMintsCredentialCommandIgnoresGenerationGateway pins the discovery
// half of the gateway boundary: the roster fetch resolves the configured
// Copilot transport, where no api_key travels, so a default row's gateway
// command is the launch's to run and must not make discovery look mintable.
// Generation through that gateway still materializes the command.
func TestLaunchMintsCredentialCommandIgnoresGenerationGateway(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "cred-ran")
	state := t.TempDir()
	saveCopilotRecordForTest(t, state, "work", healthyCopilotRecord("work"))
	r := fixtureLoad(t, nil, fmt.Sprintf(rosterGatewayCommandConfig, marker), WithStateRoot(state))

	if r.LaunchMintsCredentialCommand("work") {
		t.Fatal("the launch gate judged discovery through the default row's generation gateway command")
	}
	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("discovery executed the generation command %d time(s), want 0", len(data))
	}
	gen, err := r.Resolve("work/claude-x")
	if err != nil {
		t.Fatalf("Resolve(work/claude-x): %v", err)
	}
	if gen.Transport.Auth != AuthBearer || gen.Transport.BaseURL != "https://gateway.example.com/v1" {
		t.Fatalf("generation transport = %+v, want the gateway bearer transport", gen.Transport)
	}
	if gen.Credential.Value != "gw-secret" {
		t.Fatalf("generation credential = %+v, want the gateway command's key", gen.Credential)
	}
	if data, err := os.ReadFile(marker); err != nil || len(data) != 1 {
		t.Fatalf("generation ran the command %d time(s) (err=%v), want 1", len(data), err)
	}
}
