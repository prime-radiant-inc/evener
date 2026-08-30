package provider

import (
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// EmbeddedRegistry is the process-wide embedded registry llm loads once
// (offline, no user layer, no cache, no environment): the resolver behind
// profiles built without a registry (NewOpenAIProfile in tests,
// CoreToolNames). It resolves every curated implicit id without a credential
// (spec §5.2) and is never mutated by a live listing, so sharing it is safe.
func EmbeddedRegistry() *registry.Registry { return llm.EmbeddedRegistry() }
