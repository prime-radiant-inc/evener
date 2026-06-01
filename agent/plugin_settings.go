package agent

import (
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent/internal/frontmatter"
)

// PluginSettings holds per-project settings for a plugin, loaded from
// .claude/<plugin-name>.local.md. YAML frontmatter becomes key-value
// settings and the markdown body is available as content.
type PluginSettings struct {
	Frontmatter map[string]any // parsed YAML frontmatter (nil if none)
	Body        string         // markdown body after frontmatter
}

// LoadPluginSettings reads .claude/<pluginName>.local.md from workDir.
// Returns nil, nil if the file does not exist.
func LoadPluginSettings(workDir, pluginName string) (*PluginSettings, error) {
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

	return &PluginSettings{
		Frontmatter: doc.Meta,
		Body:        doc.Body,
	}, nil
}
