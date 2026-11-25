// Package tokenauth holds the auth schemes that mint bearer tokens from a
// local credential store instead of sending a configured key: gcp-adc
// (Google application-default credentials), oauth-openai-codex (the
// per-instance OAuth record written by `evener openai login`), and
// oauth-github-copilot (the record written by `evener github-copilot login`).
// The four trivial schemes live in package llm.
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
	// DefaultCopilot serves oauth-github-copilot. cmdutil binds its StateDir
	// to the registry state root; concurrent launches use ScopedCopilot.
	DefaultCopilot = &Copilot{}
)

func init() {
	llm.RegisterAuthenticator(registry.AuthGCPADC, DefaultGCPADC)
	llm.RegisterAuthenticator(registry.AuthOAuthOpenAICodex, DefaultCodex)
	llm.RegisterAuthenticator(registry.AuthOAuthGitHubCopilot, DefaultCopilot)
}
