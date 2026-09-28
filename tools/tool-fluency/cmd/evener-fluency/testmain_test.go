package main

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/sandbox/sandboxtest"
	"primeradiant.com/evener/llm/registry"
)

// TestMain resolves models on the embedded registry, so no default test reads
// a developer's configured providers. It also collects the session scratch and
// temp containers the sessions these tests run retain at close, which only the
// 24h crashed-scratch sweep would otherwise reclaim, and removes them when the
// run ends.
func TestMain(m *testing.M) {
	runnerLoadRegistry = func() (*registry.Registry, error) { return provider.EmbeddedRegistry(), nil }
	os.Exit(sandboxtest.Run(m, "evener-fluency-test-"))
}
