// memscope prints the pages of memory scope directories as JSON, read exactly
// as evener's generated index reads them, so the memory lab's checks need no
// frontmatter parser of their own.
//
// Usage: memscope DIR... prints {"DIR": [page, ...], ...}; each page has the
// fields of agent.MemoryScopePage.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"primeradiant.com/evener/agent"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: memscope DIR...")
		os.Exit(2)
	}
	scopes := map[string][]agent.MemoryScopePage{}
	for _, dir := range os.Args[1:] {
		pages, err := agent.ListMemoryScopePages(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "memscope: %s: %v\n", dir, err)
			os.Exit(1)
		}
		scopes[dir] = pages
	}
	if err := json.NewEncoder(os.Stdout).Encode(scopes); err != nil {
		fmt.Fprintln(os.Stderr, "memscope:", err)
		os.Exit(1)
	}
}
