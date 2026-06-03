package plugin

import (
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent/internal/frontmatter"
)

// Settings holds per-project settings for a plugin, loaded from
// .claude/<plugin-name>.local.md. YAML frontmatter becomes key-value
// settings and the markdown body is available as content.
type Settings struct {
	Frontmatter map[string]any // parsed YAML frontmatter (nil if none)
	Body        string         // markdown body after frontmatter
}

// LoadSettings reads .claude/<pluginName>.local.md from workDir.
// Returns nil, nil if the file does not exist.
func LoadSettings(workDir, pluginName string) (*Settings, error) {
	path := filepath.Join(workDir, ".claude", pluginName+".local.md")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	doc, err := frontmatter.Parse(string(data))
	if err != nil {
		return nil, err
	}

	return &Settings{
		Frontmatter: doc.Meta,
		Body:        doc.Body,
	}, nil
}
