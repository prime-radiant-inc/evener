// Command modelsdevsample cuts the registry's checked-in test fixture out of
// a full models.dev api.json. The fixture is the 40 providers below, chosen
// to cover every npm value in the converter table, per-model provider
// overrides (npm, api, shape), both interleaved shapes, every
// reasoning_options combination, limit.input, @default ids, cost tiers,
// hidden providers, providers with no api, non-Claude Bedrock rows, the
// Vertex openai/*-maas rows, mixed-case ids, and rows with mapped, unmapped,
// and absent family. Rerun it after refreshing the snapshot when a test
// needs a row the fixture lacks:
//
//	curl -sL https://models.dev/api.json -o /tmp/api.json
//	go run ./llm/registry/internal/modelsdevsample -in /tmp/api.json -out llm/registry/testdata/models.dev.sample.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
)

var keep = []string{
	"anthropic", "openai", "google", "groq", "xai", "cerebras", "mistral", "togetherai",
	"deepseek", "zai", "zai-coding-plan", "zhipuai", "zhipuai-coding-plan", "openrouter",
	"moonshotai", "moonshotai-cn", "kimi-for-coding", "minimax", "minimax-cn",
	"azure", "azure-cognitive-services", "amazon-bedrock", "google-vertex", "google-vertex-anthropic",
	"ollama-cloud", "cohere", "watsonx", "deepinfra", "perplexity", "vercel", "huggingface",
	"fireworks-ai", "github-copilot", "opencode", "nvidia", "crof", "zenifra", "hpc-ai",
	"cloudflare-workers-ai", "venice",
}

func main() {
	in := flag.String("in", "", "path to a full models.dev api.json")
	out := flag.String("out", "", "path to write the fixture")
	flag.Parse()
	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: modelsdevsample -in api.json -out fixture.json")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		os.Exit(1)
	}
	sort.Strings(keep)
	subset := map[string]json.RawMessage{}
	for _, id := range keep {
		v, ok := all[id]
		if !ok {
			fmt.Fprintf(os.Stderr, "warning: provider %q not in %s\n", id, *in)
			continue
		}
		subset[id] = v
	}
	data, err := json.MarshalIndent(subset, "", " ")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d providers to %s\n", len(subset), *out)
}
