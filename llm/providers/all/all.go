// Package all performs every side-effect provider registration import in
// one place. Binaries that need all registered providers import this package
// instead of listing each provider's blank import individually.
package all

import (
	_ "primeradiant.com/evener/llm/providers/anthropic"            // register anthropic provider adapter
	_ "primeradiant.com/evener/llm/providers/glm"                  // register glm provider adapter
	_ "primeradiant.com/evener/llm/providers/google"               // register google/gemini provider adapter
	_ "primeradiant.com/evener/llm/providers/kimi"                 // register kimi provider adapter
	_ "primeradiant.com/evener/llm/providers/kimi_anthropic"       // register kimi-anthropic provider adapter
	_ "primeradiant.com/evener/llm/providers/minimax"              // register minimax provider adapter
	_ "primeradiant.com/evener/llm/providers/ollama"               // register ollama provider adapter
	_ "primeradiant.com/evener/llm/providers/openai"               // register openai provider adapter
	_ "primeradiant.com/evener/llm/providers/openaicompat"         // register openai-compatible provider adapter
	_ "primeradiant.com/evener/llm/providers/openrouter"           // register openrouter provider adapter
	_ "primeradiant.com/evener/llm/providers/openrouter_anthropic" // register openrouter-anthropic provider adapter
)
