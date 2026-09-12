// Package tokenauth holds the two auth schemes that mint bearer tokens from
// a local credential store instead of sending a configured key: gcp-adc
// (Google application-default credentials) and oauth-openai-codex (the
// per-instance OAuth record written by `evener openai login`). The four
// trivial schemes live in package llm.
package tokenauth

import (
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// DefaultGCPADC and DefaultCodex are the registered instances; tests that
// drive real protocol code through these schemes set their seams.
var (
	DefaultGCPADC = &GCPADC{}
	DefaultCodex  = &Codex{}
)

func init() {
	llm.RegisterAuthenticator(registry.AuthGCPADC, DefaultGCPADC)
	llm.RegisterAuthenticator(registry.AuthOAuthOpenAICodex, DefaultCodex)
}
