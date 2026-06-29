package codexlaunch

import "testing"

// FuzzParseCodexEndpoint drives the real ParseCodexEndpoint seam, which scrapes
// a codex app-server log line for its websocket endpoint via two paths: a JSON
// {"endpoint":...} object and a bare "ws://…" scan. The oracle is floor "no
// panic" plus validity of any accepted endpoint: a recognized endpoint must be
// non-empty and itself a valid ws URL (validCodexEndpoint agrees). It is NOT a
// re-parse fixed point — the JSON path trusts its field verbatim while the bare
// scan strips trailing ".,)" prose punctuation, so the two paths are asymmetric
// by design (e.g. JSON "ws://000)" stays, a bare "ws://000)" trims to "ws://000").
func FuzzParseCodexEndpoint(f *testing.F) {
	f.Add(`{"endpoint":"ws://127.0.0.1:1234"}`)
	f.Add(`listening on ws://127.0.0.1:8080/app`)
	f.Add(`starting (ws://host:9/path).`)
	f.Add(`{"endpoint":"http://nope"}`)
	f.Add(`ws://`)
	f.Add(`no endpoint here`)
	f.Add(``)
	f.Add(`ws://a,ws://b`)
	f.Add(`{"endpoint":""}`)

	f.Fuzz(func(t *testing.T, line string) {
		endpoint, ok := ParseCodexEndpoint(line)
		if !ok {
			if endpoint != "" {
				t.Fatalf("ParseCodexEndpoint returned ok=false but a non-empty endpoint %q\n line=%q", endpoint, line)
			}
			return
		}
		if endpoint == "" {
			t.Fatalf("ParseCodexEndpoint returned ok=true with an empty endpoint\n line=%q", line)
		}
		if _, valid := validCodexEndpoint(endpoint); !valid {
			t.Fatalf("accepted endpoint is not itself valid: %q\n line=%q", endpoint, line)
		}
	})
}
