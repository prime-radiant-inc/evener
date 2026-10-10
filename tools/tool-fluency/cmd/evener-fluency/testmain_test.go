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
//
// The fake evener scripts the tests write print the same line for every
// invocation, --help included, so TestMain also treats every binary as
// supporting --enabled-plugins; TestEvenerSupportsEnabledPlugins* drive the
// real check.
func TestMain(m *testing.M) {
	runnerLoadRegistry = func() (*registry.Registry, error) { return provider.EmbeddedRegistry(), nil }
	evenerSupportsEnabledPlugins = func(string) (bool, error) { return true, nil }
	os.Exit(sandboxtest.Run(m, "evener-fluency-test-"))
}
