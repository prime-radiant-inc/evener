// Command providers-toml writes the providers.toml an e2e harness hands to a
// disposable hub. It is the shell harnesses' entry point to the one writer in
// the parent package (#4445).
package main

import (
	"flag"
	"fmt"
	"os"

	"primeradiant.com/evener/test/e2e/providers"
)

func main() {
	out := flag.String("out", "", "providers.toml path to write")
	name := flag.String("default", "", "default provider name and [providers.<name>] key")
	baseURL := flag.String("base-url", "", "fake backend base URL")
	apiKey := flag.String("api-key", "", "api_key to write (optional)")
	affinity := flag.Bool("session-affinity-headers", false, "set session_affinity_headers = true")
	window := flag.Int("context-window", 0, "context_window to write (optional)")
	flag.Parse()
	if *out == "" || *name == "" || *baseURL == "" {
		fmt.Fprintln(os.Stderr, "providers-toml: --out, --default and --base-url are required")
		os.Exit(2)
	}
	body, err := providers.Render(providers.Config{
		Name:                   *name,
		BaseURL:                *baseURL,
		APIKey:                 *apiKey,
		SessionAffinityHeaders: *affinity,
		ContextWindow:          *window,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "providers-toml: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, body, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "providers-toml: %v\n", err)
		os.Exit(1)
	}
}
