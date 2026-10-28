package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSampleKeepsOnlyListedProviders runs the sampler against a stand-in
// catalog holding two keep ids and one unlisted provider. The fixture the
// tool writes must carry exactly the listed providers that were present, and
// every listed provider the input lacked must be named on stderr as skipped.
func TestSampleKeepsOnlyListedProviders(t *testing.T) {
	// Not parallel: main registers its flags on the process-wide
	// flag.CommandLine and swaps os.Args, so it runs once per test process.
	in := filepath.Join(t.TempDir(), "api.json")
	out := filepath.Join(t.TempDir(), "fixture.json")
	catalog, err := json.Marshal(map[string]json.RawMessage{
		"anthropic": json.RawMessage(`{"models":{}}`),
		"openai":    json.RawMessage(`{"models":{}}`),
		"not-kept":  json.RawMessage(`{"models":{}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in, catalog, 0o644); err != nil {
		t.Fatal(err)
	}

	oldArgs, oldFlags := os.Args, flag.CommandLine
	oldStdout, oldStderr := os.Stdout, os.Stderr
	// main registers -in/-out on flag.CommandLine; give it a fresh set so
	// the call is repeat-safe and leaves the process's set alone.
	flag.CommandLine = flag.NewFlagSet("modelsdevsample", flag.ExitOnError)
	// stdout only carries the one-line summary; keep it out of the test log.
	_, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"modelsdevsample", "-in", in, "-out", out}
	os.Stdout, os.Stderr = outW, errW
	main()
	_ = outW.Close()
	_ = errW.Close()
	os.Args, flag.CommandLine = oldArgs, oldFlags
	os.Stdout, os.Stderr = oldStdout, oldStderr
	stderr, err := io.ReadAll(errR)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("fixture is not a JSON object: %v\n%s", err, data)
	}
	for _, kept := range []string{"anthropic", "openai"} {
		if _, ok := fixture[kept]; !ok {
			t.Errorf("fixture is missing listed provider %q", kept)
		}
	}
	if _, ok := fixture["not-kept"]; ok {
		t.Errorf("fixture carries unlisted provider %q", "not-kept")
	}
	if len(fixture) != 2 {
		t.Errorf("fixture holds %d providers, want the 2 listed ones present", len(fixture))
	}

	for _, id := range keep {
		if id == "anthropic" || id == "openai" {
			continue
		}
		if !strings.Contains(string(stderr), `"`+id+`"`) {
			t.Errorf("missing listed provider %q was not named on stderr:\n%s", id, stderr)
		}
	}
}
