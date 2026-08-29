// Command snapshotreport prints what the refresh script needs a human to
// look at after the embedded models.dev snapshot changes (spec §6.4):
// overlay rows upstream now covers, dangling overlay aliases, upstream rows
// whose output cap is at or above their context window, and overlay pins
// whose upstream value changed. Run by scripts/ops/refresh-model-catalog.sh;
// safe to run by hand: go run ./llm/registry/internal/snapshotreport
package main

import (
	"fmt"
	"os"
	"sort"

	"primeradiant.com/evener/llm/registry"
)

func main() {
	raw, meta, err := registry.EmbeddedSnapshot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	upstream, err := registry.FromModelsDev(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	overlay, err := registry.ParseOverlay(registry.EmbeddedOverlay())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	byID := map[string]registry.Provider{}
	for _, p := range upstream {
		byID[p.ID] = p
	}
	fmt.Printf("snapshot fetched %s (etag %s)\n", meta.FetchedAt.Format("2006-01-02"), meta.Etag)

	fmt.Println("\n== overlay rows upstream now covers (consider deleting the overlay row)")
	for _, pid := range sortedKeys(overlay.Providers) {
		for _, mid := range sortedKeys(overlay.Providers[pid].Models) {
			if up, ok := byID[pid]; ok {
				if _, ok := up.Models[mid]; ok && overlay.Providers[pid].Models[mid].AliasOf != "" {
					fmt.Printf("  %s/%s (overlay alias row; upstream has the id)\n", pid, mid)
				}
			}
		}
	}

	fmt.Println("\n== dangling overlay aliases")
	r, err := registry.Load(registry.WithNoUserLayer(), registry.WithOffline(true), registry.WithEnv(func(string) (string, bool) { return "", false }), registry.WithStateRoot(os.TempDir()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, w := range r.Warnings() {
		fmt.Println("  " + w)
	}

	fmt.Println("\n== upstream rows with output cap >= context window (cleared at resolve time; listed for awareness)")
	junk := 0
	for _, p := range upstream {
		for _, mid := range sortedKeys(p.Models) {
			c := p.Models[mid].Caps
			if c.ContextWindow != nil && c.MaxOutputTokens != nil && *c.MaxOutputTokens >= *c.ContextWindow {
				junk++
				if junk <= 20 {
					fmt.Printf("  %s/%s %d/%d\n", p.ID, mid, *c.MaxOutputTokens, *c.ContextWindow)
				}
			}
		}
	}
	fmt.Printf("  total: %d\n", junk)

	fmt.Println("\n== overlay pins whose upstream value differs (re-examine the pin)")
	for _, pid := range sortedKeys(overlay.Providers) {
		up, ok := byID[pid]
		if !ok {
			continue
		}
		for _, mid := range sortedKeys(overlay.Providers[pid].Models) {
			pin := overlay.Providers[pid].Models[mid].Caps
			upRow, ok := up.Models[mid]
			if !ok {
				continue
			}
			if pin.ContextWindow != nil && upRow.Caps.ContextWindow != nil && *pin.ContextWindow != *upRow.Caps.ContextWindow {
				fmt.Printf("  %s/%s context_window: overlay %d, upstream %d\n", pid, mid, *pin.ContextWindow, *upRow.Caps.ContextWindow)
			}
			if pin.MaxOutputTokens != nil && upRow.Caps.MaxOutputTokens != nil && *pin.MaxOutputTokens != *upRow.Caps.MaxOutputTokens {
				fmt.Printf("  %s/%s max_output_tokens: overlay %d, upstream %d\n", pid, mid, *pin.MaxOutputTokens, *upRow.Caps.MaxOutputTokens)
			}
		}
		if _, ok := up.Models["claude-mythos-5"]; ok && pid == "anthropic" {
			fmt.Println("  anthropic/claude-mythos-5 now exists upstream; drop the overlay alias")
		}
		if _, ok := up.Models["claude-mythos-preview"]; ok && pid == "anthropic" {
			fmt.Println("  anthropic/claude-mythos-preview now exists upstream; drop the overlay row")
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
