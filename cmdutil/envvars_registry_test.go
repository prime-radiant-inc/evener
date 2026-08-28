package cmdutil

import (
	"sort"
	"sync"
	"testing"

	"primeradiant.com/evener/envvars"
	"primeradiant.com/evener/llm/registry"
)

// TestEveryRegistryEnvVarIsDeclared pins that envvars declares every
// environment variable the provider registry reads: an instance's
// api_key_env names, the vars_env names its base URL is built from, and the
// host-rule variables the loader consults directly.
//
// The scrub lists that isolate tests from a developer's environment are
// derived from envvars.All() (cmd/evener/testmain_test.go and its hub
// sibling), and `evener doctor` names variables from the same roster. A
// variable the registry reads but envvars does not declare is therefore one
// no test clears and no report mentions — a stray value on a developer
// machine silently configures an instance.
//
// The registry is loaded with an environment that answers every lookup, so
// every implicit provider resolves a credential and every base URL
// substitutes: what the recorder collects is the whole set the registry can
// read, not just the subset a bare machine happens to set.
func TestEveryRegistryEnvVarIsDeclared(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}

	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(name string) (string, bool) {
			mu.Lock()
			seen[name] = true
			mu.Unlock()
			return "set", true
		}),
	)
	if err != nil {
		t.Fatalf("registry.Load: %v", err)
	}
	// Instances() resolves each record's credential and base URL, which is
	// where api_key_env and vars_env are read.
	if len(r.Instances()) == 0 {
		t.Fatal("no instances resolved; the recorder would prove nothing")
	}

	var missing []string
	mu.Lock()
	for name := range seen {
		if _, ok := envvars.Find(name); !ok {
			missing = append(missing, name)
		}
	}
	mu.Unlock()
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the registry reads %d variables envvars does not declare: %v", len(missing), missing)
	}
}
