package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/envvars"
)

var serfwideUserHomeDir = os.UserHomeDir

// globalCommandsDir resolves the user-global commands directory:
// $XDG_CONFIG_HOME/serf/commands, or ~/.config/serf/commands. Mirrors
// promptpath.globalPromptsDir. Returns "" when no home is resolvable.
func globalCommandsDir() string {
	dir := envvars.XDGConfigHome.Getenv()
	if dir == "" {
		home, err := serfwideUserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "commands")
}

// DiscoverSerfWideCommands scans the user-global commands dir, then walks
// git-root→cwd scanning <dir>/.serf/commands, returning commands keyed by
// bare name. Later scans shadow earlier ones, so the deepest project dir wins
// and every project command shadows the user-global one. A nil env or empty
// cwd skips the project walk but still scans the user-global dir.
//
// Discovery is fail-soft: a missing dir is silent, and per-file problems
// (unreadable dir/file, bad name, malformed frontmatter) skip the file with a
// warning rather than failing the scan.
func DiscoverSerfWideCommands(env execenv.ExecutionEnvironment) (map[string]Command, []events.WarningData) {
	out := map[string]Command{}
	var warnings []events.WarningData

	if dir := globalCommandsDir(); dir != "" {
		scanSerfwideDir(dir, "user", out, &warnings)
	}

	if env != nil {
		cwd := strings.TrimSpace(env.WorkingDirectory())
		if cwd != "" {
			if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
				cwd = resolved
			}
			root := cwd
			if gr := execenv.GitRootOrEmpty(env, cwd); gr != "" {
				root = gr
			}
			for _, dir := range execenv.DirsFromRootToCwd(root, cwd) {
				scanSerfwideDir(filepath.Join(dir, ".serf", "commands"), "project", out, &warnings)
			}
		}
	}

	return out, warnings
}

// scanSerfwideDir parses every immediate .md file of dir into out, keyed by
// bare filename. Later directories overwrite earlier entries.
func scanSerfwideDir(dir, source string, out map[string]Command, warnings *[]events.WarningData) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			*warnings = append(*warnings, serfwideWarning("unreadable commands directory",
				fmt.Sprintf("skipping commands directory %s: %v", dir, err)))
		}
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		file := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(file)
		if err != nil {
			*warnings = append(*warnings, serfwideWarning("unreadable command file",
				fmt.Sprintf("skipping command file %s: %v", file, err)))
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		command, err := ParseCommand(data, name, "")
		if err != nil {
			*warnings = append(*warnings, serfwideWarning("malformed command file",
				fmt.Sprintf("skipping command file %s: %v", file, err)))
			continue
		}
		command.Source = source
		command.File = file
		out[name] = command
	}
}

func serfwideWarning(title, message string) events.WarningData {
	return events.WarningData{Source: "commands", Title: title, Message: message}
}
