package agent

import (
	"path/filepath"

	"primeradiant.com/evener/agent/execenv"
)

// MemoryScopePage is one page of a memory scope as the generated index reads
// it. Description is the page's own when HasDescription, else its fallback.
type MemoryScopePage struct {
	Path           string   `json:"path"`
	Description    string   `json:"description"`
	HasDescription bool     `json:"has_description"`
	Frontmatter    bool     `json:"frontmatter"`
	Unreadable     bool     `json:"unreadable"`
	Tags           []string `json:"tags"`
	Updated        string   `json:"updated"`
	By             string   `json:"by"`
}

// ListMemoryScopePages reads the pages of the memory scope directory dir
// exactly as the generated index does, so a tool outside the agent (the
// memory lab's checks) needn't reimplement frontmatter parsing. A missing
// dir, or one that isn't a directory, is an error; nothing is created.
func ListMemoryScopePages(dir string) ([]MemoryScopePage, error) {
	// Absolute first: the listing joins the environment's working directory
	// onto its root, so a relative root would be resolved twice.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	pages, err := listMemoryPages(execenv.NewLocalExecutionEnvironment(abs))
	if err != nil {
		return nil, err
	}
	out := make([]MemoryScopePage, 0, len(pages))
	for _, p := range pages {
		out = append(out, MemoryScopePage{
			Path: p.Path, Description: p.Description, HasDescription: p.HasDescription, Frontmatter: p.Frontmatter,
			Unreadable: p.Unreadable, Tags: p.Tags, Updated: p.Updated, By: p.By,
		})
	}
	return out, nil
}
