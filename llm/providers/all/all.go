// Package all performs every side-effect protocol and authenticator
// registration import in one place. Binaries that need the whole set import
// this package instead of listing each blank import individually.
package all

import (
	_ "primeradiant.com/evener/llm/providers/anthropic"       // register the anthropic protocol
	_ "primeradiant.com/evener/llm/providers/chatcompletions" // register the openai-chat protocol
	_ "primeradiant.com/evener/llm/providers/google"          // register the google protocol
	_ "primeradiant.com/evener/llm/providers/responses"       // register the openai-responses protocol
	_ "primeradiant.com/evener/llm/providers/tokenauth"       // register the gcp-adc and oauth-openai-codex authenticators
)
