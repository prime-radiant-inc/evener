// Package difftest hosts the cross-provider differential fuzz oracle.
//
// It generates one canonical *logical* model response (text, tool calls,
// reasoning, a usage triple, a finish class), encodes that logical response
// into each provider's SSE wire format, decodes it back through the REAL
// provider adapter's public streaming path, and asserts the decoded
// llm.Response values are equivalent across providers — modulo an explicit,
// documented allow-list of legitimately provider-specific fields.
//
// This is a differential oracle (research §4): it catches adapter-specific
// decode drift that a single provider's metamorphic oracle cannot see, because
// the bug only appears as a *disagreement* between two adapters that must agree.
//
// The package intentionally contains only this doc file outside of _test.go:
// all logic is test-only. The doc file exists so `go build ./...` has a
// non-test Go file to compile for the package.
package difftest
