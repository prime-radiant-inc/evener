package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/serf/envvars"
)

// builtinAgentNames are the agents compiled into the binary (defaultPersona.txt
// etc.) and shown on Settings → Agents. They have no on-disk file to open, so
// every row's EditPath stays empty. Consumed by hubSettingsOverview
// (serf/settings/overview), the appwire data path behind Settings → Agents.
var builtinAgentNames = []string{"default", "explorer", "subagent"}

// settingsSpawnTimeoutDisplay is the Settings → General/Hub "Spawn timeout"
// value. It is a display literal, not derived from live spawner config —
// there is no configurable spawn timeout today. Consumed by
// hubSettingsOverview for the same reason as builtinAgentNames above.
const settingsSpawnTimeoutDisplay = "30s"

// handleSettings serves the SPA shell for /settings and /settings/{section};
// client-side routing selects the section.
func (s *WebServer) handleSettings(w http.ResponseWriter, r *http.Request) {
	serveSPAIndex(w, r, distFS())
}

// tildeHome replaces the user's home directory prefix in path with "~".
// Returns path unchanged if home is empty or path does not start with home.
func tildeHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	if path == home {
		return "~"
	}
	return path
}

// fileAgeHuman returns a short human-readable description of how long ago the
// file at path was last modified (e.g. "created 3d ago"). Returns "" on error.
func fileAgeHuman(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	d := time.Since(info.ModTime())
	switch {
	case d < 2*time.Minute:
		return "just now"
	case d < 2*time.Hour:
		return fmt.Sprintf("created %dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("created %dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("created %dd ago", int(d.Hours()/24))
	}
}

// fileSizeHuman returns a short human-readable file size string for path
// (e.g. "48 MB"). Returns "" if the file does not exist or stat fails.
func fileSizeHuman(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	sz := info.Size()
	switch {
	case sz < 1<<10:
		return fmt.Sprintf("%d B", sz)
	case sz < 1<<20:
		return fmt.Sprintf("%d KB", sz>>10)
	case sz < 1<<30:
		return fmt.Sprintf("%d MB", sz>>20)
	default:
		return fmt.Sprintf("%d GB", sz>>30)
	}
}

// defaultMCPConfigPath is the conventional XDG location for the global
// MCP config (~/.config/serf/mcp.json), matching agent.globalMCPConfigPath.
func defaultMCPConfigPath() string {
	dir := envvars.XDGConfigHome.Getenv()
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "mcp.json")
}
